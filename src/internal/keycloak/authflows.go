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

// firstBrokerLoginFlowSpec detects an existing broker user by the identity
// provider's configured attribute reference and links the account
// automatically, without prompting the user.
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
				Alias:         "Detect existing broker user",
				Config: &neteye.KeycloakAuthFlowConfig{
					Alias: "test",
					Values: map[string]string{
						"authenticatorReference":       "some-reference",
						"authenticatorReferenceMaxAge": "3600",
					},
				},
			},
			{
				Requirement:   "REQUIRED",
				Authenticator: "idp-auto-link",
				Alias:         "Automatically set existing user",
			},
		},
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}

// idpDiscoveryFlowSpec is the platform's browser flow: it tries the session
// cookie, then discovers the identity provider from the ADFS default and the
// user's email domain, blocking LDAP accounts from falling back to
// username/password before they reach it.
func idpDiscoveryFlowSpec() neteye.KeycloakAuthFlowSpec {
	alias := IdpDiscoveryFlowResourceName
	return neteye.KeycloakAuthFlowSpec{
		Realm: masterRealm,
		Alias: alias,
		Executions: []neteye.KeycloakAuthFlowExecution{
			{
				Requirement:   "ALTERNATIVE",
				Authenticator: "auth-cookie",
				Alias:         "Cookie",
			},
			{
				Requirement:   "ALTERNATIVE",
				Authenticator: "identity-provider-redirector",
				Alias:         alias + " adfs",
				Config: &neteye.KeycloakAuthFlowConfig{
					Alias:  "adfs",
					Values: map[string]string{"defaultProvider": "adfs"},
				},
			},
			{
				Requirement:   "ALTERNATIVE",
				Authenticator: "home-idp-discovery",
				Alias:         alias + " home-idp-discovery",
				Config: &neteye.KeycloakAuthFlowConfig{
					Alias: "home-idp-discovery",
					Values: map[string]string{
						"forwardToFirstMatch":    "true",
						"bypassLoginPage":        "true",
						"forwardToLinkedIdp":     "false",
						"forwardUnverifiedEmail": "true",
						"userAttribute":          "email",
					},
				},
			},
			{
				Requirement: "CONDITIONAL",
				Flow: &neteye.KeycloakAuthFlowExecutionSpecL2{
					Alias: alias + " Block LDAP logins",
					Executions: []neteye.KeycloakAuthFlowExecutionL2{
						{
							Requirement:   "REQUIRED",
							Authenticator: "conditional-user-attribute",
							Alias:         alias + " check-ldap-user",
							Config: &neteye.KeycloakAuthFlowConfig{
								Alias: "check-ldap-user",
								Values: map[string]string{
									"attribute_expected_value": ".+",
									"attribute_name":           "LDAP_ID",
									"regex":                    "true",
								},
							},
						},
						{
							Requirement:   "REQUIRED",
							Authenticator: "deny-access-authenticator",
							Alias:         alias + " deny-ldap",
							Config: &neteye.KeycloakAuthFlowConfig{
								Alias: "deny-ldap",
								Values: map[string]string{
									"denyErrorMessage": "Login through pb***** is not supported anymore, please use as username WN***@wgs.wuerth.com or name.surname@wuerth-it.com",
								},
							},
						},
					},
				},
			},
			{
				Requirement: "ALTERNATIVE",
				Flow: &neteye.KeycloakAuthFlowExecutionSpecL2{
					Alias:    alias + " username-password",
					Provider: "basic-flow",
					Executions: []neteye.KeycloakAuthFlowExecutionL2{
						{
							Requirement:   "REQUIRED",
							Authenticator: "auth-username-password-form",
							Alias:         "Username Password Form",
						},
					},
				},
			},
		},
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}
