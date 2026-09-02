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
	// NetEyeClientID is the OpenID Connect client every NetEye deployment
	// authenticates its users through, as created by the keycloak-setup Ansible
	// role.
	NetEyeClientID = "neteye"
	// NetEyeClientResourceName is the KeycloakClient resource declaring it.
	NetEyeClientResourceName = "neteye"
)

// EnsureNetEyeClient declares the NetEye OpenID Connect client as a
// KeycloakClient, so an installation has it without anyone applying a manifest
// by hand. As for the internal admin, it is created only when missing, leaving
// an administrator's edits in place.
func (c *Component) EnsureNetEyeClient(ctx context.Context, namespace string) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: NetEyeClientResourceName}
	existing := &neteye.KeycloakClient{}
	if err := c.client.Get(ctx, key, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get neteye keycloak client: %w", err)
	}

	client := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: NetEyeClientResourceName},
		Spec:       netEyeClientSpec(),
	}
	if err := c.client.Create(ctx, client); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create neteye keycloak client: %w", err)
	}
	log.Info("declared the NetEye Keycloak client", "keycloakclient", NetEyeClientResourceName, "clientId", NetEyeClientID, "namespace", namespace)
	return nil
}

// netEyeClientSpec mirrors setup_neteye_client.yml: a confidential client with
// the standard and direct access flows, a service account, and the group
// membership mapper NetEye reads its groups from.
//
// No SecretRef is declared, so the client secret Keycloak generates is left
// alone rather than being replaced on every reconciliation.
func netEyeClientSpec() neteye.KeycloakClientSpec {
	enabled := true
	standardFlow := true
	return neteye.KeycloakClientSpec{
		Realm:        masterRealm,
		ClientID:     NetEyeClientID,
		Enabled:      &enabled,
		RedirectUris: []string{"/neteye/*"},
		PublicClient: false,
		StandardFlow: &standardFlow,
		DirectAccess: true,
		ServiceAccount: &neteye.KeycloakServiceAccountSpec{
			Enabled: true,
			// The service account reads users and groups through the Admin API of
			// the master realm, as the Ansible role grants it.
			ClientRoles: map[string][]string{
				"master-realm": {"view-users", "query-users", "query-groups"},
			},
		},
		// Orphan: this resource is redeclared by the operator whenever it is
		// missing, so deleting it must not take the client — and its secret —
		// down with it.
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
		ProtocolMappers: []neteye.KeycloakProtocolMapper{{
			Name:           "groups membership",
			Protocol:       openIDConnect,
			ProtocolMapper: "oidc-group-membership-mapper",
			Config: map[string]string{
				"full.path":            "true",
				"id.token.claim":       "true",
				"access.token.claim":   "true",
				"userinfo.token.claim": "true",
				"claim.name":           "groups",
				"jsonType.label":       "String",
				"multivalued":          "true",
			},
		}},
	}
}
