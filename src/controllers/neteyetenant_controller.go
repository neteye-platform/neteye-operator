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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
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

	results, err := r.reconcileComponents(ctx, tenant)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.writeStatus(ctx, tenant, results)

	for _, result := range results {
		if result.Err != nil {
			log.Error(result.Err, "tenant component reconciliation failed", "component", result.ID, "reason", result.Reason, "requeueAfter", result.RequeueAfter)
		}
	}
	requeueAfter := earliestComponentRequeue(results)
	if requeueAfter == 0 {
		requeueAfter = r.reconciliationRequeue()
		log.Info("NetEyeTenant is ready", "requeueAfter", requeueAfter)
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileComponents runs the components of the tenant in dependency order.
// A tenant that is not valid changes nothing outside itself: the admission
// webhook normally refuses it, and this check keeps a tenant that bypassed the
// webhook from touching another namespace.
func (r *NetEyeTenantReconciler) reconcileComponents(ctx context.Context, tenant *neteye.NetEyeTenant) (map[componentID]componentResult, error) {
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
		return nil, fmt.Errorf("construct tenant component lifecycle graph: %w", err)
	}
	if invalid := invalidTenantReason(tenant); invalid != "" {
		result, err := degradedResult(tenantNamespaceComponentID, "InvalidTenant", invalid, r.failureRequeue(), fmt.Errorf("%s", invalid))
		if err != nil {
			return nil, err
		}
		return map[componentID]componentResult{tenantNamespaceComponentID: result}, nil
	}
	if controllerutil.AddFinalizer(tenant, NetEyeTenantFinalizer) {
		if err := r.Update(ctx, tenant); err != nil {
			return nil, fmt.Errorf("add NetEyeTenant finalizer: %w", err)
		}
	}
	return runComponentOperations(graph, map[componentID]componentOperation{
		tenantNamespaceComponentID:      func() (componentResult, error) { return r.reconcileNamespace(ctx, tenant) },
		tenantRealmComponentID:          func() (componentResult, error) { return r.reconcileRealm(ctx, tenant) },
		tenantPermissionSyncComponentID: func() (componentResult, error) { return r.reconcilePermissionSync(ctx, tenant) },
	})
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
// The NetEye resource gives the release, so the image, the public hostname of
// Keycloak, so the trusted issuer, and the CA bundle.
func (r *NetEyeTenantReconciler) reconcilePermissionSync(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	installations := &neteye.NetEyeList{}
	if err := r.List(ctx, installations); err != nil {
		return degradedResult(tenantPermissionSyncComponentID, "NetEyeReadFailed", fmt.Sprintf("failed to read the NetEye resource: %v", err), r.failureRequeue(), err)
	}
	if len(installations.Items) == 0 {
		return progressingResult(tenantPermissionSyncComponentID, "WaitingForNetEye", "waiting for the NetEye resource, which gives the release and the identity hostname", r.failureRequeue())
	}
	installation := &installations.Items[0]
	release, ok := neteye.ComponentsForVersion(installation.Spec.Version)
	if !ok {
		err := fmt.Errorf("NetEye version %q is not supported", installation.Spec.Version)
		return degradedResult(tenantPermissionSyncComponentID, "UnsupportedVersion", err.Error(), r.failureRequeue(), err)
	}
	result, err := r.ensurePermissionSync(ctx, tenant, installation, release.PermissionSyncImage)
	result.ResolvedImages = []neteye.NetEyeResolvedImage{{Name: permissionsync.DeploymentName, Image: release.PermissionSyncImage}}
	return result, err
}

func (r *NetEyeTenantReconciler) ensurePermissionSync(ctx context.Context, tenant *neteye.NetEyeTenant, installation *neteye.NetEye, image string) (componentResult, error) {
	outcome, err := permissionsync.Ensure(ctx, r.Client, permissionsync.Input{
		Tenant:           tenant,
		IdentityHostname: installation.Spec.Identity.Hostname,
		Image:            image,
		CABundle:         caBundleRefFor(installation),
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
// namespace no longer counts as a tenant namespace. The namespace itself and
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

// writeStatus publishes the component map and the conditions derived from it.
func (r *NetEyeTenantReconciler) writeStatus(ctx context.Context, tenant *neteye.NetEyeTenant, results map[componentID]componentResult) {
	components := tenantComponentStatus(tenant.Generation, results)
	writeStatus(ctx, r.Client, client.ObjectKeyFromObject(tenant), func() *neteye.NetEyeTenant { return &neteye.NetEyeTenant{} }, func(current *neteye.NetEyeTenant) {
		current.Status.ObservedGeneration = tenant.Generation
		current.Status.Components = components
		setTenantConditions(&current.Status, tenant.Generation)
	})
}

// tenantComponentStatus converts this pass's component results into the
// component map of the status.
func tenantComponentStatus(generation int64, results map[componentID]componentResult) map[string]neteye.NetEyeComponentStatus {
	components := make(map[string]neteye.NetEyeComponentStatus, len(results))
	for id, result := range results {
		blocking := make([]string, 0, len(result.BlockingDependencies))
		for _, dependency := range result.BlockingDependencies {
			blocking = append(blocking, string(dependency))
		}
		sort.Strings(blocking)
		components[string(id)] = neteye.NetEyeComponentStatus{
			Status:               neteye.ComponentState(result.State),
			ObservedGeneration:   generation,
			Reason:               result.Reason,
			Message:              result.Message,
			BlockingDependencies: blocking,
			// A component without workloads reports the empty set of images.
			ResolvedImages: append([]neteye.NetEyeResolvedImage{}, result.ResolvedImages...),
		}
	}
	return components
}

// setTenantConditions derives Ready, Progressing and Degraded from the
// component map. Names are sorted, so the messages do not change from one
// reconciliation to the next while the state stays the same.
func setTenantConditions(status *neteye.NetEyeTenantStatus, generation int64) {
	names := make([]string, 0, len(status.Components))
	for name := range status.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	var notReady, degraded, progressing []string
	for _, name := range names {
		switch status.Components[name].Status {
		case neteye.ComponentStateReady, neteye.ComponentStateDisabled:
		case neteye.ComponentStateDegraded:
			degraded = append(degraded, name)
			notReady = append(notReady, name)
		default:
			progressing = append(progressing, name)
			notReady = append(notReady, name)
		}
	}

	ready := metav1.Condition{Type: neteye.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: generation, Reason: neteye.ReasonAllComponentsReady, Message: "All components are ready"}
	switch {
	case len(degraded) > 0:
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, neteye.ReasonComponentsDegraded, "Degraded components: "+strings.Join(degraded, ", ")
	case len(notReady) > 0:
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, neteye.ReasonComponentsNotReady, "Waiting for components: "+strings.Join(notReady, ", ")
	}
	meta.SetStatusCondition(&status.Conditions, ready)

	progressingCondition := metav1.Condition{Type: neteye.ConditionProgressing, Status: metav1.ConditionFalse, ObservedGeneration: generation, Reason: neteye.ReasonConverged, Message: "No component is converging"}
	if len(progressing) > 0 {
		progressingCondition.Status, progressingCondition.Reason, progressingCondition.Message = metav1.ConditionTrue, neteye.ReasonConverging, "Converging components: "+strings.Join(progressing, ", ")
	}
	meta.SetStatusCondition(&status.Conditions, progressingCondition)

	degradedCondition := metav1.Condition{Type: neteye.ConditionDegraded, Status: metav1.ConditionFalse, ObservedGeneration: generation, Reason: neteye.ReasonNoFailures, Message: "No component has failed"}
	if len(degraded) > 0 {
		degradedCondition.Status, degradedCondition.Reason, degradedCondition.Message = metav1.ConditionTrue, neteye.ReasonComponentsDegraded, "Degraded components: "+strings.Join(degraded, ", ")
	}
	meta.SetStatusCondition(&status.Conditions, degradedCondition)
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
// either is corrected at once instead of at the next periodic reconciliation.
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

// tenantsForCABundle maps a change of the CA bundle ConfigMap of the NetEye
// resource to every tenant, because PermissionSync trusts its certificates.
func (r *NetEyeTenantReconciler) tenantsForCABundle(ctx context.Context, configMap client.Object) []reconcile.Request {
	if configMap.GetNamespace() != keycloak.WorkloadNamespace {
		return nil
	}
	installations := &neteye.NetEyeList{}
	if err := r.List(ctx, installations); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list NetEye resources after a change of a ConfigMap in the shared namespace")
		return nil
	}
	for i := range installations.Items {
		if caBundleRefFor(&installations.Items[i]).Name == configMap.GetName() {
			return r.allTenants(ctx, configMap)
		}
	}
	return nil
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
