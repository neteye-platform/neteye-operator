// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// tenantNamespaceComponent is the status key of the tenant namespace: its
	// tenant metadata and its network baseline.
	tenantNamespaceComponent = "namespace"
)

// NetEyeTenantReconciler reconciles NetEyeTenant resources: the tenant
// metadata shown on the tenant namespace and the default-deny network baseline
// of that namespace.
type NetEyeTenantReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// OperatorUsername is the Kubernetes username of the operator, for example
	// system:serviceaccount:neteye-system:neteye-operator. The admission policy
	// that keeps the tenant metadata of a namespace read-only lets only this
	// user change it. When empty, the policy is not managed.
	OperatorUsername string

	// Requeue intervals. When zero, the matching Default*RequeueAfter is used.
	FailureRequeueAfter        time.Duration
	ReconciliationRequeueAfter time.Duration
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants,verbs=get;list;watch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyetenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingadmissionpolicies;validatingadmissionpolicybindings,verbs=get;list;watch;create;update

// Reconcile reconciles a NetEyeTenant resource with the desired cluster state.
func (r *NetEyeTenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("neteyetenant", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)

	tenant := &neteye.NetEyeTenant{}
	if err := r.Get(ctx, req.NamespacedName, tenant); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// Everything the tenant creates lives in its namespace and has the tenant as
	// owner, so garbage collection removes it; a deleted tenant needs no
	// finalizer.
	if !tenant.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	status := neteye.NetEyeTenantStatus{
		ObservedGeneration: tenant.Generation,
		Components:         map[string]neteye.TenantComponentStatus{},
	}
	defer r.updateStatus(ctx, req.NamespacedName, tenant.Generation, &status)

	if err := r.ensureNamespacePolicy(ctx); err != nil {
		// The policy protects the namespace metadata of every tenant, but its
		// absence does not keep a tenant from working, so it is retried without
		// failing the tenant.
		log.Error(err, "unable to ensure the tenant namespace admission policy", "requeueAfter", r.failureRequeue())
	}

	namespace, requeueAfter := r.reconcileNamespace(ctx, tenant)
	status.Components[tenantNamespaceComponent] = namespace
	if requeueAfter == 0 {
		requeueAfter = r.reconciliationRequeue()
	}
	if namespace.Status == neteye.TenantComponentStateReady {
		log.V(1).Info("NetEyeTenant is ready", "requeueAfter", requeueAfter)
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileNamespace marks the namespace as the namespace of the tenant and
// closes it with a default-deny baseline for ingress and egress. Components
// that the tenant adds later open only the traffic they need.
func (r *NetEyeTenantReconciler) reconcileNamespace(ctx context.Context, tenant *neteye.NetEyeTenant) (neteye.TenantComponentStatus, time.Duration) {
	log := ctrl.LoggerFrom(ctx)
	if err := r.ensureNamespaceMetadata(ctx, tenant); err != nil {
		log.Error(err, "unable to update the tenant metadata of the namespace", "requeueAfter", r.failureRequeue())
		return componentStatus(tenant, neteye.TenantComponentStateDegraded, "NamespaceMetadataFailed", err.Error()), r.failureRequeue()
	}
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	if err := resources.EnsureDefaultDenyNetworkPolicy(ctx, r.Client, tenant.Namespace, owner); err != nil {
		log.Error(err, "unable to ensure the default-deny network policy of the tenant namespace", "requeueAfter", r.failureRequeue())
		return componentStatus(tenant, neteye.TenantComponentStateDegraded, "NetworkPolicyFailed", err.Error()), r.failureRequeue()
	}
	return componentStatus(tenant, neteye.TenantComponentStateReady, "Available", "Tenant namespace is ready"), 0
}

// ensureNamespaceMetadata marks the namespace as the namespace of the tenant
// and shows the tenant display name on it.
func (r *NetEyeTenantReconciler) ensureNamespaceMetadata(ctx context.Context, tenant *neteye.NetEyeTenant) error {
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: tenant.Namespace}, namespace); err != nil {
		return fmt.Errorf("get namespace %q: %w", tenant.Namespace, err)
	}
	if namespace.Labels[neteye.TenantLabel] == tenant.Name && namespace.Annotations[neteye.TenantDisplayNameAnnotation] == tenant.Spec.Name {
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
	namespace.Annotations[neteye.TenantDisplayNameAnnotation] = tenant.Spec.Name
	if err := r.Patch(ctx, namespace, patch); err != nil {
		return fmt.Errorf("update the tenant metadata of namespace %q: %w", tenant.Namespace, err)
	}
	return nil
}

func componentStatus(tenant *neteye.NetEyeTenant, state neteye.TenantComponentState, reason, message string) neteye.TenantComponentStatus {
	return neteye.TenantComponentStatus{Status: state, ObservedGeneration: tenant.Generation, Reason: reason, Message: message, ResolvedImages: []neteye.TenantResolvedImage{}}
}

// updateStatus derives the Ready condition from the components and writes the
// status to a fresh copy of the resource.
func (r *NetEyeTenantReconciler) updateStatus(ctx context.Context, key client.ObjectKey, generation int64, status *neteye.NetEyeTenantStatus) {
	ready := metav1.Condition{Type: neteye.TenantConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: generation, Reason: neteye.TenantReasonAllComponentsReady, Message: "All components are ready"}
	for name, component := range status.Components {
		switch component.Status {
		case neteye.TenantComponentStateReady:
		case neteye.TenantComponentStateDegraded:
			ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, neteye.TenantReasonComponentsDegraded, fmt.Sprintf("Component %q is degraded: %s", name, component.Message)
		default:
			if ready.Reason != neteye.TenantReasonComponentsDegraded {
				ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, neteye.TenantReasonComponentsNotReady, fmt.Sprintf("Waiting for component %q: %s", name, component.Message)
			}
		}
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &neteye.NetEyeTenant{}
		if err := r.Get(ctx, key, current); err != nil {
			return client.IgnoreNotFound(err)
		}
		next := *status
		next.Conditions = append([]metav1.Condition(nil), current.Status.Conditions...)
		meta.SetStatusCondition(&next.Conditions, ready)
		current.Status = next
		return r.Status().Update(ctx, current)
	})
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to update NetEyeTenant status")
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

// SetupWithManager registers the controller.
func (r *NetEyeTenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEyeTenant{}, builder.WithPredicates(reconcileOnSpecOrDeletionChange)).
		Complete(r)
}
