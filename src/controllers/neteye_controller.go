/*
Copyright 2026 Wuerth IT | Italy.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// Scheme is shared by the manager and tests.
var Scheme = runtime.NewScheme()

const (
	waitForProgressingRequeueAfter = 30 * time.Second
	failureRequeueAfter            = 2 * time.Minute
	reconciliationRequeueAfter     = 10 * time.Minute
)

// NetEyeReconciler reconciles NetEye CRs and drives per-CR component deployment.
type NetEyeReconciler struct {
	client.Client
	Log               logr.Logger
	Scheme            *runtime.Scheme
	KeycloakComponent *keycloak.Component
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=neteyes/finalizers,verbs=update
// +kubebuilder:rbac:groups=cert-manager.io,resources=issuers,verbs=get;list;watch
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;httproutes,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=k8s.keycloak.org,resources=keycloaks,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=olm.operatorframework.io,resources=clusterextensions,verbs=get;list;watch;create;update
func (r *NetEyeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("neteye", req.NamespacedName)

	log.Info("Reconciling NetEye", "namespace", req.Namespace, "name", req.Name)

	ne := &neteye.NetEye{}
	if err := r.Get(ctx, req.NamespacedName, ne); err != nil {
		log.Error(err, "unable to fetch NetEye")
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	ne.Status.ObservedGeneration = ne.GetGeneration()
	servicesStatus := neteye.NetEyeServicesStatus{
		Identity: identityStatus("Unknown", "", ""),
	}

	components, ok := neteye.ComponentsForVersion(ne.Spec.Version)
	if !ok {
		message := fmt.Sprintf("unsupported NetEye version '%s'; supported versions are: %v", ne.Spec.Version, neteye.SupportedVersions())
		log.Error(nil, "unsupported NetEye version", "version", ne.Spec.Version, "supportedVersions", neteye.SupportedVersions(), "requeueAfter", failureRequeueAfter)
		ne.Status.Phase = "Failed"
		ne.Status.Message = message
		ne.Status.ServicesStatus = servicesStatus
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, nil
	}

	ns := ne.Namespace
	issuerRef := resources.CertificateIssuerRef{
		Name: ne.Spec.InternalCertificateIssuerRef,
	}
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEye", ne)
	gateway := ne.Spec.Gateway

	keycloakComponent := r.KeycloakComponent
	if keycloakComponent == nil {
		log.Error(nil, "keycloak component is not initialized", "requeueAfter", failureRequeueAfter)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("keycloak component is not initialized")
	}
	log.Info("Components loaded", "version", ne.Spec.Version)

	if err := resources.EnsureIssuerExists(ctx, r.Client, log, ns, issuerRef); err != nil {
		ne.Status.Phase = "Failed"
		if apierrors.IsNotFound(err) {
			log.V(1).Info("required cert-manager Issuer is missing", "namespace", ns, "issuer", issuerRef.Name, "requeueAfter", failureRequeueAfter)
			message := fmt.Sprintf("cert-manager Issuer '%q' was not found in namespace %q; create it before creating or reconciling this NetEye resource", issuerRef.Name, ns)
			ne.Status.Message = message
			_ = r.Status().Update(ctx, ne)
			return ctrl.Result{RequeueAfter: failureRequeueAfter}, nil
		}
		log.Error(err, "failed to ensure cert-manager Issuer exists", "namespace", ns, "issuer", issuerRef.Name, "requeueAfter", failureRequeueAfter)
		message := fmt.Sprintf("failed to ensure cert-manager Issuer '%q' exists in namespace %q: %v", issuerRef.Name, ns, err)
		ne.Status.Message = message
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("ensure issuer exists: %w", err)
	}
	if err := resources.EnsureGatewayTLSCertificate(ctx, r.Client, log, ns, gateway.TLSSecretName, issuerRef, owner); err != nil {
		log.Error(err, "failed to ensure gateway TLS certificate exists", "namespace", ns, "tlsSecretName", gateway.TLSSecretName, "requeueAfter", failureRequeueAfter)
		message := fmt.Sprintf("failed to ensure gateway TLS certificate '%q' exists in namespace %q: %v", gateway.TLSSecretName, ns, err)
		ne.Status.Phase = "Failed"
		ne.Status.Message = message
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("ensure gateway tls certificate: %w", err)
	}
	if err := resources.EnsureGateway(ctx, r.Client, log, ns, gateway.Name, gateway.ClassName, gateway.Annotations, gateway.TLSSecretName, owner); err != nil {
		log.Error(err, "failed to ensure gateway exists", "namespace", ns, "gatewayName", gateway.Name, "requeueAfter", failureRequeueAfter)
		message := fmt.Sprintf("failed to ensure gateway '%q' exists in namespace %q: %v", gateway.Name, ns, err)
		ne.Status.Phase = "Failed"
		ne.Status.Message = message
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("ensure gateway: %w", err)
	}
	if err := resources.EnsureHTTPToHTTPSRedirectRoute(ctx, r.Client, log, ns, gateway.Name, owner); err != nil {
		log.Error(err, "failed to ensure HTTP to HTTPS redirect route exists", "namespace", ns, "gatewayName", gateway.Name, "requeueAfter", failureRequeueAfter)
		message := fmt.Sprintf("failed to ensure HTTP to HTTPS redirect route exists for gateway '%q' in namespace %q: %v", gateway.Name, ns, err)
		ne.Status.Phase = "Failed"
		ne.Status.Message = message
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("ensure HTTP to HTTPS redirect route: %w", err)
	}
	gatewayCertificateReady, gatewayCertificateMessage, err := resources.IsCertificateReady(ctx, r.Client, ns, gateway.TLSSecretName)
	if err != nil {
		log.Error(err, "failed to check gateway TLS certificate readiness", "namespace", ns, "tlsSecretName", gateway.TLSSecretName, "requeueAfter", failureRequeueAfter)
		message := fmt.Sprintf("failed to check gateway TLS certificate readiness for secret '%q' in namespace %q: %v", gateway.TLSSecretName, ns, err)
		ne.Status.Phase = "Failed"
		ne.Status.Message = message
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("check gateway tls certificate readiness: %w", err)
	}
	if !gatewayCertificateReady {
		ne.Status.Phase = "NotReady"
		ne.Status.Message = gatewayCertificateMessage
		_ = r.Status().Update(ctx, ne)
		log.V(1).Info("gateway tls certificate is not ready", "reason", gatewayCertificateMessage, "requeueAfter", waitForProgressingRequeueAfter)
		return ctrl.Result{RequeueAfter: waitForProgressingRequeueAfter}, nil
	}

	keycloakResourcesReady, keycloakResourcesMessage, err := keycloakComponent.EnsureResources(ctx, ns, components.KeycloakImage, ne.Spec.Identity, gateway.Name, issuerRef, owner)
	if err != nil {
		log.Error(err, "failed to ensure keycloak resources", "namespace", ns, "requeueAfter", failureRequeueAfter)
		message := fmt.Sprintf("failed to ensure keycloak resources in namespace %q: %v", ns, err)
		ne.Status.Phase = "Failed"
		ne.Status.Message = "Check services status for details"
		ne.Status.ServicesStatus.Identity = identityStatus("Failed", message, components.KeycloakImage)
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("ensure keycloak resources: %w", err)
	}
	if !keycloakResourcesReady {
		log.V(1).Info("identity resources are not ready", "reason", keycloakResourcesMessage, "requeueAfter", waitForProgressingRequeueAfter)
		ne.Status.Phase = "NotReady"
		ne.Status.Message = "Check services status for details"
		ne.Status.ServicesStatus.Identity = identityStatus("NotReady", keycloakResourcesMessage, components.KeycloakImage)
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: waitForProgressingRequeueAfter}, nil
	}
	keycloakReady, keycloakMessage, err := keycloakComponent.IsReady(ctx, ns)
	if err != nil {
		log.Error(err, "failed to check keycloak readiness", "namespace", ns, "requeueAfter", failureRequeueAfter)
		ne.Status.Phase = "Failed"
		ne.Status.Message = "Check services status for details"
		ne.Status.ServicesStatus.Identity = identityStatus("Failed", fmt.Sprintf("failed to check keycloak readiness: %v", err), components.KeycloakImage)
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: failureRequeueAfter}, fmt.Errorf("check keycloak readiness: %w", err)
	}
	if !keycloakReady {
		log.V(1).Info("identity service is not ready", "reason", keycloakMessage, "requeueAfter", waitForProgressingRequeueAfter)
		ne.Status.Phase = "NotReady"
		ne.Status.Message = "Check services status for details"
		ne.Status.ServicesStatus.Identity = identityStatus("NotReady", keycloakMessage, components.KeycloakImage)
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{RequeueAfter: waitForProgressingRequeueAfter}, nil
	}

	ne.Status.Phase = "Ready"
	ne.Status.Message = "All components are ready"
	ne.Status.ServicesStatus.Identity = identityStatus("Ready", "Identity service is ready", components.KeycloakImage)
	_ = r.Status().Update(ctx, ne)

	log.Info("NetEye is ready", "namespace", ns, "requeueAfter", reconciliationRequeueAfter)
	return ctrl.Result{RequeueAfter: reconciliationRequeueAfter}, nil
}

func identityStatus(status, message, image string) *neteye.NetEyeServiceStatus {
	return &neteye.NetEyeServiceStatus{
		Status:        status,
		Message:       message,
		ResolvedImage: image,
	}
}

func (r *NetEyeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&neteye.NetEye{}).
		Complete(r)
}
