// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package controllers contains the Kubernetes controllers for the NetEye operator's NetEye CR.
package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// Scheme is shared by the manager and tests.
var Scheme = runtime.NewScheme()

// Default requeue intervals. main.go seeds the reconciler fields from these
// (overridable via env vars); tests rely on them directly.
const (
	DefaultWaitForProgressingRequeueAfter = 30 * time.Second
	DefaultFailureRequeueAfter            = 2 * time.Minute
	DefaultReconciliationRequeueAfter     = 10 * time.Minute
	clusterAuthorityLeaseName             = "neteye-cluster-authority"
)

// NetEyeReconciler reconciles NetEye CRs and drives per-CR component deployment.
type NetEyeReconciler struct {
	client.Client
	Log                    logr.Logger
	Scheme                 *runtime.Scheme
	KeycloakComponent      *keycloak.Component
	ElasticStackReconciler *elasticstack.Reconciler

	// Requeue intervals. When zero, the matching Default*RequeueAfter is used.
	WaitForProgressingRequeueAfter time.Duration
	FailureRequeueAfter            time.Duration
	ReconciliationRequeueAfter     time.Duration
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes/finalizers,verbs=update
// +kubebuilder:rbac:groups=cert-manager.io,resources=issuers,verbs=get;list;watch
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;grpcroutes,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=configmaps;services,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=k8s.keycloak.org,resources=keycloaks,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=olm.operatorframework.io,resources=clusterextensions,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;delete

// Reconcile reconciles a NetEye resource with the desired cluster state.
func (r *NetEyeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("neteye", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)

	log.Info("Started reconciling NetEye", "namespace", req.Namespace, "name", req.Name)

	ne := &neteye.NetEye{}
	if err := r.Get(ctx, req.NamespacedName, ne); err != nil {
		log.Error(err, "unable to fetch NetEye")
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	ne.Status.ObservedGeneration = ne.GetGeneration()
	// Reset the phase so severity comparisons in setPhase are scoped to this
	// reconcile only; otherwise a stale Failed phase from a previous
	// reconcile would block this pass from ever reporting Ready again.
	ne.Status.Phase = ""
	ne.Status.Message = ""
	ne.Status.ServicesStatus = neteye.NetEyeServicesStatus{
		Identity:     identityStatus(neteye.ServiceStateUnknown, "", ""),
		ElasticStack: &neteye.NetEyeElasticStackStatus{Status: neteye.ServiceStateUnknown, OTelCollector: &neteye.NetEyeServiceStatus{Status: neteye.ServiceStateUnknown}},
	}
	defer func() {
		if err := r.updateStatus(ctx, req.NamespacedName, ne.Status); err != nil {
			log.Error(err, "unable to update NetEye status")
		}
	}()

	if !neteye.IsLatestVersion(ne.Spec.Version) && neteye.IsPreviousVersion(ne.Spec.Version) {
		log.Info("NetEye version is not the latest", "currentVersion", ne.Spec.Version, "latestVersion", neteye.CurrentNetEyeVersion, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhasePendingUpgrades, fmt.Sprintf("NetEye version '%s' is not the latest; consider upgrading to '%s'. Reconciliation will be paused until the upgrade is performed.", ne.Spec.Version, neteye.CurrentNetEyeVersion))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	} else if !neteye.IsLatestVersion(ne.Spec.Version) {
		log.Error(nil, "NetEye version mismatch detected", "currentVersion", ne.Spec.Version, "latestVersion", neteye.CurrentNetEyeVersion, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("NetEye version '%s' is not the latest. Latest version is '%s'. Reconciliation will be paused until the mismatch has been resolved.", ne.Spec.Version, neteye.CurrentNetEyeVersion))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	components, ok := neteye.ComponentsForVersion(ne.Spec.Version)
	if !ok {
		log.Error(nil, "unsupported NetEye version", "version", ne.Spec.Version, "supportedVersions", neteye.SupportedVersions(), "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("unsupported NetEye version '%s'; supported versions are: %v", ne.Spec.Version, neteye.SupportedVersions()))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	if r.KeycloakComponent == nil {
		log.Error(nil, "keycloak component is not initialized", "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "keycloak component is not initialized")
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("keycloak component is not initialized")
	}
	log.V(1).Info("Components loaded", "version", ne.Spec.Version)

	if result, err := r.reconcileBaseResources(ctx, ne); shouldReturn(result, err) {
		return result, err
	}
	if err := resources.EnsureDefaultDenyNetworkPolicy(ctx, r.Client, keycloak.WorkloadNamespace, ownerReferenceFor(ne)); err != nil {
		log.Error(err, "failed to ensure shared default-deny network policy", "namespace", keycloak.WorkloadNamespace, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "Check services status for details")
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure shared default-deny network policy: %w", err)
	}

	keycloakResult, keycloakErr := r.reconcileKeycloak(ctx, ne, components.KeycloakImage)
	elasticResult, elasticErr := r.reconcileElasticStack(ctx, ne, components.OTelCollectorImage)
	combinedResult := combineResults(keycloakResult, elasticResult)

	if keycloakErr != nil {
		return combinedResult, keycloakErr
	}
	if elasticErr != nil {
		return combinedResult, elasticErr
	}
	if !combinedResult.IsZero() {
		return combinedResult, nil
	}

	setPhase(ne, neteye.PhaseReady, "All components are ready")
	ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateReady, "Identity service is ready", components.KeycloakImage)

	log.Info("NetEye is ready", "namespace", ne.Namespace, "name", ne.Name, "requeueAfter", r.reconciliationRequeue())
	return ctrl.Result{RequeueAfter: r.reconciliationRequeue()}, nil
}

func (r *NetEyeReconciler) reconcileElasticStack(ctx context.Context, ne *neteye.NetEye, collectorImage string) (ctrl.Result, error) {
	if r.ElasticStackReconciler == nil {
		r.ElasticStackReconciler = elasticstack.NewReconciler(nil)
	}
	outcome := r.ElasticStackReconciler.Reconcile(ctx, elasticstack.Request{
		Namespace: keycloak.WorkloadNamespace, Config: ne.Spec.ElasticStack, IdentityHostname: ne.Spec.Identity.Hostname, CollectorImage: collectorImage,
		GatewayNamespace: keycloak.WorkloadNamespace, GatewayName: ne.Spec.Gateway.Name, IssuerRef: issuerRefFor(ne), Owner: ownerReferenceFor(ne),
	})
	ne.Status.ServicesStatus.ElasticStack = &neteye.NetEyeElasticStackStatus{Status: outcome.Module.Status, Message: outcome.Module.Message, OTelCollector: outcome.Collector}
	if outcome.Phase != "" {
		setPhase(ne, outcome.Phase, outcome.PhaseMessage)
	}
	return r.resultForRequeue(outcome.Requeue), outcome.Err
}

func (r *NetEyeReconciler) resultForRequeue(reason elasticstack.RequeueReason) ctrl.Result {
	switch reason {
	case elasticstack.RequeueProgressing:
		return ctrl.Result{RequeueAfter: r.waitForProgressingRequeue()}
	case elasticstack.RequeueFailure:
		return ctrl.Result{RequeueAfter: r.failureRequeue()}
	default:
		return ctrl.Result{}
	}
}

func (r *NetEyeReconciler) updateStatus(ctx context.Context, key client.ObjectKey, status neteye.NetEyeStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &neteye.NetEye{}
		if err := r.Get(ctx, key, current); err != nil {
			return client.IgnoreNotFound(err)
		}
		current.Status = status
		return r.Status().Update(ctx, current)
	})
}

func shouldReturn(result ctrl.Result, err error) bool {
	return err != nil || !result.IsZero()
}

// combineResults returns a requeue request that honors both reconciliation outcomes.
// When both request a delayed requeue, the earliest one takes precedence.
func combineResults(a, b ctrl.Result) ctrl.Result {
	var result ctrl.Result

	switch {
	case a.RequeueAfter > 0 && b.RequeueAfter > 0:
		if a.RequeueAfter < b.RequeueAfter {
			result.RequeueAfter = a.RequeueAfter
		} else {
			result.RequeueAfter = b.RequeueAfter
		}
	case a.RequeueAfter > 0:
		result.RequeueAfter = a.RequeueAfter
	case b.RequeueAfter > 0:
		result.RequeueAfter = b.RequeueAfter
	}

	return result
}

func (r *NetEyeReconciler) waitForProgressingRequeue() time.Duration {
	if r.WaitForProgressingRequeueAfter > 0 {
		return r.WaitForProgressingRequeueAfter
	}
	return DefaultWaitForProgressingRequeueAfter
}

func (r *NetEyeReconciler) failureRequeue() time.Duration {
	if r.FailureRequeueAfter > 0 {
		return r.FailureRequeueAfter
	}
	return DefaultFailureRequeueAfter
}

func (r *NetEyeReconciler) reconciliationRequeue() time.Duration {
	if r.ReconciliationRequeueAfter > 0 {
		return r.ReconciliationRequeueAfter
	}
	return DefaultReconciliationRequeueAfter
}

func (r *NetEyeReconciler) reconcileBaseResources(ctx context.Context, ne *neteye.NetEye) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	gateway := ne.Spec.Gateway
	issuerRef := issuerRefFor(ne)
	owner := ownerReferenceFor(ne)
	if err := resources.EnsureIssuerExists(ctx, r.Client, keycloak.WorkloadNamespace, issuerRef); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("required cert-manager Issuer is missing", "namespace", keycloak.WorkloadNamespace, "issuer", issuerRef.Name, "requeueAfter", r.failureRequeue())
			setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("cert-manager Issuer '%q' was not found in namespace %q; create it before creating or reconciling this NetEye resource", issuerRef.Name, keycloak.WorkloadNamespace))
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		log.Error(err, "failed to ensure cert-manager Issuer exists", "namespace", keycloak.WorkloadNamespace, "issuer", issuerRef.Name, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("failed to ensure cert-manager Issuer '%q' exists in namespace %q: %v", issuerRef.Name, keycloak.WorkloadNamespace, err))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure issuer exists: %w", err)
	}
	if err := resources.EnsureGateway(ctx, r.Client, keycloak.WorkloadNamespace, gateway.Name, gateway.ClassName, gateway.Annotations, gatewayListeners(ne), owner); err != nil {
		log.Error(err, "failed to ensure gateway exists", "namespace", keycloak.WorkloadNamespace, "gatewayName", gateway.Name, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("failed to ensure gateway '%q' exists in namespace %q: %v", gateway.Name, keycloak.WorkloadNamespace, err))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure gateway: %w", err)
	}
	if err := resources.EnsureHTTPToHTTPSRedirectRoute(ctx, r.Client, keycloak.WorkloadNamespace, gateway.Name, owner); err != nil {
		log.Error(err, "failed to ensure HTTP to HTTPS redirect route exists", "namespace", keycloak.WorkloadNamespace, "gatewayName", gateway.Name, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("failed to ensure HTTP to HTTPS redirect route exists for gateway '%q' in namespace %q: %v", gateway.Name, keycloak.WorkloadNamespace, err))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure HTTP to HTTPS redirect route: %w", err)
	}
	return ctrl.Result{}, nil
}

// gatewayListeners returns the per-component HTTPS listeners exposed on the
// shared Gateway. The identity listener is always present; the Elastic Stack
// listeners are only added when the feature module is enabled.
func gatewayListeners(ne *neteye.NetEye) []resources.GatewayListener {
	listeners := []resources.GatewayListener{
		{Name: keycloak.GatewayListenerName, Hostname: keycloak.RouteHostname, TLSSecretName: keycloak.TLSSecretName, RouteKind: resources.RouteKindHTTP},
	}
	if ne.Spec.ElasticStack != nil && ne.Spec.ElasticStack.Enabled {
		listeners = append(listeners,
			resources.GatewayListener{Name: elasticstack.GRPCListenerName, Hostname: elasticstack.GRPCRouteHostname, TLSSecretName: elasticstack.GRPCTLSSecretName, RouteKind: resources.RouteKindGRPC},
			resources.GatewayListener{Name: elasticstack.CrossTenantListenerName, Hostname: elasticstack.CrossTenantRouteHostname, TLSSecretName: elasticstack.CrossTenantTLSSecretName, RouteKind: resources.RouteKindHTTP},
		)
	}
	return listeners
}

func (r *NetEyeReconciler) reconcileKeycloak(ctx context.Context, ne *neteye.NetEye, image string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	owner := ownerReferenceFor(ne)
	log.Info("Started Keycloak reconciliation", "namespace", ne.Namespace, "name", owner.Name)
	if err := r.ensureClusterAuthority(ctx, ne); err != nil {
		setPhase(ne, neteye.PhaseFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	issuerRef := issuerRefFor(ne)
	if err := resources.EnsureIssuerExists(ctx, r.Client, keycloak.WorkloadNamespace, issuerRef); err != nil {
		if apierrors.IsNotFound(err) {
			setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("cert-manager Issuer '%q' was not found in namespace %q; create it before creating or reconciling this NetEye resource", issuerRef.Name, keycloak.WorkloadNamespace))
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("failed to ensure cert-manager Issuer '%q' exists in namespace %q: %v", issuerRef.Name, keycloak.WorkloadNamespace, err))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure shared Keycloak issuer exists: %w", err)
	}
	keycloakResourcesReady, keycloakResourcesMessage, err := r.KeycloakComponent.EnsureResources(ctx, keycloak.WorkloadNamespace, image, ne.Spec.Identity, keycloak.WorkloadNamespace, ne.Spec.Gateway.Name, issuerRef, owner)
	if err != nil {
		log.Error(err, "failed to ensure keycloak resources", "namespace", keycloak.WorkloadNamespace, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateFailed, fmt.Sprintf("failed to ensure keycloak resources in namespace %q: %v", keycloak.WorkloadNamespace, err), image)
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure keycloak resources: %w", err)
	}
	if !keycloakResourcesReady {
		log.V(1).Info("identity resources are not ready", "reason", keycloakResourcesMessage, "requeueAfter", r.waitForProgressingRequeue())
		setPhase(ne, neteye.PhaseNotReady, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateNotReady, keycloakResourcesMessage, image)
		return ctrl.Result{RequeueAfter: r.waitForProgressingRequeue()}, nil
	}
	keycloakReady, keycloakMessage, err := r.KeycloakComponent.IsReady(ctx, keycloak.WorkloadNamespace)
	if err != nil {
		log.Error(err, "failed to check keycloak readiness", "namespace", ne.Namespace, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateFailed, fmt.Sprintf("failed to check keycloak readiness: %v", err), image)
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("check keycloak readiness: %w", err)
	}
	if !keycloakReady {
		log.V(1).Info("identity service is not ready", "reason", keycloakMessage, "requeueAfter", r.waitForProgressingRequeue())
		setPhase(ne, neteye.PhaseNotReady, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateNotReady, keycloakMessage, image)
		return ctrl.Result{RequeueAfter: r.waitForProgressingRequeue()}, nil
	}

	// The instance is up, so the Admin API is reachable and the KeycloakUser
	// controller can make progress on the administrative account the platform
	// owns. Declaring it here is what replaces the Ansible role creating it with
	// the bootstrap admin.
	if err := r.KeycloakComponent.EnsureInternalAdminUser(ctx, keycloak.WorkloadNamespace); err != nil {
		log.Error(err, "failed to declare the Keycloak internal admin user", "namespace", keycloak.WorkloadNamespace, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateFailed, fmt.Sprintf("failed to declare the Keycloak internal admin user: %v", err), image)
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure keycloak internal admin user: %w", err)
	}

	// The internal admin is usable now too, so the root account IcingaWeb2
	// authenticates as can be declared the same way, replacing the Ansible
	// "Create root user" task.
	if err := r.KeycloakComponent.EnsureRootUser(ctx, keycloak.WorkloadNamespace); err != nil {
		log.Error(err, "failed to declare the Keycloak root user", "namespace", keycloak.WorkloadNamespace, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateFailed, fmt.Sprintf("failed to declare the Keycloak root user: %v", err), image)
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure keycloak root user: %w", err)
	}

	if err := r.KeycloakComponent.EnsureNetEyeClient(ctx, keycloak.WorkloadNamespace); err != nil {
		log.Error(err, "failed to declare the NetEye Keycloak client", "namespace", keycloak.WorkloadNamespace, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, "Check services status for details")
		ne.Status.ServicesStatus.Identity = identityStatus(neteye.ServiceStateFailed, fmt.Sprintf("failed to declare the NetEye Keycloak client: %v", err), image)
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, fmt.Errorf("ensure neteye keycloak client: %w", err)
	}

	// Once the internal admin is usable the bootstrap account has served its
	// purpose, exactly as in the Ansible role. This is a no-op until then.
	if err := r.KeycloakComponent.EnsureBootstrapAdminDisabled(ctx, keycloak.WorkloadNamespace); err != nil {
		// Not fatal: the platform works with the bootstrap account still enabled,
		// so this is reported and retried promptly rather than failing the
		// reconciliation.
		log.Error(err, "failed to disable the Keycloak bootstrap admin", "namespace", keycloak.WorkloadNamespace, "requeueAfter", r.failureRequeue())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	log.Info("Keycloak reconciled and ready", "namespace", ne.Namespace, "name", owner.Name)
	return ctrl.Result{}, nil
}

func (r *NetEyeReconciler) ensureClusterAuthority(ctx context.Context, ne *neteye.NetEye) error {
	key := client.ObjectKey{Namespace: keycloak.WorkloadNamespace, Name: clusterAuthorityLeaseName}
	lease := &coordinationv1.Lease{}
	if err := r.Get(ctx, key, lease); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get NetEye cluster authority lease: %w", err)
		}
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       keycloak.WorkloadNamespace,
				Name:            clusterAuthorityLeaseName,
				OwnerReferences: []metav1.OwnerReference{ownerReferenceFor(ne)},
			},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(string(ne.UID))},
		}
		createErr := r.Create(ctx, lease)
		if createErr != nil && !apierrors.IsAlreadyExists(createErr) {
			return fmt.Errorf("create NetEye cluster authority lease: %w", createErr)
		}
		if apierrors.IsAlreadyExists(createErr) {
			if err := r.Get(ctx, key, lease); err != nil {
				return fmt.Errorf("get NetEye cluster authority lease after create race: %w", err)
			}
		}
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != string(ne.UID) {
		return fmt.Errorf("shared NetEye platform components are managed by another NetEye resource")
	}
	if owner := metav1.GetControllerOf(lease); owner == nil || owner.UID != ne.UID {
		if err := controllerutil.SetControllerReference(ne, lease, r.Scheme); err != nil {
			return fmt.Errorf("set NetEye owner reference on cluster authority lease: %w", err)
		}
		if err := r.Update(ctx, lease); err != nil {
			return fmt.Errorf("update cluster authority lease owner reference: %w", err)
		}
	}
	return nil
}

func issuerRefFor(ne *neteye.NetEye) resources.CertificateIssuerRef {
	return resources.CertificateIssuerRef{Name: ne.Spec.InternalCertificateIssuerRef}
}

func ownerReferenceFor(ne *neteye.NetEye) metav1.OwnerReference {
	return resources.OwnerReference(neteye.GroupVersion.String(), "NetEye", ne)
}

func setPhase(ne *neteye.NetEye, phase neteye.NetEyePhase, message string) {
	if phaseSeverity(phase) < phaseSeverity(ne.Status.Phase) {
		// A less severe phase must not clobber a more severe one already
		// recorded during this reconcile (e.g. ElasticStack reporting
		// NotReady after Keycloak already reported Failed).
		return
	}
	ne.Status.Phase = phase
	ne.Status.Message = message
}

// phaseSeverity ranks NetEyePhase values so a later, less severe phase update
// cannot silently overwrite an earlier, more severe one within the same
// reconcile. Higher is more severe.
func phaseSeverity(phase neteye.NetEyePhase) int {
	switch phase {
	case neteye.PhaseFailed:
		return 3
	case neteye.PhaseNotReady, neteye.PhasePendingUpgrades:
		return 2
	case neteye.PhaseReady:
		return 1
	default:
		return 0
	}
}

func identityStatus(state neteye.ServiceState, message, image string) *neteye.NetEyeServiceStatus {
	return &neteye.NetEyeServiceStatus{
		Status:        state,
		Message:       message,
		ResolvedImage: image,
	}
}

func (r *NetEyeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEye{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
