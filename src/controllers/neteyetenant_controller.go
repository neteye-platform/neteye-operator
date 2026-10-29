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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
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
)

var tenantNamePattern = regexp.MustCompile(neteye.TenantNamePattern)

// NetEyeTenantReconciler reconciles NetEyeTenant resources.
type NetEyeTenantReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// Requeue intervals. When zero, the matching Default*RequeueAfter is used.
	FailureRequeueAfter        time.Duration
	ReconciliationRequeueAfter time.Duration
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update
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
	graph, err := newLifecycleGraph([]lifecycleNode{{ID: tenantNamespaceComponentID}})
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
		tenantNamespaceComponentID: func() (componentResult, error) { return r.reconcileNamespace(ctx, tenant) },
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
	if err := resources.ApplyDefaultDenyNetworkPolicy(ctx, r.Client, tenant.Namespace, owner); err != nil {
		return degradedResult(tenantNamespaceComponentID, "NetworkPolicyFailed", fmt.Sprintf("failed to apply the default-deny network policy: %v", err), r.failureRequeue(), err)
	}
	return readyResult(tenantNamespaceComponentID, "Available", "Tenant namespace is ready")
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
// namespace no longer counts as a tenant namespace. The namespace itself and
// anything else in it stay: deleting a tenant never deletes the namespace that
// was created for it. Objects owned by the tenant, such as its network policy,
// are removed by garbage collection.
func (r *NetEyeTenantReconciler) reconcileDelete(ctx context.Context, tenant *neteye.NetEyeTenant) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(tenant, NetEyeTenantFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.removeNamespaceMetadata(ctx, tenant); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to remove the tenant metadata from the namespace", "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	controllerutil.RemoveFinalizer(tenant, NetEyeTenantFinalizer)
	if err := r.Update(ctx, tenant); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove NetEyeTenant finalizer: %w", err)
	}
	ctrl.LoggerFrom(ctx).Info("NetEyeTenant deleted; its namespace is no longer marked as a tenant namespace", "namespace", tenant.Namespace)
	return ctrl.Result{}, nil
}

func (r *NetEyeTenantReconciler) removeNamespaceMetadata(ctx context.Context, tenant *neteye.NetEyeTenant) error {
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: tenant.Namespace}, namespace); err != nil {
		return client.IgnoreNotFound(err)
	}
	_, labeled := namespace.Labels[neteye.TenantLabel]
	_, annotated := namespace.Annotations[neteye.TenantDisplayNameAnnotation]
	if !labeled && !annotated {
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
			// Tenant components run no workloads yet, so each reports the empty
			// set of images.
			ResolvedImages: []neteye.NetEyeResolvedImage{},
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
func (r *NetEyeTenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	networkPolicy := &unstructured.Unstructured{}
	networkPolicy.SetGroupVersionKind(resources.CiliumNetworkPolicyGVK())
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEyeTenant{}, builder.WithPredicates(reconcileOnSpecOrDeletionChange)).
		Owns(networkPolicy).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(tenantForNamespace)).
		Complete(r)
}

// tenantForNamespace maps a namespace to the tenant it may contain, which has
// the same name.
func tenantForNamespace(_ context.Context, namespace client.Object) []reconcile.Request {
	if !strings.HasPrefix(namespace.GetName(), neteye.TenantNamePrefix) {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace.GetName(), Name: namespace.GetName()}}}
}
