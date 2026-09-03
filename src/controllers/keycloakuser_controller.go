// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// KeycloakUserFinalizer lets the operator delete the remote account before the
// KeycloakUser resource disappears, for the resources that ask for it through
// spec.deletionPolicy.
const KeycloakUserFinalizer = "neteye.cloud/keycloak-user"

const (
	// generatedPasswordLength matches the 32 characters the NetEye Ansible role
	// generates for the administrative accounts.
	generatedPasswordLength = 32
	// generatedPasswordAlphabet excludes symbols, as the Ansible role does, so
	// the password survives every consumer that stores it unquoted.
	generatedPasswordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// KeycloakUserReconciler keeps Keycloak accounts matching their KeycloakUser
// resources. As for KeycloakClient, Keycloak exposes no watch API, so drift is
// corrected by requeueing rather than by reacting to remote events.
type KeycloakUserReconciler struct {
	KeycloakAPIReconciler
}

// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakusers,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=neteye.cloud,resources=keycloakusers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update

// Reconcile reconciles one KeycloakUser against the Keycloak Admin API.
func (r *KeycloakUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("keycloakuser", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, log)

	kcu := &neteye.KeycloakUser{}
	if err := r.Get(ctx, req.NamespacedName, kcu); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch KeycloakUser")
		return ctrl.Result{}, err
	}

	api, err := r.adminAPI(ctx) // nosemgrep: trailofbits.go.invalid-usage-of-modified-variable.invalid-usage-of-modified-variable
	if err != nil {
		log.Error(err, "unable to build Keycloak Admin API client", "requeueAfter", r.failureRequeue())
		if !kcu.DeletionTimestamp.IsZero() {
			// Keycloak is unreachable; keep the finalizer and retry rather than
			// leaking or force-dropping the remote account.
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		r.setStatus(ctx, req.NamespacedName, kcu, neteye.ServiceStateNotReady, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	if !kcu.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, kcu, api)
	}

	if controllerutil.AddFinalizer(kcu, KeycloakUserFinalizer) {
		if err := r.Update(ctx, kcu); err != nil {
			return ctrl.Result{}, fmt.Errorf("add keycloak user finalizer: %w", err)
		}
	}

	credential, generated, err := r.credential(ctx, kcu)
	if err != nil {
		log.Error(err, "unable to resolve the account credential", "requeueAfter", r.failureRequeue())
		r.setStatus(ctx, req.NamespacedName, kcu, neteye.ServiceStateFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	result, err := keycloak.ReconcileUser(ctx, api, kcu.Spec, credential)
	if err != nil {
		log.Error(err, "unable to reconcile the Keycloak user", "username", kcu.Spec.Username, "requeueAfter", r.failureRequeue())
		r.setStatus(ctx, req.NamespacedName, kcu, neteye.ServiceStateFailed, err.Error())
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	switch {
	case result.Created:
		log.Info("keycloak user created", "username", kcu.Spec.Username, "realm", kcu.Spec.Realm)
	case result.Updated:
		log.Info("keycloak user drift reconciled", "username", kcu.Spec.Username, "realm", kcu.Spec.Realm)
	}

	// Adoption is a fact about the first reconciliation, so it is recorded once
	// and then left alone.
	if kcu.Status.UserID == "" {
		kcu.Status.Adopted = result.Adopted
	}
	kcu.Status.UserID = result.UserID

	// The generated password reaches the Secret only once Keycloak has accepted
	// it, so a failed reset never leaves behind a stored value nobody can log in
	// with. An unset password only happens when the account was adopted as-is and
	// no rotation was requested, i.e. no update was ever asked of Keycloak — that
	// is expected, not a failure.
	if generated && !result.PasswordSet && !result.Created && !credential.Rotate {
		log.Info("not storing the generated password: account already exists and is not being rotated", "username", kcu.Spec.Username)
	} else if generated {
		if !result.PasswordSet {
			err := fmt.Errorf("generated password was not applied to account %q", kcu.Spec.Username)
			log.Error(err, "refusing to store an unapplied generated password", "requeueAfter", r.failureRequeue())
			r.setStatus(ctx, req.NamespacedName, kcu, neteye.ServiceStateFailed, err.Error())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		if err := r.storePassword(ctx, kcu, credential.Password); err != nil {
			log.Error(err, "unable to store the generated password", "requeueAfter", r.failureRequeue())
			r.setStatus(ctx, req.NamespacedName, kcu, neteye.ServiceStateFailed, err.Error())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
	}

	if kcu.Spec.Credential != nil {
		kcu.Status.CredentialRotation = kcu.Spec.Credential.RotationToken
	}
	r.setStatus(ctx, req.NamespacedName, kcu, neteye.ServiceStateReady, "Keycloak user is reconciled")
	return ctrl.Result{RequeueAfter: r.reconciliationRequeue()}, nil
}

func (r *KeycloakUserReconciler) reconcileDelete(ctx context.Context, kcu *neteye.KeycloakUser, api *keycloak.AdminAPI) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if !controllerutil.ContainsFinalizer(kcu, KeycloakUserFinalizer) {
		return ctrl.Result{}, nil
	}
	// Delete is the default, so an unset policy — a resource built in code, or
	// one predating the field — removes the account too.
	if kcu.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		if err := keycloak.DeleteUser(ctx, api, kcu.Spec); err != nil {
			log.Error(err, "unable to delete the Keycloak user", "username", kcu.Spec.Username, "requeueAfter", r.failureRequeue())
			return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
		}
		log.Info("keycloak user deleted", "username", kcu.Spec.Username, "realm", kcu.Spec.Realm)
	} else {
		log.Info("keycloak user orphaned by deletion policy", "username", kcu.Spec.Username, "realm", kcu.Spec.Realm)
	}
	controllerutil.RemoveFinalizer(kcu, KeycloakUserFinalizer)
	if err := r.Update(ctx, kcu); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove keycloak user finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// credential resolves the password to apply and reports whether it was
// generated, meaning the operator still has to store it. A password already
// present in the referenced Secret is reused, so a reconciliation never mints a
// second password for the same account.
func (r *KeycloakUserReconciler) credential(ctx context.Context, kcu *neteye.KeycloakUser) (keycloak.UserCredential, bool, error) {
	spec := kcu.Spec.Credential
	if spec == nil {
		return keycloak.UserCredential{}, false, nil
	}

	rotate := spec.RotationToken != "" &&
		kcu.Status.CredentialRotation != "" &&
		spec.RotationToken != kcu.Status.CredentialRotation

	stored, err := r.storedPassword(ctx, kcu)
	if err != nil {
		return keycloak.UserCredential{}, false, err
	}

	credential := keycloak.UserCredential{Temporary: spec.Temporary, Rotate: rotate}
	switch {
	case !spec.Generate:
		if stored == "" {
			return credential, false, fmt.Errorf("secret %q in namespace %q has no value for key %q", spec.SecretRef.Name, kcu.Namespace, spec.SecretRef.Key)
		}
		credential.Password = stored
		return credential, false, nil
	case stored != "" && !rotate:
		// The password was generated by an earlier pass; reusing it keeps the
		// Secret and the account in agreement.
		credential.Password = stored
		return credential, false, nil
	default:
		password, err := generatePassword()
		if err != nil {
			return credential, false, err
		}
		credential.Password = password
		return credential, true, nil
	}
}

// storedPassword reads the password from the referenced Secret. A missing
// Secret is not an error: the operator creates it when it generates a password.
func (r *KeycloakUserReconciler) storedPassword(ctx context.Context, kcu *neteye.KeycloakUser) (string, error) {
	ref := kcu.Spec.Credential.SecretRef
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: kcu.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get credential secret %q in namespace %q: %w", ref.Name, kcu.Namespace, err)
	}
	return string(secret.Data[ref.Key]), nil
}

// storePassword writes the generated password to the referenced Secret. The
// Secret carries no owner reference on purpose: under the default deletion
// policy the account outlives the resource, so its credential has to as well.
func (r *KeycloakUserReconciler) storePassword(ctx context.Context, kcu *neteye.KeycloakUser, password string) error {
	ref := kcu.Spec.Credential.SecretRef
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: kcu.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[ref.Key] = []byte(password)
		return nil
	})
	if err != nil {
		return fmt.Errorf("store credential secret %q in namespace %q: %w", ref.Name, kcu.Namespace, err)
	}
	return nil
}

func (r *KeycloakUserReconciler) setStatus(ctx context.Context, key client.ObjectKey, kcu *neteye.KeycloakUser, state neteye.ServiceState, message string) {
	status := neteye.KeycloakUserStatus{
		Status:             state,
		Message:            message,
		UserID:             kcu.Status.UserID,
		Adopted:            kcu.Status.Adopted,
		CredentialRotation: kcu.Status.CredentialRotation,
		ObservedGeneration: kcu.GetGeneration(),
	}
	writeStatus(ctx, r.Client, key, func() *neteye.KeycloakUser { return &neteye.KeycloakUser{} },
		func(current *neteye.KeycloakUser) { current.Status = status })
}

func (r *KeycloakUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Setting a deletion timestamp bumps the generation, so the finalizer
		// still runs under this predicate while status writes do not requeue.
		For(&neteye.KeycloakUser{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func generatePassword() (string, error) {
	limit := big.NewInt(int64(len(generatedPasswordAlphabet)))
	password := make([]byte, generatedPasswordLength)
	for index := range password {
		pick, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		password[index] = generatedPasswordAlphabet[pick.Int64()]
	}
	return string(password), nil
}
