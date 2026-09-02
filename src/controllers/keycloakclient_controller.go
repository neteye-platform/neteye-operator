// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// KeycloakClientFinalizer makes the operator delete the remote Keycloak client
// before the KeycloakClient resource disappears from the cluster.
const KeycloakClientFinalizer = "neteye.cloud/keycloak-client"

// AdminAPIFactory builds the Admin API client used to talk to Keycloak. Tests
// substitute it to point at a stub server.
type AdminAPIFactory = keycloak.AdminAPIFactory

// KeycloakClientReconciler keeps Keycloak clients matching their KeycloakClient
// resources. Keycloak exposes no watch API, so drift is corrected by requeueing
// on ReconciliationRequeueAfter rather than by reacting to remote events.
type KeycloakClientReconciler struct {
	KeycloakAPIReconciler
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakclients,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakclients/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakclients/finalizers,verbs=update

// Reconcile reconciles one KeycloakClient against the Keycloak Admin API.
func (r *KeycloakClientReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("keycloakclient", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)

	kcc := &neteye.KeycloakClient{}
	if err := r.Get(ctx, req.NamespacedName, kcc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch KeycloakClient")
		return ctrl.Result{}, err
	}

	api, err := r.adminAPI(ctx) // nosemgrep: trailofbits.go.invalid-usage-of-modified-variable.invalid-usage-of-modified-variable
	if err != nil {
		log.Error(err, "unable to build Keycloak Admin API client", "requeueAfter", r.failureRequeue())
		if !kcc.DeletionTimestamp.IsZero() {
			// Keycloak is unreachable; keep the finalizer and retry rather than
			// leaking or force-dropping the remote client.
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		r.setStatus(ctx, req.NamespacedName, kcc, neteye.ServiceStateNotReady, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	if !kcc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, kcc, api)
	}

	if controllerutil.AddFinalizer(kcc, KeycloakClientFinalizer) {
		if err := r.Update(ctx, kcc); err != nil {
			return ctrl.Result{}, fmt.Errorf("add keycloak client finalizer: %w", err)
		}
	}

	clientSecret, err := r.clientSecret(ctx, kcc)
	if err != nil {
		log.Error(err, "unable to read the client secret", "requeueAfter", r.failureRequeue())
		r.setStatus(ctx, req.NamespacedName, kcc, neteye.ServiceStateFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	result, err := keycloak.ReconcileClient(ctx, api, kcc.Spec, clientSecret)
	if err != nil {
		log.Error(err, "unable to reconcile the Keycloak client", "clientId", kcc.Spec.ClientID, "requeueAfter", r.failureRequeue())
		r.setStatus(ctx, req.NamespacedName, kcc, neteye.ServiceStateFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	switch {
	case result.Created:
		log.Info("keycloak client created", "clientId", kcc.Spec.ClientID, "realm", kcc.Spec.Realm)
	case result.Updated:
		log.Info("keycloak client drift reconciled", "clientId", kcc.Spec.ClientID, "realm", kcc.Spec.Realm)
	}

	kcc.Status.ClientUUID = result.UUID
	r.setStatus(ctx, req.NamespacedName, kcc, neteye.ServiceStateReady, readyMessage(kcc))
	return ctrl.Result{RequeueAfter: r.reconciliationRequeue()}, nil
}

func readyMessage(kcc *neteye.KeycloakClient) string {
	if !kcc.Spec.PublicClient && kcc.Spec.SecretRef == nil {
		return "Keycloak client is reconciled; client secret is managed by Keycloak (no secretRef)"
	}
	return "Keycloak client is reconciled"
}

func (r *KeycloakClientReconciler) reconcileDelete(ctx context.Context, kcc *neteye.KeycloakClient, api *keycloak.AdminAPI) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if !controllerutil.ContainsFinalizer(kcc, KeycloakClientFinalizer) {
		return ctrl.Result{}, nil
	}
	// Delete is the default, so an unset policy removes the client too. Orphan is
	// what the platform's own client declares: the resource can come and go
	// without destroying a client secret its consumers still hold.
	if kcc.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		if err := keycloak.DeleteClient(ctx, api, kcc.Spec); err != nil {
			log.Error(err, "unable to delete the Keycloak client", "clientId", kcc.Spec.ClientID, "requeueAfter", r.failureRequeue())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		log.Info("keycloak client deleted", "clientId", kcc.Spec.ClientID, "realm", kcc.Spec.Realm)
	} else {
		log.Info("keycloak client orphaned by deletion policy", "clientId", kcc.Spec.ClientID, "realm", kcc.Spec.Realm)
	}
	controllerutil.RemoveFinalizer(kcc, KeycloakClientFinalizer)
	if err := r.Update(ctx, kcc); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove keycloak client finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// clientSecret resolves spec.secretRef. Public clients need no secret, so an
// absent reference is not an error for them.
func (r *KeycloakClientReconciler) clientSecret(ctx context.Context, kcc *neteye.KeycloakClient) (string, error) {
	ref := kcc.Spec.SecretRef
	if ref == nil {
		if !kcc.Spec.PublicClient {
			// Keycloak generates a secret for a confidential client on creation;
			// without a reference the operator simply leaves it alone.
			ctrl.LoggerFrom(ctx).V(1).Info("no client secret reference configured; leaving the Keycloak-generated secret in place", "clientId", kcc.Spec.ClientID)
		}
		return "", nil
	}
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: kcc.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("get client secret %q in namespace %q: %w", ref.Name, kcc.Namespace, err)
	}
	value := string(secret.Data[ref.Key])
	if value == "" {
		return "", fmt.Errorf("secret %q in namespace %q has no value for key %q", ref.Name, kcc.Namespace, ref.Key)
	}
	return value, nil
}

func (r *KeycloakClientReconciler) setStatus(ctx context.Context, key client.ObjectKey, kcc *neteye.KeycloakClient, state neteye.ServiceState, message string) {
	status := neteye.KeycloakClientStatus{
		Status:             state,
		Message:            message,
		ClientUUID:         kcc.Status.ClientUUID,
		ObservedGeneration: kcc.GetGeneration(),
	}
	writeStatus(ctx, r.Client, key, func() *neteye.KeycloakClient { return &neteye.KeycloakClient{} },
		func(current *neteye.KeycloakClient) { current.Status = status })
}

func (r *KeycloakClientReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Setting a deletion timestamp bumps the generation, so the finalizer
		// still runs under this predicate while status writes do not requeue.
		For(&neteye.KeycloakClient{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
