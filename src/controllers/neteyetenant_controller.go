// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/alyvix"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// NetEyeTenantFinalizer lets the tenant remove its metadata from the
	// namespace before the resource goes away. The namespace is not owned by the
	// tenant, so garbage collection does not clean it.
	NetEyeTenantFinalizer = "neteye.cloud/neteye-tenant"

	// tenantNamespaceComponentID is the tenant namespace: its tenant metadata
	// and its network baseline. Later tenant components depend on it.
	tenantNamespaceComponentID componentID = "namespace"

	// tenantAlyvixRDPComponentID is the RDP access to the Alyvix sessions of
	// the tenant: Apache Guacamole and guacd. It runs while the tenant enables
	// the Alyvix feature module.
	tenantAlyvixRDPComponentID componentID = "alyvix-rdp"
)

var tenantNamePattern = regexp.MustCompile(neteye.TenantNamePattern)

// NetEyeTenantReconciler reconciles NetEyeTenant resources.
type NetEyeTenantReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// APIReader reads from the API server without the cache. The deletion of a
	// tenant uses it. When nil, the client is used.
	APIReader client.Reader

	// RDPComponent reconciles the RDP access to the Alyvix sessions of a
	// tenant. When nil, the component is reported as disabled.
	RDPComponent *alyvix.RDPComponent

	// OperatorUsername is the Kubernetes username of the operator, the only
	// user that the tenant namespace admission policy exempts. The tenant uses
	// it to check that the policy in the cluster is the one of the operator.
	OperatorUsername string

	// Requeue intervals. When zero, the matching Default*RequeueAfter is used.
	FailureRequeueAfter        time.Duration
	ProgressingRequeueAfter    time.Duration
	ReconciliationRequeueAfter time.Duration

	// releasePlans overrides defaultTenantReleasePlans in tests.
	releasePlans map[string]tenantReleasePlan
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/finalizers,verbs=update
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyeproceduregates,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingadmissionpolicies;validatingadmissionpolicybindings,verbs=get;list;watch;create;update

// Reconcile reconciles a NetEyeTenant resource with the desired cluster state.
func (r *NetEyeTenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("neteyetenant", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)

	tenant := &neteye.NetEyeTenant{}
	if err := r.Get(ctx, req.NamespacedName, tenant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tenant.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, tenant)
	}

	result, err := r.reconcileTenant(ctx, tenant)
	r.writeStatus(ctx, tenant)
	if err == nil && tenant.Status.Phase == neteye.PhaseReady {
		log.Info("NetEyeTenant is ready", "requeueAfter", result.RequeueAfter)
	}
	return result, err
}

// reconcileTenant follows the version rules of the NetEye resource: a tenant
// at the previous version waits for its upgrade, an install or an upgrade
// waits for the procedure gates of its components, and status.currentVersion
// records the version once every component is ready. A tenant that is not
// valid changes nothing outside itself: the admission webhook normally refuses
// it, and this check keeps a tenant that bypassed the webhook from touching
// another namespace.
func (r *NetEyeTenantReconciler) reconcileTenant(ctx context.Context, tenant *neteye.NetEyeTenant) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	version := tenant.Spec.Version
	failed := func(message string) (ctrl.Result, error) {
		setTenantPhase(tenant, neteye.PhaseFailed, message)
		log.Error(nil, "NetEyeTenant cannot be reconciled", "reason", message, "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	switch {
	case neteye.IsPreviousVersion(version):
		setTenantPhase(tenant, neteye.PhasePendingUpgrades, fmt.Sprintf("Tenant version '%s' is not the latest; consider upgrading to '%s'. Reconciliation will be paused until the upgrade is performed.", version, neteye.CurrentNetEyeVersion))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	case !neteye.IsLatestVersion(version):
		return failed(fmt.Sprintf("Tenant version '%s' is not the latest. Latest version is '%s'. Reconciliation will be paused until the mismatch has been resolved.", version, neteye.CurrentNetEyeVersion))
	}
	procedure, ok := lifecycleForVersions(tenant.Status.CurrentVersion, version)
	if !ok {
		return failed(fmt.Sprintf("cannot upgrade the tenant from '%s' to '%s'; only upgrades from '%s' are supported", tenant.Status.CurrentVersion, version, neteye.PreviousNetEyeVersion))
	}
	if invalid := invalidTenantReason(tenant); invalid != "" {
		return failed(invalid)
	}
	if procedure == lifecycleUpgrade {
		completed, err := r.netEyeCompleted(ctx, version)
		if err != nil {
			setTenantPhase(tenant, neteye.PhaseFailed, err.Error())
			log.Error(err, "unable to read the NetEye resource", "requeueAfter", r.failureRequeue())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		if !completed {
			setTenantPhase(tenant, neteye.PhaseUpgrading, fmt.Sprintf("Upgrading the tenant to NetEye %s; waiting for the NetEye resource to complete NetEye %s", version, version))
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
	}
	if controllerutil.AddFinalizer(tenant, NetEyeTenantFinalizer) {
		if err := r.Update(ctx, tenant); err != nil {
			setTenantPhase(tenant, neteye.PhaseFailed, fmt.Sprintf("failed to add the finalizer: %v", err))
			return ctrl.Result{}, fmt.Errorf("add NetEyeTenant finalizer: %w", err)
		}
	}
	holder, err := r.claimDisplayName(ctx, tenant)
	if err != nil {
		setTenantPhase(tenant, neteye.PhaseFailed, fmt.Sprintf("failed to claim the display name: %v", err))
		log.Error(err, "unable to claim the display name of the tenant", "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if holder != "" {
		return failed(fmt.Sprintf("display name %q is already used by tenant %s; choose another display name", tenant.Spec.DisplayName, holder))
	}

	graph, err := newLifecycleGraph(tenantLifecycleNodes(tenant))
	if err != nil {
		setTenantPhase(tenant, neteye.PhaseFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("construct tenant component lifecycle graph: %w", err)
	}
	operations := map[componentID]componentOperation{
		tenantNamespaceComponentID: func() (componentResult, error) { return r.reconcileNamespace(ctx, tenant) },
		tenantAlyvixRDPComponentID: func() (componentResult, error) { return r.reconcileAlyvixRDP(ctx, tenant) },
	}
	if procedure != lifecycleSteady && tenant.Spec.EffectiveUseProcedureGates() {
		present, err := listProcedureGates(ctx, r.Client, tenant.Namespace, version)
		if err != nil {
			setTenantPhase(tenant, neteye.PhaseFailed, err.Error())
			log.Error(err, "failed to read the procedure gates of the tenant", "requeueAfter", r.failureRequeue())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		plan := r.gatePlanFor(version, procedure)
		for id, op := range operations {
			operations[id] = r.gatedOperation(ctx, tenant, id, plan[id], present, op)
		}
	}
	results, err := runComponentOperations(graph, operations)
	if err != nil {
		setTenantPhase(tenant, neteye.PhaseFailed, err.Error())
		return ctrl.Result{}, err
	}
	for _, result := range results {
		if result.Err != nil {
			log.Error(result.Err, "tenant component reconciliation failed", "component", result.ID, "reason", result.Reason, "requeueAfter", result.RequeueAfter)
		}
	}
	phase, message := tenantComponentPhase(results)
	phase, message = tenantLifecyclePhase(tenant, procedure, phase, message)
	setTenantPhase(tenant, phase, message)

	requeueAfter := earliestComponentRequeue(results)
	if requeueAfter == 0 {
		requeueAfter = r.reconciliationRequeue()
	}
	// The namespace reconciliation shows the display name on the namespace, so
	// the names that the tenant no longer shows can go now.
	held, err := r.heldDisplayNameLeases(ctx, tenant)
	if err == nil {
		err = r.releaseDisplayNames(ctx, tenant, held)
	}
	if err != nil {
		log.Error(err, "unable to release the old display names of the tenant", "requeueAfter", r.failureRequeue())
		requeueAfter = min(requeueAfter, r.failureRequeue())
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// netEyeCompleted reports whether the NetEye resource has completed version.
// A tenant upgrade waits for it, so the shared components are at the new
// version before the tenant changes. A tenant install does not wait: it has
// no state of an older version to keep consistent.
func (r *NetEyeTenantReconciler) netEyeCompleted(ctx context.Context, version string) (bool, error) {
	installations := &neteye.NetEyeList{}
	if err := r.List(ctx, installations); err != nil {
		return false, fmt.Errorf("list NetEye resources: %w", err)
	}
	for i := range installations.Items {
		if installations.Items[i].Status.CurrentVersion == version {
			return true, nil
		}
	}
	return false, nil
}

// tenantLifecycleNodes declares what each tenant component waits for. The RDP
// access waits for the namespace only while the tenant enables Alyvix: its
// removal must not wait for the namespace baseline, so a disabled RDP access
// is removed on every pass.
func tenantLifecycleNodes(tenant *neteye.NetEyeTenant) []lifecycleNode {
	rdp := lifecycleNode{ID: tenantAlyvixRDPComponentID}
	if tenant.Spec.AlyvixEnabled() {
		rdp.Dependencies = []componentID{tenantNamespaceComponentID}
	}
	return []lifecycleNode{{ID: tenantNamespaceComponentID}, rdp}
}

func invalidTenantReason(tenant *neteye.NetEyeTenant) string {
	if !tenantNamePattern.MatchString(tenant.Name) {
		return fmt.Sprintf("tenant name %q does not match %s", tenant.Name, neteye.TenantNamePattern)
	}
	if errors := neteye.ValidateTenantNamespace(tenant); len(errors) > 0 {
		return errors.ToAggregate().Error()
	}
	return ""
}

// reconcileNamespace marks the namespace as the namespace of the tenant and
// closes it with a default-deny baseline for ingress and egress. Components
// that the tenant adds later open only the traffic they need.
func (r *NetEyeTenantReconciler) reconcileNamespace(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	if err := r.ensureNamespaceMetadata(ctx, tenant); err != nil {
		return degradedResult(tenantNamespaceComponentID, "NamespaceMetadataFailed", err.Error(), r.failureRequeue(), err)
	}
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	if err := resources.ApplyTenantDefaultDenyNetworkPolicy(ctx, r.Client, tenant.Namespace, owner); err != nil {
		return degradedResult(tenantNamespaceComponentID, "NetworkPolicyFailed", fmt.Sprintf("failed to apply the default-deny network policy: %v", err), r.failureRequeue(), err)
	}
	// Without the admission policy, anyone who can edit namespaces can change
	// the tenant metadata, so the namespace is not ready until the API server
	// enforces it.
	state, message, err := checkTenantNamespacePolicy(ctx, r.Client, r.OperatorUsername)
	switch {
	case err != nil:
		return degradedResult(tenantNamespaceComponentID, "NamespacePolicyCheckFailed", fmt.Sprintf("failed to check the tenant namespace admission policy: %v", err), r.failureRequeue(), err)
	case state == tenantPolicyConflict:
		return degradedResult(tenantNamespaceComponentID, "NamespacePolicyConflict", message, r.failureRequeue(), fmt.Errorf("%s", message))
	case state == tenantPolicyMissing:
		return progressingResult(tenantNamespaceComponentID, "WaitingForNamespacePolicy", message, r.failureRequeue())
	}
	return readyResult(tenantNamespaceComponentID, "Available", "Tenant namespace is ready")
}

// reconcileAlyvixRDP deploys the RDP access to the Alyvix sessions in the
// tenant namespace while the tenant enables the Alyvix feature module, and
// removes it otherwise. The images come from the NetEye version of the NetEye
// resource, which also names the Gateway.
func (r *NetEyeTenantReconciler) reconcileAlyvixRDP(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	if !tenant.Spec.AlyvixEnabled() || r.RDPComponent == nil {
		if r.RDPComponent != nil {
			if err := r.RDPComponent.Delete(ctx, tenant.Namespace, owner); err != nil {
				return degradedResult(tenantAlyvixRDPComponentID, "DeleteFailed", fmt.Sprintf("failed to remove the RDP access: %v", err), r.failureRequeue(), err)
			}
		}
		return readyResult(tenantAlyvixRDPComponentID, "Disabled", "The Alyvix feature module is not enabled for the tenant")
	}
	ne, err := r.netEye(ctx)
	if err != nil {
		return degradedResult(tenantAlyvixRDPComponentID, "NetEyeUnavailable", fmt.Sprintf("failed to read the NetEye resource: %v", err), r.failureRequeue(), err)
	}
	if ne == nil {
		return progressingResult(tenantAlyvixRDPComponentID, "WaitingForNetEye", fmt.Sprintf("waiting for the NetEye resource in namespace %q, which selects the images and the Gateway", neteye.NetEyeNamespace), r.progressingRequeue())
	}
	components, ok := neteye.ComponentsForVersion(ne.Spec.Version)
	if !ok {
		message := fmt.Sprintf("unsupported NetEye version '%s'; supported versions are: %v", ne.Spec.Version, neteye.SupportedVersions())
		return degradedResult(tenantAlyvixRDPComponentID, "UnsupportedVersion", message, r.failureRequeue(), fmt.Errorf("%s", message))
	}
	var gateway *resources.GatewayRef
	if ne.Spec.Gateway.EffectiveManaged() {
		gateway = &resources.GatewayRef{Namespace: ne.Namespace, Name: ne.Spec.Gateway.Name}
	}
	outcome := r.RDPComponent.Ensure(ctx, alyvix.RDPInputs{
		Namespace:      tenant.Namespace,
		GuacamoleImage: components.GuacamoleImage,
		GuacdImage:     components.GuacdImage,
		Ports:          tenant.Spec.Alyvix.EffectiveRDPPorts(),
		CABundle:       caBundleRefForTenant(tenant),
		Gateway:        gateway,
	}, owner)
	if gateway != nil && outcome.Phase != alyvix.PhaseDegraded {
		if result, waiting, err := r.waitForTenantRoutePolicy(ctx); waiting {
			return result, err
		}
	}
	switch outcome.Phase {
	case alyvix.PhaseReady:
		return readyResult(tenantAlyvixRDPComponentID, outcome.Reason, outcome.Message)
	case alyvix.PhaseProgressing:
		return progressingResult(tenantAlyvixRDPComponentID, outcome.Reason, outcome.Message, r.progressingRequeue())
	default:
		return degradedResult(tenantAlyvixRDPComponentID, outcome.Reason, outcome.Message, r.failureRequeue(), outcome.Err)
	}
}

func caBundleRefForTenant(tenant *neteye.NetEyeTenant) resources.CABundleRef {
	selector := tenant.Spec.EffectiveCABundle()
	return resources.CABundleRef{Name: selector.Name, Key: selector.Key}
}

// waitForTenantRoutePolicy reports the RDP access not ready while the API
// server does not enforce the route policy. The listener of the RDP access is
// shared by the tenants, so without the policy whoever can write the routes of
// a tenant namespace can serve the hostname of another tenant.
func (r *NetEyeTenantReconciler) waitForTenantRoutePolicy(ctx context.Context) (componentResult, bool, error) {
	state, message, err := checkTenantAdmissionPolicy(ctx, r.Client, tenantRoutePolicy())
	switch {
	case err != nil:
		result, err := degradedResult(tenantAlyvixRDPComponentID, "RoutePolicyCheckFailed", fmt.Sprintf("failed to check the tenant route admission policy: %v", err), r.failureRequeue(), err)
		return result, true, err
	case state == tenantPolicyConflict:
		result, err := degradedResult(tenantAlyvixRDPComponentID, "RoutePolicyConflict", message, r.failureRequeue(), fmt.Errorf("%s", message))
		return result, true, err
	case state == tenantPolicyMissing:
		result, err := progressingResult(tenantAlyvixRDPComponentID, "WaitingForRoutePolicy", message, r.failureRequeue())
		return result, true, err
	}
	return componentResult{}, false, nil
}

// netEye returns the NetEye resource, the singleton in the shared namespace,
// or nil when there is none.
func (r *NetEyeTenantReconciler) netEye(ctx context.Context) (*neteye.NetEye, error) {
	list := &neteye.NetEyeList{}
	if err := r.List(ctx, list, client.InNamespace(neteye.NetEyeNamespace)); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return &list.Items[0], nil
}

// ensureNamespaceMetadata marks the namespace as the namespace of the tenant
// and shows the tenant display name on it.
func (r *NetEyeTenantReconciler) ensureNamespaceMetadata(ctx context.Context, tenant *neteye.NetEyeTenant) error {
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: tenant.Namespace}, namespace); err != nil {
		return fmt.Errorf("get namespace %q: %w", tenant.Namespace, err)
	}
	if namespace.Labels[neteye.TenantLabel] == tenant.Name && namespace.Annotations[neteye.TenantDisplayNameAnnotation] == tenant.Spec.DisplayName {
		return nil
	}
	patch := client.MergeFrom(namespace.DeepCopy())
	if namespace.Labels == nil {
		namespace.Labels = map[string]string{}
	}
	if namespace.Annotations == nil {
		namespace.Annotations = map[string]string{}
	}
	namespace.Labels[neteye.TenantLabel] = tenant.Name
	namespace.Annotations[neteye.TenantDisplayNameAnnotation] = tenant.Spec.DisplayName
	if err := r.Patch(ctx, namespace, patch); err != nil {
		return fmt.Errorf("update the tenant metadata of namespace %q: %w", tenant.Namespace, err)
	}
	return nil
}

// reconcileDelete removes the tenant metadata from the namespace, so that the
// namespace no longer counts as a tenant namespace, and then releases the
// display name. The namespace itself and
// anything else in it stay: deleting a tenant never deletes the namespace that
// was created for it. Objects owned by the tenant, such as its network policy,
// are removed by garbage collection.
func (r *NetEyeTenantReconciler) reconcileDelete(ctx context.Context, tenant *neteye.NetEyeTenant) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(tenant, NetEyeTenantFinalizer) {
		return ctrl.Result{}, nil
	}
	// A tenant that is not valid never wrote metadata, so it must not remove
	// the metadata that another tenant wrote on that namespace.
	if invalidTenantReason(tenant) != "" {
		return r.removeFinalizer(ctx, tenant)
	}
	if err := r.removeNamespaceMetadata(ctx, tenant); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to remove the tenant metadata from the namespace", "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if err := r.releaseDisplayNames(ctx, tenant, nil); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to release the display name of the tenant", "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	ctrl.LoggerFrom(ctx).Info("NetEyeTenant deleted; its namespace is no longer marked as a tenant namespace", "namespace", tenant.Namespace)
	return r.removeFinalizer(ctx, tenant)
}

func (r *NetEyeTenantReconciler) removeFinalizer(ctx context.Context, tenant *neteye.NetEyeTenant) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(tenant, NetEyeTenantFinalizer)
	if err := r.Update(ctx, tenant); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove NetEyeTenant finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// removeNamespaceMetadata removes the label and the annotation that this
// tenant wrote. It reads the namespace from the API server, not from the
// cache: after the finalizer is gone nothing reconciles the namespace again,
// and only the operator can remove the metadata, so a stale read would leave
// it on the namespace for good. A label with the name of another tenant is
// left alone.
func (r *NetEyeTenantReconciler) removeNamespaceMetadata(ctx context.Context, tenant *neteye.NetEyeTenant) error {
	namespace := &corev1.Namespace{}
	if err := r.apiReader().Get(ctx, types.NamespacedName{Name: tenant.Namespace}, namespace); err != nil {
		return client.IgnoreNotFound(err)
	}
	label, labeled := namespace.Labels[neteye.TenantLabel]
	_, annotated := namespace.Annotations[neteye.TenantDisplayNameAnnotation]
	if labeled && label != tenant.Name || !labeled && !annotated {
		return nil
	}
	patch := client.MergeFrom(namespace.DeepCopy())
	delete(namespace.Labels, neteye.TenantLabel)
	delete(namespace.Annotations, neteye.TenantDisplayNameAnnotation)
	if err := r.Patch(ctx, namespace, patch); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove the tenant metadata of namespace %q: %w", tenant.Namespace, err)
	}
	return nil
}

func (r *NetEyeTenantReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// writeStatus publishes the status of this pass.
func (r *NetEyeTenantReconciler) writeStatus(ctx context.Context, tenant *neteye.NetEyeTenant) {
	status := tenant.Status
	status.ObservedGeneration = tenant.Generation
	writeStatus(ctx, r.Client, client.ObjectKeyFromObject(tenant), func() *neteye.NetEyeTenant { return &neteye.NetEyeTenant{} }, func(current *neteye.NetEyeTenant) {
		current.Status = status
	})
}

func setTenantPhase(tenant *neteye.NetEyeTenant, phase neteye.NetEyePhase, message string) {
	tenant.Status.Phase = phase
	tenant.Status.Message = message
}

// tenantComponentPhase derives the phase from the component results. The
// tenant has no per-component status, so the message names the components
// that are not ready and says why. Names are sorted, so the message does not
// change from one reconciliation to the next while the state stays the same.
func tenantComponentPhase(results map[componentID]componentResult) (neteye.NetEyePhase, string) {
	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	var degraded, waiting []string
	for _, id := range ids {
		result := results[componentID(id)]
		switch result.State {
		case componentStateDegraded:
			degraded = append(degraded, id+": "+result.Message)
		case componentStateProgressing, componentStateBlocked:
			waiting = append(waiting, id+": "+result.Message)
		}
	}
	switch {
	case len(degraded) > 0:
		return neteye.PhaseFailed, "Failed components: " + strings.Join(degraded, "; ")
	case len(waiting) > 0:
		return neteye.PhaseNotReady, "Waiting for components: " + strings.Join(waiting, "; ")
	}
	return neteye.PhaseReady, "All components are ready"
}

// tenantLifecyclePhase folds an unfinished install or upgrade into the phase.
// The procedure completes, and the version is recorded, once every component
// is ready.
func tenantLifecyclePhase(tenant *neteye.NetEyeTenant, procedure lifecycle, phase neteye.NetEyePhase, message string) (neteye.NetEyePhase, string) {
	switch {
	case procedure == lifecycleSteady || phase == neteye.PhaseFailed:
		return phase, message
	case phase == neteye.PhaseReady:
		tenant.Status.CurrentVersion = tenant.Spec.Version
		return phase, message
	case procedure == lifecycleInstall:
		return neteye.PhaseInstalling, fmt.Sprintf("Installing NetEye %s in the tenant; %s", tenant.Spec.Version, message)
	default:
		return neteye.PhaseUpgrading, fmt.Sprintf("Upgrading the tenant to NetEye %s; %s", tenant.Spec.Version, message)
	}
}

func (r *NetEyeTenantReconciler) failureRequeue() time.Duration {
	if r.FailureRequeueAfter > 0 {
		return r.FailureRequeueAfter
	}
	return DefaultFailureRequeueAfter
}

func (r *NetEyeTenantReconciler) progressingRequeue() time.Duration {
	if r.ProgressingRequeueAfter > 0 {
		return r.ProgressingRequeueAfter
	}
	return DefaultWaitForProgressingRequeueAfter
}

func (r *NetEyeTenantReconciler) reconciliationRequeue() time.Duration {
	if r.ReconciliationRequeueAfter > 0 {
		return r.ReconciliationRequeueAfter
	}
	return DefaultReconciliationRequeueAfter
}

// SetupWithManager registers the controller. Besides the tenant, it watches
// the objects the tenant owns and the tenant namespaces, so drift in either is
// corrected at once instead of at the next periodic reconciliation, and the
// procedure gates in the tenant namespaces, so an install or an upgrade
// continues as soon as a gate it waits for exists. A NetEye resource that
// completes a version reconciles every tenant, because their upgrades wait for
// it, and so does a change of its spec, which selects the images and the
// Gateway of the tenant components. Routes are not watched, so a
// cluster without the Gateway API can still start the controller; their status
// is read again at the next requeue.
// It also watches the admission policies of the tenant namespaces and their
// bindings, so the readiness of every tenant follows their state at once.
func (r *NetEyeTenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	networkPolicy := &unstructured.Unstructured{}
	networkPolicy.SetGroupVersionKind(resources.NetworkPolicyGVK())
	admissionPolicy := &unstructured.Unstructured{}
	admissionPolicy.SetGroupVersionKind(validatingAdmissionPolicyGVK)
	admissionPolicyBinding := &unstructured.Unstructured{}
	admissionPolicyBinding.SetGroupVersionKind(validatingAdmissionPolicyBindingGVK)
	onlyTheTenantPolicies := builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
		return object.GetName() == TenantNamespacePolicyName || object.GetName() == TenantRoutePolicyName
	}))
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEyeTenant{}, builder.WithPredicates(reconcileOnSpecOrDeletionChange)).
		Owns(networkPolicy).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(tenantForNamespace)).
		Watches(&neteye.NetEyeProcedureGate{}, handler.EnqueueRequestsFromMapFunc(tenantForProcedureGate)).
		Watches(&neteye.NetEye{}, handler.EnqueueRequestsFromMapFunc(r.allTenants), builder.WithPredicates(predicate.Or(netEyeCurrentVersionChanged, reconcileOnSpecOrDeletionChange))).
		Watches(admissionPolicy, handler.EnqueueRequestsFromMapFunc(r.allTenants), onlyTheTenantPolicies).
		Watches(admissionPolicyBinding, handler.EnqueueRequestsFromMapFunc(r.allTenants), onlyTheTenantPolicies).
		Complete(r)
}

// allTenants maps a change of the shared admission policy, or of the NetEye
// resource, to every tenant, because all tenants depend on them.
func (r *NetEyeTenantReconciler) allTenants(ctx context.Context, _ client.Object) []reconcile.Request {
	tenants := &neteye.NetEyeTenantList{}
	if err := r.List(ctx, tenants); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list NetEyeTenants after a change of the tenant namespace admission policy")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(tenants.Items))
	for _, tenant := range tenants.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: tenant.Namespace, Name: tenant.Name}})
	}
	return requests
}

// tenantForNamespace maps a namespace to the tenant it may contain, which has
// the same name.
func tenantForNamespace(_ context.Context, namespace client.Object) []reconcile.Request {
	if !strings.HasPrefix(namespace.GetName(), neteye.TenantNamePrefix) {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace.GetName(), Name: namespace.GetName()}}}
}

// tenantForProcedureGate maps a procedure gate to the tenant of its namespace.
// The gates of the NetEye resource live in the shared namespace, which has no
// tenant.
func tenantForProcedureGate(_ context.Context, gate client.Object) []reconcile.Request {
	if !tenantNamePattern.MatchString(gate.GetNamespace()) {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: gate.GetNamespace(), Name: gate.GetNamespace()}}}
}

// netEyeCurrentVersionChanged passes a NetEye resource whose
// status.currentVersion changed.
var netEyeCurrentVersionChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		before, beforeOK := e.ObjectOld.(*neteye.NetEye)
		after, afterOK := e.ObjectNew.(*neteye.NetEye)
		return beforeOK && afterOK && before.Status.CurrentVersion != after.Status.CurrentVersion
	},
	CreateFunc:  func(event.CreateEvent) bool { return true },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
}
