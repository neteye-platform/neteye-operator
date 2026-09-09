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
	// MasterRealmResourceName is the KeycloakRealm resource declaring the
	// master realm's configuration.
	MasterRealmResourceName = masterRealm
	// masterRealmTheme is the Keycloak theme every NetEye installation uses.
	masterRealmTheme = "neteye"
	// masterRealmDisplayName is the realm display name shown in the admin console.
	masterRealmDisplayName = "NetEye"
)

// EnsureMasterRealm declares the master realm's Events, BruteForceProtection,
// and Theme configuration as a KeycloakRealm, so the KeycloakRealm controller
// reconciles it without anyone applying a manifest by hand. NetEye's
// single-tenant setup is the master realm itself, the same realm
// EnsureNetEyeClient declares the NetEye client in.
//
// It only creates the resource when it is missing: an administrator who
// edits the CR keeps their changes, which would not survive an unconditional
// apply. Callers must invoke it after Keycloak is reachable, since the
// KeycloakRealm controller needs a reachable Admin API to make progress.
func (c *Component) EnsureMasterRealm(ctx context.Context, namespace string) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: MasterRealmResourceName}
	existing := &neteye.KeycloakRealm{}
	if err := c.client.Get(ctx, key, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get master keycloak realm: %w", err)
	}

	realm := &neteye.KeycloakRealm{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: MasterRealmResourceName},
		Spec:       masterRealmSpec(),
	}
	if err := c.client.Create(ctx, realm); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create master keycloak realm: %w", err)
	}
	log.Info("declared the master Keycloak realm configuration", "keycloakrealm", MasterRealmResourceName, "namespace", namespace)
	return nil
}

// masterRealmSpec configures the master realm. Events and
// BruteForceProtection are left at their zero value: the KeycloakRealm
// controller always enforces its own defaults for them regardless (see
// ADR-0004), so there is nothing to set here. Theme is set explicitly
// because it is optional and opt-in on KeycloakRealmSpec — unlike Events and
// BruteForceProtection, "wp" is NetEye's own branding, not a Keycloak-side
// default the CRD schema enforces on every realm.
func masterRealmSpec() neteye.KeycloakRealmSpec {
	return neteye.KeycloakRealmSpec{
		Realm:       masterRealm,
		DisplayName: masterRealmDisplayName,
		Theme: &neteye.KeycloakRealmTheme{
			LoginTheme:   masterRealmTheme,
			AdminTheme:   masterRealmTheme,
			AccountTheme: masterRealmTheme,
			EmailTheme:   masterRealmTheme,
		},
		// Orphan: this resource is redeclared by the operator whenever it is
		// missing, so deleting it must not take the master realm down with it
		// — Keycloak requires the master realm to exist for administration.
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}
