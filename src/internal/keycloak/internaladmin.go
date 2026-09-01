// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const (
	// InternalAdminUsername is the administrative account the platform owns in
	// Keycloak, the same one the NetEye Ansible role creates with the bootstrap
	// admin before deleting it.
	InternalAdminUsername = "neteye-internal-keycloak-admin"
	// InternalAdminResourceName is the KeycloakUser resource declaring it.
	InternalAdminResourceName = "neteye-internal-admin"
	// InternalAdminSecretName holds the password of the internal admin. It is
	// separate from AdminSecretName, which belongs to the bootstrap account.
	InternalAdminSecretName = InstanceName + "-internal-admin"
	// InternalAdminSecretPasswordKey is the key holding that password.
	InternalAdminSecretPasswordKey = "password"
	// InternalAdminRealmRole is the realm role the account needs to administer
	// the master realm.
	InternalAdminRealmRole = "admin"
)

// EnsureInternalAdminUser declares the administrative account the platform owns
// as a KeycloakUser, so that the KeycloakUser controller creates it in Keycloak
// with the bootstrap credentials and keeps it reconciled afterwards.
//
// It only creates the resource when it is missing: an administrator who edits
// the CR keeps their changes, which would not survive an unconditional apply.
// Callers must invoke it after the Keycloak instance is Ready, since the
// KeycloakUser controller needs a reachable Admin API to make progress.
func (c *Component) EnsureInternalAdminUser(ctx context.Context, namespace string) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: InternalAdminResourceName}
	existing := &neteye.KeycloakUser{}
	if err := c.client.Get(ctx, key, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get keycloak internal admin user: %w", err)
	}

	user := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: InternalAdminResourceName},
		Spec:       internalAdminSpec(),
	}
	if err := c.client.Create(ctx, user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create keycloak internal admin user: %w", err)
	}
	log.Info("declared the Keycloak internal admin user", "keycloakuser", InternalAdminResourceName, "username", InternalAdminUsername, "namespace", namespace)
	return nil
}

// internalAdminSpec describes the account: a permanent administrator whose
// password the cluster owns, orphaned rather than deleted so that removing the
// NetEye resource never locks the platform out of its own Keycloak.
func internalAdminSpec() neteye.KeycloakUserSpec {
	enabled := true
	return neteye.KeycloakUserSpec{
		Realm:      masterRealm,
		Username:   InternalAdminUsername,
		Enabled:    &enabled,
		RealmRoles: []string{InternalAdminRealmRole},
		Credential: &neteye.KeycloakUserCredentialSpec{
			SecretRef: neteye.NetEyeSecretKeySelector{
				Name: InternalAdminSecretName,
				Key:  InternalAdminSecretPasswordKey,
			},
			Generate: true,
		},
		DeletionPolicy: neteye.KeycloakUserDeletionPolicyOrphan,
	}
}

// EnsureBootstrapAdminDisabled disables the bootstrap admin account once the
// internal administrative account is proven to work, mirroring the Ansible role
// deleting its temporary bootstrap user at the end of the setup.
//
// It disables rather than deletes: the account is the operator's way back in if
// the internal credential is ever lost, and an administrator can re-enable it
// from the console. Nothing happens unless the operator currently authenticates
// as the internal admin, so the last usable credential is never taken away.
func (c *Component) EnsureBootstrapAdminDisabled(ctx context.Context, namespace string) error {
	log := ctrl.LoggerFrom(ctx)
	api, username, err := ResolveAdminAPI(ctx, c.client, namespace, c.AdminAPIFactory)
	if err != nil {
		return err
	}
	if username != InternalAdminUsername {
		log.V(1).Info("keeping the bootstrap admin enabled until the internal admin is usable", "authenticatedAs", username)
		return nil
	}

	bootstrap, err := bootstrapAdminCredentials(ctx, c.client, namespace)
	if err != nil {
		// Without the Secret there is no account name to disable, which is the
		// normal state against a pre-existing Keycloak that never had one.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	live, err := api.GetUser(ctx, masterRealm, bootstrap.Username)
	if err != nil {
		return fmt.Errorf("get keycloak bootstrap admin %q: %w", bootstrap.Username, err)
	}
	if live == nil {
		return nil
	}
	if enabled, ok := live["enabled"].(bool); ok && !enabled {
		return nil
	}

	disabled := mergeRepresentation(live, representation{"enabled": false})
	if err := api.UpdateUser(ctx, masterRealm, stringValue(live, "id"), disabled); err != nil {
		return fmt.Errorf("disable keycloak bootstrap admin %q: %w", bootstrap.Username, err)
	}
	log.Info("disabled the Keycloak bootstrap admin", "username", bootstrap.Username, "authenticatedAs", username)
	return nil
}
