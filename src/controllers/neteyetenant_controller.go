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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/permissionsync"
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

	// tenantPermissionSyncComponentID is the PermissionSync instance of the
	// tenant. It runs in the tenant namespace, so it depends on it, and on
	// the realm of the tenant when it trusts that realm.
	tenantPermissionSyncComponentID componentID = "permission-sync"
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

	// AdminProviders gives the Admin API client of the internal Keycloak
	// administrator, which creates the realm of the tenant and its
	// administrator account. The KeycloakRealm controller shares it.
	AdminProviders *keycloak.AdminProviderRegistry

	// OperatorUsername is the Kubernetes username of the operator, the only
	// user that the tenant namespace admission policy exempts. The tenant uses
	// it to check that the policy in the cluster is the one of the operator.
	OperatorUsername string

	// Requeue intervals. When zero, the matching Default*RequeueAfter is used.
	FailureRequeueAfter        time.Duration
	ReconciliationRequeueAfter time.Duration

	// releasePlans overrides defaultTenantReleasePlans in tests.
	releasePlans map[string]tenantReleasePlan
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/finalizers,verbs=update
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyeproceduregates,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=configmaps;services,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakrealms,verbs=get;list;watch;create;patch;delete
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

	permissionSyncDependencies := []componentID{tenantNamespaceComponentID}
	if tenant.Spec.PermissionSync.EffectiveRealm(tenant.Name) == tenant.Name {
		permissionSyncDependencies = append(permissionSyncDependencies, tenantRealmComponentID)
	}
	graph, err := newLifecycleGraph([]lifecycleNode{
		{ID: tenantNamespaceComponentID},
		{ID: tenantRealmComponentID, Dependencies: []componentID{tenantNamespaceComponentID}},
		{ID: tenantPermissionSyncComponentID, Dependencies: permissionSyncDependencies},
	})
	if err != nil {
		setTenantPhase(tenant, neteye.PhaseFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("construct tenant component lifecycle graph: %w", err)
	}
	operations := map[componentID]componentOperation{
		tenantNamespaceComponentID:      func() (componentResult, error) { return r.reconcileNamespace(ctx, tenant) },
		tenantRealmComponentID:          func() (componentResult, error) { return r.reconcileRealm(ctx, tenant) },
		tenantPermissionSyncComponentID: func() (componentResult, error) { return r.reconcilePermissionSync(ctx, tenant) },
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
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
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
	case state == tenantNamespacePolicyConflict:
		return degradedResult(tenantNamespaceComponentID, "NamespacePolicyConflict", message, r.failureRequeue(), fmt.Errorf("%s", message))
	case state == tenantNamespacePolicyMissing:
		return progressingResult(tenantNamespaceComponentID, "WaitingForNamespacePolicy", message, r.failureRequeue())
	}
	return readyResult(tenantNamespaceComponentID, "Available", "Tenant namespace is ready")
}

// reconcilePermissionSync deploys the PermissionSync instance of the tenant.
// The version of the tenant gives the image, and the tenant gives the CA
// bundle. The NetEye resource gives the public hostname of Keycloak, so the
// trusted issuer, and tells whether the operator manages the Gateway.
func (r *NetEyeTenantReconciler) reconcilePermissionSync(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	installations := &neteye.NetEyeList{}
	if err := r.List(ctx, installations); err != nil {
		return degradedResult(tenantPermissionSyncComponentID, "NetEyeReadFailed", fmt.Sprintf("failed to read the NetEye resource: %v", err), r.failureRequeue(), err)
	}
	if len(installations.Items) == 0 {
		return progressingResult(tenantPermissionSyncComponentID, "WaitingForNetEye", "waiting for the NetEye resource, which gives the identity hostname", r.failureRequeue())
	}
	release, ok := neteye.ComponentsForVersion(tenant.Spec.Version)
	if !ok {
		err := fmt.Errorf("NetEye version %q is not supported", tenant.Spec.Version)
		return degradedResult(tenantPermissionSyncComponentID, "UnsupportedVersion", err.Error(), r.failureRequeue(), err)
	}
	return r.ensurePermissionSync(ctx, tenant, &installations.Items[0], release.PermissionSyncImage)
}

func (r *NetEyeTenantReconciler) ensurePermissionSync(ctx context.Context, tenant *neteye.NetEyeTenant, installation *neteye.NetEye, image string) (componentResult, error) {
	outcome, err := permissionsync.Ensure(ctx, r.Client, permissionsync.Input{
		Tenant:           tenant,
		IdentityHostname: installation.Spec.Identity.Hostname,
		Image:            image,
		CABundle:         tenantCABundleRef(tenant),
		ManagedGateway:   installation.Spec.Gateway.EffectiveManaged(),
	})
	if err != nil {
		return degradedResult(tenantPermissionSyncComponentID, "ReconcileFailed", err.Error(), r.failureRequeue(), err)
	}
	switch outcome.State {
	case permissionsync.StateReady:
		return readyResult(tenantPermissionSyncComponentID, outcome.Reason, outcome.Message)
	case permissionsync.StateInvalid:
		return degradedResult(tenantPermissionSyncComponentID, outcome.Reason, outcome.Message, r.failureRequeue(), fmt.Errorf("%s", outcome.Message))
	default:
		return progressingResult(tenantPermissionSyncComponentID, outcome.Reason, outcome.Message, r.failureRequeue())
	}
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

// reconcileDelete removes the Keycloak realm of the tenant and its
// administrator, then the tenant metadata from the namespace, so that the
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
	removed, err := r.deleteTenantRealm(ctx, tenant)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to delete the Keycloak realm of the tenant", "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if !removed {
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if err := r.removeNamespaceMetadata(ctx, tenant); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to remove the tenant metadata from the namespace", "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if err := r.releaseDisplayNames(ctx, tenant, ""); err != nil {
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
// tenant has no per-component status, so the message lists every component
// that is not ready, with its state, its reason and why. Names are sorted, so
// the message does not change from one reconciliation to the next while the
// state stays the same.
func tenantComponentPhase(results map[componentID]componentResult) (neteye.NetEyePhase, string) {
	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	phase := neteye.PhaseReady
	var notReady []string
	for _, id := range ids {
		result := results[componentID(id)]
		switch result.State {
		case componentStateReady:
			continue
		case componentStateDegraded:
			phase = neteye.PhaseFailed
		default:
			if phase == neteye.PhaseReady {
				phase = neteye.PhaseNotReady
			}
		}
		notReady = append(notReady, fmt.Sprintf("%s %s (%s): %s", id, result.State, result.Reason, result.Message))
	}
	if len(notReady) == 0 {
		return neteye.PhaseReady, "All components are ready"
	}
	return phase, "Components not ready: " + strings.Join(notReady, "; ")
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

func (r *NetEyeTenantReconciler) reconciliationRequeue() time.Duration {
	if r.ReconciliationRequeueAfter > 0 {
		return r.ReconciliationRequeueAfter
	}
	return DefaultReconciliationRequeueAfter
}

// SetupWithManager registers the controller. Besides the tenant, it watches
// the network policies the tenant owns and the tenant namespaces, so drift in
// either is corrected at once instead of at the next periodic reconciliation,
// and the procedure gates in the tenant namespaces, so an install or an
// upgrade continues as soon as a gate it waits for exists.
// It also watches the admission policy that protects the tenant metadata and
// its binding, so the readiness of every tenant follows their state at once.
func (r *NetEyeTenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	networkPolicy := &unstructured.Unstructured{}
	networkPolicy.SetGroupVersionKind(resources.NetworkPolicyGVK())
	admissionPolicy := &unstructured.Unstructured{}
	admissionPolicy.SetGroupVersionKind(validatingAdmissionPolicyGVK)
	admissionPolicyBinding := &unstructured.Unstructured{}
	admissionPolicyBinding.SetGroupVersionKind(validatingAdmissionPolicyBindingGVK)
	onlyTheTenantNamespacePolicy := builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
		return object.GetName() == TenantNamespacePolicyName
	}))
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEyeTenant{}, builder.WithPredicates(reconcileOnSpecOrDeletionChange)).
		Owns(networkPolicy).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&neteye.KeycloakRealm{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.tenantsForSecret)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.tenantsForCABundle)).
		// Only the spec of NetEye matters (release and identity hostname), so
		// its status writes do not reconcile every tenant.
		Watches(&neteye.NetEye{}, handler.EnqueueRequestsFromMapFunc(r.allTenants), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(tenantForNamespace)).
		Watches(&neteye.NetEyeProcedureGate{}, handler.EnqueueRequestsFromMapFunc(tenantForProcedureGate)).
		Watches(admissionPolicy, handler.EnqueueRequestsFromMapFunc(r.allTenants), onlyTheTenantNamespacePolicy).
		Watches(admissionPolicyBinding, handler.EnqueueRequestsFromMapFunc(r.allTenants), onlyTheTenantNamespacePolicy).
		Complete(r)
}

// tenantsForSecret maps a Secret to the tenants that read it: the credentials
// of the tenant realm administrator and the GLPI credentials of PermissionSync
// to the tenant of their namespace, and the root CA of NetEye and the Keycloak
// administrator credentials of the platform to every tenant. Other Secrets of
// a tenant namespace do not reconcile the tenant, because each reconciliation
// calls the Keycloak Admin API.
func (r *NetEyeTenantReconciler) tenantsForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	if secret.GetNamespace() == keycloak.WorkloadNamespace {
		switch secret.GetName() {
		case permissionsync.RootCASecretName, keycloak.AdminSecretName, keycloak.InternalAdminSecretName:
			return r.allTenants(ctx, secret)
		}
		return nil
	}
	if !tenantNamePattern.MatchString(secret.GetNamespace()) {
		return nil
	}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: secret.GetNamespace(), Name: secret.GetNamespace()}}
	if secret.GetName() == keycloak.AdminSecretName {
		return []reconcile.Request{request}
	}
	tenant := &neteye.NetEyeTenant{}
	if err := r.Get(ctx, request.NamespacedName, tenant); err != nil {
		return nil
	}
	if glpi := tenant.Spec.PermissionSync.GLPI; glpi != nil && glpi.CredentialsSecretRef.Name == secret.GetName() {
		return []reconcile.Request{request}
	}
	return nil
}

// tenantsForCABundle maps a change of a ConfigMap in a tenant namespace to
// that tenant when the ConfigMap is its CA bundle, because PermissionSync
// trusts its certificates.
func (r *NetEyeTenantReconciler) tenantsForCABundle(ctx context.Context, configMap client.Object) []reconcile.Request {
	if !tenantNamePattern.MatchString(configMap.GetNamespace()) {
		return nil
	}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: configMap.GetNamespace(), Name: configMap.GetNamespace()}}
	tenant := &neteye.NetEyeTenant{}
	if err := r.Get(ctx, request.NamespacedName, tenant); err != nil {
		return nil
	}
	if tenant.Spec.EffectiveCABundle().Name != configMap.GetName() {
		return nil
	}
	return []reconcile.Request{request}
}

// tenantCABundleRef returns the CA bundle ConfigMap of the tenant.
func tenantCABundleRef(tenant *neteye.NetEyeTenant) resources.CABundleRef {
	selector := tenant.Spec.EffectiveCABundle()
	return resources.CABundleRef{Name: selector.Name, Key: selector.Key}
}

// allTenants maps a change of a shared object to every tenant, because all
// tenants depend on it.
func (r *NetEyeTenantReconciler) allTenants(ctx context.Context, _ client.Object) []reconcile.Request {
	tenants := &neteye.NetEyeTenantList{}
	if err := r.List(ctx, tenants); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list NetEyeTenants after a change of an object that every tenant uses")
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
	if !tenantNamePattern.MatchString(namespace.GetName()) {
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
