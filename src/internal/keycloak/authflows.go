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
	// FirstBrokerLoginFlowResourceName is the KeycloakAuthFlow resource
	// declaring the platform's first-broker-login flow.
	FirstBrokerLoginFlowResourceName = "neteye-first-broker-login-flow"
	// IdpDiscoveryFlowResourceName is the KeycloakAuthFlow resource declaring
	// the platform's IdP-discovery browser flow.
	IdpDiscoveryFlowResourceName = "neteye-idp-discovery-flow"
)

// EnsureFirstBrokerLoginFlow declares the platform's first-broker-login flow
// as a KeycloakAuthFlow, so an installation has it without anyone applying a
// manifest by hand. As for the NetEye client, it is created only when
// missing, leaving an administrator's edits in place.
func (c *Component) EnsureFirstBrokerLoginFlow(ctx context.Context, namespace string) error {
	return c.ensureAuthFlow(ctx, namespace, FirstBrokerLoginFlowResourceName, firstBrokerLoginFlowSpec())
}

// EnsureIdpDiscoveryFlow declares the platform's IdP-discovery flow as a
// KeycloakAuthFlow. See EnsureFirstBrokerLoginFlow.
func (c *Component) EnsureIdpDiscoveryFlow(ctx context.Context, namespace string) error {
	return c.ensureAuthFlow(ctx, namespace, IdpDiscoveryFlowResourceName, idpDiscoveryFlowSpec())
}

func (c *Component) ensureAuthFlow(ctx context.Context, namespace, name string, spec neteye.KeycloakAuthFlowSpec) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: name}
	existing := &neteye.KeycloakAuthFlow{}
	if err := c.client.Get(ctx, key, existing); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get keycloak auth flow %q: %w", name, err)
	}

	flow := &neteye.KeycloakAuthFlow{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}
	if err := c.client.Create(ctx, flow); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create keycloak auth flow %q: %w", name, err)
	}
	log.Info("declared the Keycloak auth flow", "keycloakauthflow", name, "alias", spec.Alias, "namespace", namespace)
	return nil
}

// firstBrokerLoginFlowSpec detects whether the identity the broker just
// authenticated already matches an existing local user (Keycloak's
// idp-detect-existing-broker-user authenticator takes no configuration; it
// looks the user up by the identity provider's federated identity) and links
// the account automatically, without prompting the user.
//
// Orphan: this resource is redeclared by the operator whenever it is missing,
// so deleting it must not take the remote flow down with it.
func firstBrokerLoginFlowSpec() neteye.KeycloakAuthFlowSpec {
	return neteye.KeycloakAuthFlowSpec{
		Realm: masterRealm,
		Alias: FirstBrokerLoginFlowResourceName,
		Executions: []neteye.KeycloakAuthFlowExecution{
			{
				Requirement:   "REQUIRED",
				Authenticator: "idp-detect-existing-broker-user",
			},
			{
				Requirement:   "REQUIRED",
				Authenticator: "idp-auto-link",
			},
		},
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}

// idpDiscoveryFlowSpec is the platform's browser flow: session cookie, then
// kc_idp_hint, then identity provider discovery by email domain, then a local
// username/password form.
//
// It mirrors the reference flow the NetEye Keycloak Ansible role installs
// under templates/flows/neteye-idp-discovery-flow; keep the two in sync.
//
// The operator declares no binding: pointing a realm purpose such as the
// browser flow at this flow is the administrator's decision.
func idpDiscoveryFlowSpec() neteye.KeycloakAuthFlowSpec {
	return neteye.KeycloakAuthFlowSpec{
		Realm: masterRealm,
		Alias: IdpDiscoveryFlowResourceName,
		Executions: []neteye.KeycloakAuthFlowExecution{
			{
				Requirement:   "ALTERNATIVE",
				Authenticator: "auth-cookie",
			},
			{
				// DISABLED as in the reference flow.
				Requirement:   "DISABLED",
				Authenticator: "identity-provider-redirector",
			},
			{
				Requirement:   "ALTERNATIVE",
				Authenticator: "home-idp-discovery",
				Alias:         "home-idp-discovery",
				Config: &neteye.KeycloakAuthFlowConfig{
					Alias: "home-idp-discovery",
					Values: map[string]string{
						"default.reference.value":  "",
						"default.reference.maxAge": "",
						"bypassLoginPage":          "true",
						"forwardToFirstMatch":      "true",
						"userAttribute":            "email",
						"forwardUnverifiedEmail":   "true",
						"forwardToLinkedIdp":       "false",
					},
				},
			},
			{
				Requirement: "ALTERNATIVE",
				Flow: &neteye.KeycloakAuthFlowExecutionSpecL2{
					Alias:    "username-password",
					Provider: "basic-flow",
					Executions: []neteye.KeycloakAuthFlowExecutionL2{
						{
							Requirement:   "REQUIRED",
							Authenticator: "auth-username-password-form",
						},
					},
				},
			},
		},
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}
