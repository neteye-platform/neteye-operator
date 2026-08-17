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
	reconciliationRequeueAfter = 10 * time.Minute
	failureRequeueAfter        = 30 * time.Second
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

	components, ok := neteye.ComponentsForVersion(ne.Spec.Version)
	if !ok {
		message := fmt.Sprintf("unsupported NetEye version %q", ne.Spec.Version)
		log.Error(nil, "unsupported NetEye version", "version", ne.Spec.Version)
		ne.Status.Phase = "Failed"
		ne.Status.Message = message
		ne.Status.ServicesStatus.Identity = identityStatus("Failed", message, "")
		ne.Status.ObservedGeneration = ne.GetGeneration()
		_ = r.Status().Update(ctx, ne)
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	ns := ne.Namespace
	issuerRef := resources.CertificateIssuerRef{
		Name: ne.Spec.InternalCertificateIssuerRef,
	}
	keycloakComponent := r.KeycloakComponent
	if keycloakComponent == nil {
		return ctrl.Result{}, fmt.Errorf("keycloak component is not configured")
	}
	log.Info("Components loaded", "version", ne.Spec.Version)
	if err := resources.EnsureIssuerExists(ctx, r.Client, log, ns, issuerRef); err != nil {
		if apierrors.IsNotFound(err) {
			message := fmt.Sprintf("cert-manager Issuer %q was not found in namespace %q; create it before creating or reconciling this NetEye resource", issuerRef.Name, ns)
			ne.Status.Phase = "Failed"
			ne.Status.Message = message
			ne.Status.ServicesStatus.Identity = identityStatus("Failed", message, components.KeycloakImage)
			ne.Status.ObservedGeneration = ne.GetGeneration()
			_ = r.Status().Update(ctx, ne)
			log.Info("required cert-manager Issuer is missing", "namespace", ns, "issuer", issuerRef.Name, "requeueAfter", failureRequeueAfter)
			return ctrl.Result{RequeueAfter: failureRequeueAfter}, nil
		}
		return ctrl.Result{}, fmt.Errorf("ensure issuer exists: %w", err)
	}
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEye", ne)
	gateway := ne.Spec.Gateway
	if err := resources.EnsureGatewayTLSCertificate(ctx, r.Client, log, ns, gateway.TLSSecretName, issuerRef, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure gateway tls certificate: %w", err)
	}
	if err := resources.EnsureGateway(ctx, r.Client, log, ns, gateway.Name, gateway.ClassName, gateway.Annotations, gateway.TLSSecretName, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure gateway: %w", err)
	}
	if err := resources.EnsureHTTPToHTTPSRedirectRoute(ctx, r.Client, log, ns, gateway.Name, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure http to https redirect route: %w", err)
	}
	gatewayCertificateReady, gatewayCertificateMessage, err := resources.IsCertificateReady(ctx, r.Client, ns, gateway.TLSSecretName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check gateway tls certificate readiness: %w", err)
	}
	if !gatewayCertificateReady {
		ne.Status.Phase = "Pending"
		ne.Status.Message = gatewayCertificateMessage
		ne.Status.ServicesStatus.Identity = identityStatus("Pending", gatewayCertificateMessage, components.KeycloakImage)
		ne.Status.ObservedGeneration = ne.GetGeneration()
		_ = r.Status().Update(ctx, ne)
		log.V(1).Info("gateway tls certificate is not ready", "reason", gatewayCertificateMessage, "requeueAfter", reconciliationRequeueAfter)
		return ctrl.Result{RequeueAfter: reconciliationRequeueAfter}, nil
	}
	keycloakResourcesReady, keycloakResourcesMessage, err := keycloakComponent.EnsureResources(ctx, ns, components.KeycloakImage, ne.Spec.Identity, gateway.Name, issuerRef, owner)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile keycloak: %w", err)
	}
	if !keycloakResourcesReady {
		ne.Status.Phase = "Pending"
		ne.Status.Message = keycloakResourcesMessage
		ne.Status.ServicesStatus.Identity = identityStatus("Pending", keycloakResourcesMessage, components.KeycloakImage)
		ne.Status.ObservedGeneration = ne.GetGeneration()
		_ = r.Status().Update(ctx, ne)
		log.V(1).Info("identity resources are not ready", "reason", keycloakResourcesMessage, "requeueAfter", reconciliationRequeueAfter)
		return ctrl.Result{RequeueAfter: reconciliationRequeueAfter}, nil
	}
	keycloakReady, keycloakMessage, err := keycloakComponent.IsReady(ctx, ns)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check keycloak readiness: %w", err)
	}
	if !keycloakReady {
		ne.Status.Phase = "Pending"
		ne.Status.Message = keycloakMessage
		ne.Status.ServicesStatus.Identity = identityStatus("Pending", keycloakMessage, components.KeycloakImage)
		ne.Status.ObservedGeneration = ne.GetGeneration()
		_ = r.Status().Update(ctx, ne)
		log.V(1).Info("identity service is not ready", "reason", keycloakMessage, "requeueAfter", reconciliationRequeueAfter)
		return ctrl.Result{RequeueAfter: reconciliationRequeueAfter}, nil
	}

	ne.Status.Phase = "Ready"
	ne.Status.Message = ""
	ne.Status.ServicesStatus.Identity = identityStatus("Ready", "", components.KeycloakImage)
	ne.Status.ObservedGeneration = ne.GetGeneration()
	_ = r.Status().Update(ctx, ne)

	log.Info("NetEye is ready", "namespace", ns, "identityImage", components.KeycloakImage, "requeueAfter", reconciliationRequeueAfter)
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
