// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package permissionsyncconfig defines the PermissionSync integration values
// the NetEye operator owns on both sides of the boundary: the audience and
// logical-target scope grammar PermissionSync verifies in a caller's token,
// and the Keycloak client the technical caller authenticates as. It exists so
// the Keycloak and PermissionSync components agree on them without one
// importing the other.
package permissionsyncconfig

const (
	// Audience is the audience PermissionSync requires in every caller token.
	// Additional audience values are permitted, so a token minted for other
	// uses is not rejected for carrying them too.
	Audience = "permissionsync"

	// ScopePrefix prefixes the one scope token that selects a logical target.
	// A token carrying no such scope is a valid targetless no-op request.
	ScopePrefix = "permissionsync:"

	// CallerClientID is the confidential Keycloak client the Keycloak
	// login-sync authenticator obtains its service-account token from. The
	// authenticator configuration is not operator-managed; see the
	// PermissionSync ADR for that boundary.
	CallerClientID = "login-sync"

	// CallerClientResourceName is the KeycloakClient resource declaring it.
	CallerClientResourceName = CallerClientID

	// AudienceMapperName is the protocol mapper that puts Audience into the
	// tokens issued for one logical target.
	AudienceMapperName = "permissionsync-audience"

	// WorkloadAppLabel selects the PermissionSync pods. The identity service
	// has to reach them, so its own egress policy selects them by this label.
	WorkloadAppLabel = "permissionsync"

	// ListenerPort is PermissionSync's single listener port, which both its
	// own Service and the identity service's egress policy refer to.
	ListenerPort = int32(8443)

	// ProviderAudience is the audience the NetEye Permission Provider
	// validates. PermissionSync forwards the caller's own JWT to it, and the
	// provider validates that token as an independent resource server, so the
	// token carries this audience next to Audience (PermissionSync ADR-0008).
	ProviderAudience = "neteye-permission-provider"

	// ProviderAudienceMapperName puts ProviderAudience into the caller's tokens.
	ProviderAudienceMapperName = "neteye-permission-provider-audience"

	// Hostname is the name PermissionSync is published under on the shared
	// Gateway, which terminates TLS in front of its plaintext listener.
	Hostname = "permissionsync.neteyelocal"

	// SyncPath is PermissionSync's synchronization endpoint.
	SyncPath = "/api/sync-user"

	// CallerClientSecretName holds the login-sync client secret. The operator
	// generates it once and both Keycloak sides read it: the KeycloakClient,
	// which sets it on the client, and the login-sync authenticator, which
	// authenticates with it.
	CallerClientSecretName = "permissionsync-login-sync-client"
	// CallerClientSecretKey is the key inside CallerClientSecretName.
	CallerClientSecretKey = "client-secret"

	// BrowserFlowAlias is the browser flow that runs the login-sync
	// authenticator after authentication. It is bound only to the Keycloak
	// clients that are PermissionSync targets.
	BrowserFlowAlias = "neteye-permissionsync-browser"

	// CallerTimeoutMarginMilliseconds is added to the PermissionSync overall
	// request deadline to give the login-sync authenticator's own timeout. A
	// caller must wait longer than the receiver's deadline, or it gives up on
	// a request PermissionSync would still have answered.
	CallerTimeoutMarginMilliseconds = int64(5000)
)

// SyncURL is the URL the login-sync authenticator posts to.
func SyncURL() string {
	return "https://" + Hostname + SyncPath
}

// ScopeName returns the client scope a login client must have in its realm for
// the login-sync authenticator to treat it as a PermissionSync target. The
// authenticator derives the target from the login client's own clientId, so a
// logical target is only reachable when this scope exists under that name.
func ScopeName(logicalTarget string) string {
	return ScopePrefix + logicalTarget
}
