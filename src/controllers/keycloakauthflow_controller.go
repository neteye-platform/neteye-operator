// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

const KeycloakAuthFlowFinalizer = "neteye.cloud/keycloak-auth-flow"

type KeycloakAuthFlowReconciler struct{ KeycloakAPIReconciler }

// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakauthflows,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakauthflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakauthflows/finalizers,verbs=update
func (r *KeycloakAuthFlowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("keycloakauthflow", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)
	flow := &neteye.KeycloakAuthFlow{}
	if err := r.Get(ctx, req.NamespacedName, flow); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !flow.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, flow)
	}
	api, err := r.adminAPI(ctx, flow.Namespace) // nosemgrep: trailofbits.go.invalid-usage-of-modified-variable.invalid-usage-of-modified-variable
	if err != nil {
		if r.isMissingCredentials(err) {
			r.setStatus(ctx, req.NamespacedName, flow, neteye.ServiceStateFailed, missingCredentialsMessage(flow.Namespace))
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		r.setStatus(ctx, req.NamespacedName, flow, neteye.ServiceStateNotReady, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if controllerutil.AddFinalizer(flow, KeycloakAuthFlowFinalizer) {
		if err := r.Update(ctx, flow); err != nil {
			return ctrl.Result{}, fmt.Errorf("add keycloak authentication flow finalizer: %w", err)
		}
	}
	result, err := keycloak.ReconcileFlow(ctx, api, flow.Spec)
	if err != nil {
		log.Error(err, "unable to reconcile Keycloak authentication flow", "alias", flow.Spec.Alias)
		r.setStatus(ctx, req.NamespacedName, flow, neteye.ServiceStateFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if result.Created {
		log.Info("keycloak authentication flow created", "alias", flow.Spec.Alias, "realm", flow.Spec.Realm)
	} else if result.Updated {
		log.Info("keycloak authentication flow drift reconciled", "alias", flow.Spec.Alias, "realm", flow.Spec.Realm)
	}
	r.setStatus(ctx, req.NamespacedName, flow, neteye.ServiceStateReady, "Keycloak authentication flow is reconciled")
	return ctrl.Result{RequeueAfter: r.reconciliationRequeue()}, nil
}
func (r *KeycloakAuthFlowReconciler) reconcileDelete(ctx context.Context, flow *neteye.KeycloakAuthFlow) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(flow, KeycloakAuthFlowFinalizer) {
		return ctrl.Result{}, nil
	}
	if flow.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		api, err := r.adminAPI(ctx, flow.Namespace)
		if err != nil {
			if r.isMissingCredentials(err) {
				r.Log.Info("leaving Keycloak authentication flow behind because local admin credentials are unavailable", "severity", "warning", "object", client.ObjectKeyFromObject(flow))
				return r.removeFinalizer(ctx, flow)
			}
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		if err := keycloak.DeleteFlow(ctx, api, flow.Spec); err != nil {
			var permanent *keycloak.PermanentDeleteError
			if !errors.As(err, &permanent) {
				return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
			}
			r.Log.Error(err, "keycloak authentication flow cannot be deleted, releasing finalizer", "alias", flow.Spec.Alias)
		}
	}
	return r.removeFinalizer(ctx, flow)
}

func (r *KeycloakAuthFlowReconciler) removeFinalizer(ctx context.Context, flow *neteye.KeycloakAuthFlow) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(flow, KeycloakAuthFlowFinalizer)
	if err := r.Update(ctx, flow); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove keycloak authentication flow finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}
func (r *KeycloakAuthFlowReconciler) setStatus(ctx context.Context, key client.ObjectKey, flow *neteye.KeycloakAuthFlow, state neteye.ServiceState, message string) {
	status := neteye.KeycloakAuthFlowStatus{Status: state, Message: message, ObservedGeneration: flow.Generation}
	writeStatus(ctx, r.Client, key, func() *neteye.KeycloakAuthFlow { return &neteye.KeycloakAuthFlow{} }, func(current *neteye.KeycloakAuthFlow) { current.Status = status })
}
func (r *KeycloakAuthFlowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&neteye.KeycloakAuthFlow{}, builder.WithPredicates(reconcileOnSpecOrDeletionChange)).Complete(r)
}
