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
	// RootUsername is the administrative account IcingaWeb2 (icw2) authenticates
	// as, the same one the NetEye Ansible role creates with the "root" username
	// in the keycloak-create-user role.
	RootUsername = "root"
	// RootResourceName is the KeycloakUser resource declaring it.
	RootResourceName = "neteye-root"
	// RootSecretName holds the password of the root user.
	RootSecretName = InstanceName + "-root"
	// RootSecretPasswordKey is the key holding that password.
	RootSecretPasswordKey = "password"
	// RootRealmRole is the realm role the account needs to administer the
	// master realm.
	RootRealmRole = "admin"
)

// EnsureRootUser declares the IcingaWeb2 root account as a KeycloakUser, so
// that the KeycloakUser controller creates it in Keycloak and keeps it
// reconciled afterwards, replacing the Ansible "Create root user" task.
//
// It only creates the resource when it is missing: an administrator who edits
// the CR keeps their changes, which would not survive an unconditional apply.
// Callers must invoke it after the internal admin user is usable, since the
// KeycloakUser controller needs a reachable Admin API to make progress.
func (c *Component) EnsureRootUser(ctx context.Context, namespace string) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: RootResourceName}
	existing := &neteye.KeycloakUser{}
	if err := c.client.Get(ctx, key, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get keycloak root user: %w", err)
	}

	user := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: RootResourceName},
		Spec:       rootUserSpec(),
	}
	if err := c.client.Create(ctx, user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create keycloak root user: %w", err)
	}
	log.Info("declared the Keycloak root user", "keycloakuser", RootResourceName, "username", RootUsername, "namespace", namespace)
	return nil
}

// rootUserSpec describes the account: orphaned rather than deleted so that
// removing the NetEye resource never locks IcingaWeb2 out of its own
// Keycloak account.
func rootUserSpec() neteye.KeycloakUserSpec {
	enabled := true
	return neteye.KeycloakUserSpec{
		Realm:      masterRealm,
		Username:   RootUsername,
		Enabled:    &enabled,
		RealmRoles: []string{RootRealmRole},
		Credential: &neteye.KeycloakUserCredentialSpec{
			SecretRef: neteye.NetEyeSecretKeySelector{
				Name: RootSecretName,
				Key:  RootSecretPasswordKey,
			},
			Generate: true,
		},
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}
