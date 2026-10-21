// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

const (
	permissionSyncCallerName        = "NetEye login synchronization"
	permissionSyncCallerDescription = "Service account the Keycloak login-sync authenticator obtains its PermissionSync token from."
	permissionSyncScopeDescription  = "Selects one PermissionSync logical target. Requested per login, never applied by default."
)

// EnsurePermissionSyncCaller provisions the Keycloak side of the PermissionSync
// contract and reports whether it is complete: one client scope per logical
// target, the confidential client whose service account the login-sync
// authenticator authenticates as, and the login flow running that
// authenticator, bound to every target client.
//
// The login-sync authenticator decides that a login client has a target by
// looking for a client scope named permissionsync:<clientId> in the login
// realm, so the scope has to exist under exactly that name for the target to
// be reachable at all. Each scope carries the PermissionSync audience and is
// assigned to the caller as Optional, never Default: a Default scope would
// travel on every token the caller obtains, so a caller serving two targets
// would emit two permissionsync: scopes in one token, which the receiver
// refuses.
//
// The client secret is the operator-generated one in
// CallerClientSecretName, which the Keycloak instance hands to the
// authenticator through a Secret reference, never in plaintext.
//
// It is not complete while the login flow or a target's own Keycloak client
// does not exist yet: the flow is declared here and reconciled by its own
// controller, and a target client is created by the module integrating it.
func (c *Component) EnsurePermissionSyncCaller(ctx context.Context, namespace string, logicalTargets []string) (bool, string, error) {
	api, _, err := c.admin(namespace).Get(ctx)
	if err != nil {
		return false, "", fmt.Errorf("resolve keycloak admin credentials: %w", err)
	}
	scopes := make([]string, 0, len(logicalTargets))
	for _, target := range logicalTargets {
		name := permissionsyncconfig.ScopeName(target)
		if err := ensureAudienceClientScope(ctx, api, masterRealm, name, permissionSyncScopeDescription, permissionsyncconfig.Audience, permissionsyncconfig.AudienceMapperName); err != nil {
			return false, "", err
		}
		scopes = append(scopes, name)
	}
	if err := c.ensurePermissionSyncCallerClient(ctx, namespace, scopes); err != nil {
		return false, "", err
	}
	if err := c.EnsurePermissionSyncFlow(ctx, namespace); err != nil {
		return false, "", err
	}
	return bindPermissionSyncFlow(ctx, api, logicalTargets)
}

// bindPermissionSyncFlow makes the PermissionSync login flow the browser flow
// of exactly the target clients.
//
// Only a target needs the synchronization, and the authenticator is
// fail-closed, so binding it realm-wide would turn a PermissionSync outage
// into a refused login everywhere, admin console included. A client that is
// no longer a target is unbound for the same reason: its login would still
// run the authenticator, and PermissionSync refuses a target it no longer
// routes. Unbinding restores Keycloak's own browser flow for that client.
func bindPermissionSyncFlow(ctx context.Context, api *AdminAPI, logicalTargets []string) (bool, string, error) {
	flow, err := api.GetAuthFlow(ctx, masterRealm, permissionsyncconfig.BrowserFlowAlias)
	if err != nil {
		return false, "", fmt.Errorf("get keycloak flow %q: %w", permissionsyncconfig.BrowserFlowAlias, err)
	}
	if flow == nil {
		return false, fmt.Sprintf("waiting for the Keycloak flow %q to be created", permissionsyncconfig.BrowserFlowAlias), nil
	}
	flowID := stringValue(flow, "id")

	clients, err := api.ListClients(ctx, masterRealm)
	if err != nil {
		return false, "", fmt.Errorf("list keycloak clients: %w", err)
	}
	targets := make(map[string]bool, len(logicalTargets))
	for _, target := range logicalTargets {
		targets[target] = true
	}
	found := make(map[string]bool, len(logicalTargets))
	for _, client := range clients {
		clientID := stringValue(client, "clientId")
		bound := browserFlowOverride(client) == flowID
		switch {
		case targets[clientID]:
			found[clientID] = true
			if !bound {
				if err := setBrowserFlowOverride(ctx, api, client, flowID); err != nil {
					return false, "", fmt.Errorf("bind the PermissionSync flow to keycloak client %q: %w", clientID, err)
				}
			}
		case bound:
			if err := setBrowserFlowOverride(ctx, api, client, ""); err != nil {
				return false, "", fmt.Errorf("unbind the PermissionSync flow from keycloak client %q: %w", clientID, err)
			}
		}
	}
	for _, target := range logicalTargets {
		if !found[target] {
			return false, fmt.Sprintf("waiting for the Keycloak client %q of PermissionSync target %q to be created", target, target), nil
		}
	}
	return true, "", nil
}

func browserFlowOverride(client representation) string {
	overrides, _ := client["authenticationFlowBindingOverrides"].(map[string]any)
	value, _ := overrides["browser"].(string)
	return value
}

// setBrowserFlowOverride points the client's browser flow at flowID, or clears
// the override when flowID is empty. Other overrides are kept.
func setBrowserFlowOverride(ctx context.Context, api *AdminAPI, client representation, flowID string) error {
	overrides := map[string]any{}
	if live, ok := client["authenticationFlowBindingOverrides"].(map[string]any); ok {
		for key, value := range live {
			overrides[key] = value
		}
	}
	if flowID == "" {
		delete(overrides, "browser")
	} else {
		overrides["browser"] = flowID
	}
	updated := representation{}
	for key, value := range client {
		updated[key] = value
	}
	updated["authenticationFlowBindingOverrides"] = overrides
	return api.UpdateClient(ctx, masterRealm, stringValue(client, "id"), updated)
}

// ensurePermissionSyncCallerClient declares the caller as a KeycloakClient. An
// existing resource is only extended with the scopes it is missing: like the
// rest of this API's Keycloak handling, it adds and never revokes, so an
// administrator's own additions survive and a target removed from the NetEye
// resource does not silently invalidate a caller still using it.
func (c *Component) ensurePermissionSyncCallerClient(ctx context.Context, namespace string, scopes []string) error {
	log := ctrl.LoggerFrom(ctx)
	key := types.NamespacedName{Namespace: namespace, Name: permissionsyncconfig.CallerClientResourceName}
	existing := &neteye.KeycloakClient{}
	switch err := c.client.Get(ctx, key, existing); {
	case err == nil:
		missing := missingScopes(existing.Spec.OptionalClientScopes, scopes)
		missingMappers := missingProtocolMappers(existing.Spec.ProtocolMappers, callerAudienceMappers())
		needsSecret := existing.Spec.ClientSecretRef == nil || *existing.Spec.ClientSecretRef != callerClientSecretRef()
		if len(missing) == 0 && len(missingMappers) == 0 && !needsSecret {
			return nil
		}
		existing.Spec.OptionalClientScopes = append(existing.Spec.OptionalClientScopes, missing...)
		existing.Spec.ProtocolMappers = append(existing.Spec.ProtocolMappers, missingMappers...)
		// The authenticator reads the secret from this Secret, so the client
		// must use it; a secret set any other way would not match.
		existing.Spec.ClientSecretRef = ptr.To(callerClientSecretRef())
		if err := c.client.Update(ctx, existing); err != nil {
			return fmt.Errorf("update permissionsync caller keycloak client: %w", err)
		}
		log.Info("extended the PermissionSync caller Keycloak client", "keycloakclient", key.Name, "scopes", missing, "mappersAdded", len(missingMappers), "namespace", namespace)
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get permissionsync caller keycloak client: %w", err)
	}

	client := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: permissionsyncconfig.CallerClientResourceName},
		Spec:       permissionSyncCallerClientSpec(scopes),
	}
	if err := c.client.Create(ctx, client); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create permissionsync caller keycloak client: %w", err)
	}
	log.Info("declared the PermissionSync caller Keycloak client", "keycloakclient", key.Name, "clientId", permissionsyncconfig.CallerClientID, "namespace", namespace)
	return nil
}

func permissionSyncCallerClientSpec(optionalScopes []string) neteye.KeycloakClientSpec {
	return neteye.KeycloakClientSpec{
		Realm:       masterRealm,
		ClientID:    permissionsyncconfig.CallerClientID,
		Enabled:     ptr.To(true),
		Name:        permissionSyncCallerName,
		Description: permissionSyncCallerDescription,
		// A technical caller authenticates only through the Client Credentials
		// grant: no browser flow, no password grant, and never public.
		PublicClient:                false,
		StandardFlow:                ptr.To(false),
		DirectAccess:                false,
		AllowClientCredentialsGrant: true,
		OptionalClientScopes:        optionalScopes,
		// The audience is carried by the client itself, not only by the
		// target scopes: a login with no target travels on a token with no
		// permissionsync: scope, and PermissionSync answers it as a targetless
		// no-op only if it is still addressed to it. Without this mapper that
		// token is refused with 401, and the fail-closed authenticator then
		// blocks every login on a client that has no target.
		ProtocolMappers: callerAudienceMappers(),
		ClientSecretRef: ptr.To(callerClientSecretRef()),
		// Orphan: this resource is redeclared whenever it is missing, so
		// deleting it must not destroy a client secret the authenticator still
		// holds.
		DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan,
	}
}

// callerAudienceMappers put both audiences into every access token the caller
// obtains, with or without a target scope: PermissionSync's own, and the
// Permission Provider's, because PermissionSync forwards that very token to the
// provider, which validates it as an independent resource server
// (PermissionSync ADR-0008).
func callerAudienceMappers() []neteye.KeycloakProtocolMapper {
	return []neteye.KeycloakProtocolMapper{
		audienceMapper(permissionsyncconfig.AudienceMapperName, permissionsyncconfig.Audience),
		audienceMapper(permissionsyncconfig.ProviderAudienceMapperName, permissionsyncconfig.ProviderAudience),
	}
}

func audienceMapper(name, audience string) neteye.KeycloakProtocolMapper {
	return neteye.KeycloakProtocolMapper{
		Name:           name,
		Protocol:       openIDConnect,
		ProtocolMapper: "oidc-audience-mapper",
		Config: map[string]string{
			"included.custom.audience":  audience,
			"access.token.claim":        "true",
			"id.token.claim":            "false",
			"introspection.token.claim": "true",
		},
	}
}

func callerClientSecretRef() neteye.NetEyeSecretKeySelector {
	return neteye.NetEyeSecretKeySelector{Name: permissionsyncconfig.CallerClientSecretName, Key: permissionsyncconfig.CallerClientSecretKey}
}

// missingProtocolMappers returns the desired mappers the live list lacks by
// name. A mapper already present is left as it is, as the KeycloakClient
// controller reconciles its configuration.
func missingProtocolMappers(live, desired []neteye.KeycloakProtocolMapper) []neteye.KeycloakProtocolMapper {
	missing := []neteye.KeycloakProtocolMapper{}
	for _, mapper := range desired {
		present := false
		for _, existing := range live {
			if existing.Name == mapper.Name {
				present = true
				break
			}
		}
		if !present {
			missing = append(missing, mapper)
		}
	}
	return missing
}

func missingScopes(assigned, desired []string) []string {
	present := make(map[string]struct{}, len(assigned))
	for _, scope := range assigned {
		present[scope] = struct{}{}
	}
	missing := make([]string, 0, len(desired))
	for _, scope := range desired {
		if _, exists := present[scope]; exists {
			continue
		}
		present[scope] = struct{}{}
		missing = append(missing, scope)
	}
	return missing
}

// IsClientReady reports whether the named KeycloakClient has reached the Ready
// state, meaning the client exists in Keycloak with the declared scopes. It
// does not wait; callers should requeue and check again later.
func (c *Component) IsClientReady(ctx context.Context, namespace, name string) (bool, string, error) {
	client := &neteye.KeycloakClient{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := c.client.Get(ctx, key, client); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("waiting for KeycloakClient %q to be created", name), nil
		}
		return false, "", fmt.Errorf("get keycloak client %q: %w", name, err)
	}
	if client.Status.Status != neteye.ServiceStateReady {
		message := client.Status.Message
		if message == "" {
			message = fmt.Sprintf("waiting for KeycloakClient %q to be ready", name)
		}
		return false, message, nil
	}
	return true, "", nil
}
