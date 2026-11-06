// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// tenantRealmComponentID is the Keycloak realm of the tenant. Its
// KeycloakRealm resource lives in the tenant namespace, so it depends on it.
const tenantRealmComponentID componentID = "realm"

// errNoAdminProvider reports a tenant controller built without the Keycloak
// admin provider, a programming error.
var errNoAdminProvider = errors.New("the tenant controller has no Keycloak admin provider")

// reconcileRealm gives the tenant its own Keycloak realm, with nothing in it.
//
// The KeycloakRealm resource of the tenant lives in the tenant namespace, so
// its controller uses the administrator credentials of that namespace and
// never the internal administrator of the platform. The tenant creates them:
//
//  1. with the internal administrator, the realm, marked with the tenant,
//     which Keycloak gives a management client in the master realm;
//  2. a Secret in the tenant namespace with the username and a generated
//     password of the tenant realm administrator;
//  3. with the internal administrator, that account in the master realm,
//     with an email that marks it, and with every role of the management client
//     except impersonation, so it can administer the tenant realm and
//     nothing else;
//  4. the KeycloakRealm resource, which then manages the realm settings.
func (r *NetEyeTenantReconciler) reconcileRealm(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	api, err := r.internalKeycloakAdmin(ctx)
	if err != nil {
		return r.keycloakFailure("KeycloakAdminFailed", err)
	}
	realm := tenant.Name
	realmCreated, err := keycloak.EnsureTenantRealm(ctx, api, realm, tenant.Name)
	if err != nil {
		return r.keycloakFailure("RealmCreateFailed", err)
	}
	if realmCreated {
		ctrl.LoggerFrom(ctx).Info("created the Keycloak realm of the tenant", "realm", realm)
	}

	username := keycloak.TenantRealmAdminUsername(realm)
	secret, err := r.ensureTenantRealmAdminSecret(ctx, tenant, api, username)
	if err != nil {
		return r.keycloakFailure("AdminSecretFailed", err)
	}
	password := string(secret.Data[keycloak.AdminSecretPasswordKey])
	adminCreated, err := keycloak.EnsureTenantRealmAdmin(ctx, api, realm, username, password)
	if err != nil {
		return r.keycloakFailure("RealmAdminFailed", err)
	}
	if !adminCreated {
		if err := r.applyTenantRealmAdminPassword(ctx, tenant, api, password); err != nil {
			return r.keycloakFailure("RealmAdminFailed", err)
		}
	}

	resource, err := r.ensureTenantKeycloakRealm(ctx, tenant)
	if err != nil {
		return degradedResult(tenantRealmComponentID, "KeycloakRealmFailed", err.Error(), r.failureRequeue(), err)
	}
	if resource.Status.ObservedGeneration != resource.Generation || resource.Status.Status != neteye.ServiceStateReady {
		message := fmt.Sprintf("waiting for the KeycloakRealm %q to be reconciled", resource.Name)
		if resource.Status.Message != "" {
			message += ": " + resource.Status.Message
		}
		result, err := progressingResult(tenantRealmComponentID, "WaitingForRealm", message, r.failureRequeue())
		// The realm exists with its signing keys, which is all that
		// PermissionSync needs; only its settings are pending.
		result.ServesDependants = true
		return result, err
	}
	return readyResult(tenantRealmComponentID, "Available", fmt.Sprintf("Keycloak realm %q is ready", realm))
}

// keycloakFailure reports a failure of the Keycloak Admin API. Missing
// platform credentials and a Keycloak that cannot answer now are temporary,
// as during the installation, so the component waits instead of failing.
func (r *NetEyeTenantReconciler) keycloakFailure(reason string, err error) (componentResult, error) {
	switch {
	case errors.Is(err, keycloak.ErrNoAdminCredentials):
		return progressingResult(tenantRealmComponentID, "WaitingForKeycloak", "waiting for the Keycloak administrator credentials of the platform", r.failureRequeue())
	case keycloak.IsUnavailable(err):
		return progressingResult(tenantRealmComponentID, "KeycloakUnavailable", fmt.Sprintf("the Keycloak Admin API is not available: %v", err), r.failureRequeue())
	default:
		return degradedResult(tenantRealmComponentID, reason, err.Error(), r.failureRequeue(), err)
	}
}

// ensureTenantRealmAdminSecret returns the Secret of the tenant namespace with
// the credentials of the tenant realm administrator. It writes the Secret when
// it is missing or incomplete, with a password that satisfies the policy of
// the master realm.
func (r *NetEyeTenantReconciler) ensureTenantRealmAdminSecret(ctx context.Context, tenant *neteye.NetEyeTenant, api *keycloak.AdminAPI, username string) (*corev1.Secret, error) {
	// The Secret is read from the API server: a Secret that the cache does
	// not show yet would make the operator generate another password.
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: tenant.Namespace, Name: keycloak.AdminSecretName}
	err := r.apiReader().Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get Secret %q: %w", keycloak.AdminSecretName, err)
	}
	found := err == nil
	if found && !metav1.IsControlledBy(secret, tenant) {
		return nil, fmt.Errorf("the Secret %q exists and the tenant does not control it", keycloak.AdminSecretName)
	}
	if found && string(secret.Data[keycloak.AdminSecretUsernameKey]) == username && len(secret.Data[keycloak.AdminSecretPasswordKey]) > 0 {
		return secret, nil
	}

	policy, err := api.GetPasswordPolicy(ctx, "master")
	if err != nil {
		return nil, fmt.Errorf("read the password policy of the master realm: %w", err)
	}
	password, err := keycloak.GeneratePassword(policy)
	if err != nil {
		return nil, fmt.Errorf("generate the password of the tenant realm administrator: %w", err)
	}
	data := map[string][]byte{
		keycloak.AdminSecretUsernameKey: []byte(username),
		keycloak.AdminSecretPasswordKey: []byte(password),
	}
	if found {
		secret.Data = data
		if err := r.Update(ctx, secret); err != nil {
			return nil, fmt.Errorf("update Secret %q: %w", keycloak.AdminSecretName, err)
		}
		return secret, nil
	}
	secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: tenant.Namespace, Name: keycloak.AdminSecretName}, Type: corev1.SecretTypeOpaque, Data: data}
	if err := controllerutil.SetControllerReference(tenant, secret, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("create Secret %q: %w", keycloak.AdminSecretName, err)
	}
	return secret, nil
}

// applyTenantRealmAdminPassword makes Keycloak accept the password of the
// Secret for the tenant realm administrator, after a failed reset, a manual
// edit of the Secret or a reset outside the operator. The admin provider of
// the tenant namespace keeps a client while the password of the Secret does
// not change, and that client gets a new token when its token expires, so a
// password that Keycloak no longer accepts is found within one token
// lifetime. A password that works is not set again: the password policy of
// the master realm can refuse a recently used password. Neither is a password
// while Keycloak cannot answer.
func (r *NetEyeTenantReconciler) applyTenantRealmAdminPassword(ctx context.Context, tenant *neteye.NetEyeTenant, api *keycloak.AdminAPI, password string) error {
	tenantAPI, _, err := r.AdminProviders.For(tenant.Namespace).Get(ctx)
	if err == nil {
		err = tenantAPI.Verify(ctx)
	}
	switch {
	case err == nil:
		return nil
	case keycloak.IsUnavailable(err):
		return err
	}
	if err := keycloak.SetTenantRealmAdminPassword(ctx, api, tenant.Name, password); err != nil {
		return err
	}
	r.AdminProviders.Forget(tenant.Namespace)
	return nil
}

// ensureTenantKeycloakRealm creates the KeycloakRealm resource of the tenant
// when it is missing. It has the name of the tenant and declares the realm of
// the same name, with the default settings of the resource.
func (r *NetEyeTenantReconciler) ensureTenantKeycloakRealm(ctx context.Context, tenant *neteye.NetEyeTenant) (*neteye.KeycloakRealm, error) {
	resource := &neteye.KeycloakRealm{}
	err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: tenant.Name}, resource)
	if err == nil {
		if !metav1.IsControlledBy(resource, tenant) {
			return nil, fmt.Errorf("the KeycloakRealm %q exists and the tenant does not control it", tenant.Name)
		}
		if resource.Spec.Realm != tenant.Name {
			return nil, fmt.Errorf("the KeycloakRealm %q declares realm %q instead of %q", tenant.Name, resource.Spec.Realm, tenant.Name)
		}
		return resource, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get KeycloakRealm %q: %w", tenant.Name, err)
	}
	resource = &neteye.KeycloakRealm{
		ObjectMeta: metav1.ObjectMeta{Namespace: tenant.Namespace, Name: tenant.Name},
		Spec:       neteye.KeycloakRealmSpec{Realm: tenant.Name},
	}
	if err := controllerutil.SetControllerReference(tenant, resource, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, resource); err != nil {
		return nil, fmt.Errorf("create KeycloakRealm %q: %w", tenant.Name, err)
	}
	return resource, nil
}

// deleteTenantRealm removes the realm of the tenant, its KeycloakRealm
// resource and its administrator. It reports false while it must wait.
//
// The internal administrator deletes the realm first, so the deletion never
// depends on the credentials of the tenant namespace, which garbage
// collection or a manual change can remove or break. The KeycloakRealm
// resource then has nothing left to delete: when it still waits on its
// finalizer, the tenant removes the finalizer, because the realm is gone.
// Then the internal administrator deletes the administrator account.
//
// When the platform has no Keycloak credentials any more, as when NetEye is
// being removed, the KeycloakRealm resource is deleted with the credentials
// of the tenant, and the account is left behind instead of blocking the
// deletion of the tenant.
//
// A realm without the mark of the tenant is never deleted: the KeycloakRealm
// resource then orphans it.
func (r *NetEyeTenantReconciler) deleteTenantRealm(ctx context.Context, tenant *neteye.NetEyeTenant) (bool, error) {
	api, err := r.internalKeycloakAdmin(ctx)
	platformCredentials := true
	switch {
	case errors.Is(err, keycloak.ErrNoAdminCredentials):
		platformCredentials = false
	case err != nil:
		return false, err
	default:
		if err := keycloak.DeleteTenantRealm(ctx, api, tenant.Name, tenant.Name); err != nil {
			return false, err
		}
	}

	gone, err := r.deleteTenantKeycloakRealm(ctx, tenant, api, platformCredentials)
	if err != nil || !gone {
		return false, err
	}
	if platformCredentials {
		if err := keycloak.DeleteTenantRealmAdmin(ctx, api, tenant.Name); err != nil {
			return false, err
		}
	} else {
		ctrl.LoggerFrom(ctx).Info("leaving the Keycloak administrator of the tenant realm behind because the platform has no Keycloak credentials", "severity", "warning", "realm", tenant.Name)
	}
	r.AdminProviders.Forget(tenant.Namespace)
	return true, nil
}

// deleteTenantKeycloakRealm deletes the KeycloakRealm resource of the tenant
// and reports whether it is gone. With platform credentials, the realm is
// already deleted or orphaned, so a finalizer that still waits is removed.
func (r *NetEyeTenantReconciler) deleteTenantKeycloakRealm(ctx context.Context, tenant *neteye.NetEyeTenant, api *keycloak.AdminAPI, platformCredentials bool) (bool, error) {
	key := types.NamespacedName{Namespace: tenant.Namespace, Name: tenant.Name}
	resource := &neteye.KeycloakRealm{}
	if err := r.apiReader().Get(ctx, key, resource); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get KeycloakRealm %q: %w", tenant.Name, err)
	}
	if !metav1.IsControlledBy(resource, tenant) {
		return true, nil
	}
	if resource.DeletionTimestamp.IsZero() {
		if err := r.orphanUnmarkedRealm(ctx, tenant, resource, api, platformCredentials); err != nil {
			return false, err
		}
		if err := r.Delete(ctx, resource); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete KeycloakRealm %q: %w", resource.Name, err)
		}
		if err := r.apiReader().Get(ctx, key, resource); err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, fmt.Errorf("get KeycloakRealm %q: %w", tenant.Name, err)
		}
	}
	if !platformCredentials || !controllerutil.ContainsFinalizer(resource, KeycloakRealmFinalizer) {
		return false, nil
	}
	if resource.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		exists, err := keycloak.TenantRealmExists(ctx, api, tenant.Name)
		if err != nil || exists {
			return false, err
		}
	}
	patch := client.MergeFrom(resource.DeepCopy())
	controllerutil.RemoveFinalizer(resource, KeycloakRealmFinalizer)
	if err := r.Patch(ctx, resource, patch); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("remove the finalizer of KeycloakRealm %q: %w", resource.Name, err)
	}
	return true, nil
}

// orphanUnmarkedRealm sets the deletion policy of the KeycloakRealm resource
// to Orphan when its realm exists without the mark of the tenant, so that its
// finalizer leaves the realm alone, as DeleteTenantRealm does. Without
// platform credentials, the credentials of the tenant check the mark, because
// the finalizer uses them. Without those either, the finalizer leaves the
// realm behind.
func (r *NetEyeTenantReconciler) orphanUnmarkedRealm(ctx context.Context, tenant *neteye.NetEyeTenant, resource *neteye.KeycloakRealm, api *keycloak.AdminAPI, platformCredentials bool) error {
	if resource.Spec.DeletionPolicy == neteye.KeycloakDeletionPolicyOrphan {
		return nil
	}
	if !platformCredentials {
		tenantAPI, _, err := r.AdminProviders.For(tenant.Namespace).Get(ctx)
		if errors.Is(err, keycloak.ErrNoAdminCredentials) {
			return nil
		}
		if err != nil {
			return err
		}
		api = tenantAPI
	}
	unmarked, err := keycloak.TenantRealmUnmarked(ctx, api, tenant.Name, tenant.Name)
	if err != nil || !unmarked {
		return err
	}
	patch := client.MergeFrom(resource.DeepCopy())
	resource.Spec.DeletionPolicy = neteye.KeycloakDeletionPolicyOrphan
	if err := r.Patch(ctx, resource, patch); err != nil {
		return fmt.Errorf("orphan the realm of KeycloakRealm %q: %w", resource.Name, err)
	}
	ctrl.LoggerFrom(ctx).Info("leaving the Keycloak realm behind because it is not marked as owned by the tenant", "severity", "warning", "realm", tenant.Name)
	return nil
}

// internalKeycloakAdmin returns the Admin API client of the internal
// administrator of the platform, whose credentials live in the namespace of
// Keycloak.
func (r *NetEyeTenantReconciler) internalKeycloakAdmin(ctx context.Context) (*keycloak.AdminAPI, error) {
	if r.AdminProviders == nil {
		return nil, errNoAdminProvider
	}
	api, _, err := r.AdminProviders.For(keycloak.WorkloadNamespace).Get(ctx)
	return api, err
}
