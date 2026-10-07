// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

const (
	// PermissionSyncFlowResourceName is the KeycloakAuthFlow resource
	// declaring the PermissionSync browser flow.
	PermissionSyncFlowResourceName = permissionsyncconfig.BrowserFlowAlias
	// loginSyncAuthenticator is the provider id of the login-sync
	// authenticator the neteye-keycloak image ships.
	loginSyncAuthenticator = "login-sync"
)

// EnsurePermissionSyncFlow declares the browser flow that synchronizes a
// user's permissions after every login on a PermissionSync target client.
func (c *Component) EnsurePermissionSyncFlow(ctx context.Context, namespace string) error {
	return c.ensureAuthFlow(ctx, namespace, PermissionSyncFlowResourceName, permissionSyncFlowSpec())
}

// permissionSyncFlowSpec is Keycloak's built-in browser flow, wrapped in a
// REQUIRED subflow and followed by the login-sync authenticator.
//
// login-sync has to be a top-level REQUIRED execution after authentication:
// it needs an authenticated user, and only at the top level does it run for
// every login, including one an existing SSO cookie completes, which is how a
// user already signed in to NetEye reaches GLPI. A REQUIRED sibling makes
// Keycloak ignore the ALTERNATIVE executions at the same level, so the
// authentication alternatives move one level down into their own subflow,
// with the conditional second factor exactly as Keycloak ships it.
//
// No binding is declared: the flow is bound only to the Keycloak clients that
// are PermissionSync targets (see EnsurePermissionSyncCaller), so that a
// PermissionSync outage, which the fail-closed authenticator turns into a
// refused login, never locks anyone out of a client that has no target,
// including the admin console.
func permissionSyncFlowSpec() neteye.KeycloakAuthFlowSpec {
	return neteye.KeycloakAuthFlowSpec{
		Realm: masterRealm,
		Alias: permissionsyncconfig.BrowserFlowAlias,
		Executions: []neteye.KeycloakAuthFlowExecution{
			{
				Requirement: "REQUIRED",
				Flow: &neteye.KeycloakAuthFlowExecutionSpecL2{
					Alias:    permissionsyncconfig.BrowserFlowAlias + "-authentication",
					Provider: "basic-flow",
					Executions: []neteye.KeycloakAuthFlowExecutionL2{
						{Requirement: "ALTERNATIVE", Authenticator: "auth-cookie"},
						{Requirement: "DISABLED", Authenticator: "auth-spnego"},
						{Requirement: "ALTERNATIVE", Authenticator: "identity-provider-redirector"},
						{
							Requirement: "ALTERNATIVE",
							Flow: &neteye.KeycloakAuthFlowExecutionSpecL3{
								Alias:    permissionsyncconfig.BrowserFlowAlias + "-forms",
								Provider: "basic-flow",
								Executions: []neteye.KeycloakAuthFlowExecutionL3{
									{Requirement: "REQUIRED", Authenticator: "auth-username-password-form"},
									{
										Requirement: "CONDITIONAL",
										Flow: &neteye.KeycloakAuthFlowExecutionSpecL4{
											Alias:    permissionsyncconfig.BrowserFlowAlias + "-conditional-2fa",
											Provider: "basic-flow",
											Executions: []neteye.KeycloakAuthFlowExecutionL4{
												{Requirement: "REQUIRED", Authenticator: "conditional-user-configured"},
												{
													Requirement:   "REQUIRED",
													Authenticator: "conditional-credential",
													Config: &neteye.KeycloakAuthFlowConfig{
														Alias:  permissionsyncconfig.BrowserFlowAlias + "-conditional-credential",
														Values: map[string]string{"credentials": "webauthn-passwordless"},
													},
												},
												{Requirement: "ALTERNATIVE", Authenticator: "auth-otp-form"},
												{Requirement: "DISABLED", Authenticator: "webauthn-authenticator"},
												{Requirement: "DISABLED", Authenticator: "auth-recovery-authn-code-form"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			{Requirement: "REQUIRED", Authenticator: loginSyncAuthenticator},
		},
		// Orphan: this resource is redeclared whenever it is missing, so
		// deleting it must not remove a flow target clients are bound to.
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}
