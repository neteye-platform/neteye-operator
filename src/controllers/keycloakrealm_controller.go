// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

const KeycloakRealmFinalizer = "neteye.cloud/keycloak-realm"

type KeycloakRealmReconciler struct{ KeycloakAPIReconciler }

// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakrealms,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakrealms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakrealms/finalizers,verbs=update
func (r *KeycloakRealmReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("keycloakrealm", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)
	realm := &neteye.KeycloakRealm{}
	if err := r.Get(ctx, req.NamespacedName, realm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	api, err := r.adminAPI(ctx, realm.Namespace) // nosemgrep: trailofbits.go.invalid-usage-of-modified-variable.invalid-usage-of-modified-variable
	if err != nil {
		if !realm.DeletionTimestamp.IsZero() {
			if r.isMissingCredentials(err) {
				log.Info("leaving Keycloak realm behind because local admin credentials are unavailable", "severity", "warning", "object", req.NamespacedName)
				return r.removeFinalizer(ctx, realm)
			}
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		if r.isMissingCredentials(err) {
			r.setStatus(ctx, req.NamespacedName, realm, neteye.ServiceStateFailed, missingCredentialsMessage(realm.Namespace))
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		r.setStatus(ctx, req.NamespacedName, realm, neteye.ServiceStateNotReady, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if !realm.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, realm, api)
	}
	if controllerutil.AddFinalizer(realm, KeycloakRealmFinalizer) {
		if err := r.Update(ctx, realm); err != nil {
			return ctrl.Result{}, fmt.Errorf("add keycloak realm finalizer: %w", err)
		}
	}
	result, err := keycloak.ReconcileRealm(ctx, api, realm.Spec)
	if err != nil {
		log.Error(err, "unable to reconcile Keycloak realm", "realm", realm.Spec.Realm)
		r.setStatus(ctx, req.NamespacedName, realm, neteye.ServiceStateFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if result.Created {
		log.Info("keycloak realm created", "realm", realm.Spec.Realm)
	} else if result.Updated {
		log.Info("keycloak realm drift reconciled", "realm", realm.Spec.Realm)
	}
	r.setStatus(ctx, req.NamespacedName, realm, neteye.ServiceStateReady, "Keycloak realm is reconciled")
	return ctrl.Result{RequeueAfter: r.reconciliationRequeue()}, nil
}

func (r *KeycloakRealmReconciler) reconcileDelete(ctx context.Context, realm *neteye.KeycloakRealm, api *keycloak.AdminAPI) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if !controllerutil.ContainsFinalizer(realm, KeycloakRealmFinalizer) {
		return ctrl.Result{}, nil
	}
	if realm.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		if err := keycloak.DeleteRealm(ctx, api, realm.Spec); err != nil {
			log.Error(err, "unable to delete Keycloak realm", "realm", realm.Spec.Realm, "requeueAfter", r.failureRequeue())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
	}
	return r.removeFinalizer(ctx, realm)
}

func (r *KeycloakRealmReconciler) removeFinalizer(ctx context.Context, realm *neteye.KeycloakRealm) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(realm, KeycloakRealmFinalizer)
	if err := r.Update(ctx, realm); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove keycloak realm finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *KeycloakRealmReconciler) setStatus(ctx context.Context, key client.ObjectKey, realm *neteye.KeycloakRealm, state neteye.ServiceState, message string) {
	status := neteye.KeycloakRealmStatus{Status: state, Message: message, ObservedGeneration: realm.Generation}
	writeStatus(ctx, r.Client, key, func() *neteye.KeycloakRealm { return &neteye.KeycloakRealm{} }, func(current *neteye.KeycloakRealm) { current.Status = status })
}

func (r *KeycloakRealmReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&neteye.KeycloakRealm{}, builder.WithPredicates(reconcileOnSpecOrDeletionChange)).Complete(r)
}
