// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"
	"reflect"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// Client scope kinds accepted by the Admin API client scope endpoints.
const (
	defaultScopeKind  = "default"
	optionalScopeKind = "optional"
	openIDConnect     = "openid-connect"
)

// ClientSyncResult reports what reconciling a KeycloakClient did.
type ClientSyncResult struct {
	// UUID is the internal Keycloak identifier of the client.
	UUID string
	// Created is true when the client did not exist and was created.
	Created bool
	// Updated is true when remote state drifted and was corrected.
	Updated bool
}

// ReconcileClient makes the Keycloak client match spec and returns to the
// desired state anything that drifted since the last pass. Keycloak offers no
// watch mechanism, so callers invoke this on every reconciliation instead of
// reacting to remote events.
//
// Protocol mappers and client scopes are only reconciled when the matching spec
// field is set: a nil list means "not managed here" and leaves remote state
// alone, while a non-nil list is authoritative and extra entries are removed.
func ReconcileClient(ctx context.Context, api *AdminAPI, spec neteye.KeycloakClientSpec, clientSecret string) (ClientSyncResult, error) {
	realm := clientRealm(spec)
	result := ClientSyncResult{}

	desired := desiredClientRepresentation(spec, clientSecret)
	live, err := api.GetClient(ctx, realm, spec.ClientID)
	if err != nil {
		return result, fmt.Errorf("get keycloak client %q: %w", spec.ClientID, err)
	}

	if live == nil {
		uuid, err := api.CreateClient(ctx, realm, desired)
		if err != nil {
			return result, fmt.Errorf("create keycloak client %q: %w", spec.ClientID, err)
		}
		result.UUID, result.Created = uuid, true
	} else {
		result.UUID = stringValue(live, "id")
		merged := mergeRepresentation(live, desired)
		if !reflect.DeepEqual(map[string]any(live), map[string]any(merged)) {
			if err := api.UpdateClient(ctx, realm, result.UUID, merged); err != nil {
				return result, fmt.Errorf("update keycloak client %q: %w", spec.ClientID, err)
			}
			result.Updated = true
		}
	}

	serviceAccountChanged, err := reconcileServiceAccountRoles(ctx, api, realm, result.UUID, spec)
	if err != nil {
		return result, err
	}
	mappersChanged, err := reconcileProtocolMappers(ctx, api, realm, result.UUID, spec.ProtocolMappers)
	if err != nil {
		return result, err
	}
	defaultsChanged, err := reconcileClientScopes(ctx, api, realm, result.UUID, defaultScopeKind, spec.DefaultClientScopes)
	if err != nil {
		return result, err
	}
	optionalsChanged, err := reconcileClientScopes(ctx, api, realm, result.UUID, optionalScopeKind, spec.OptionalClientScopes)
	if err != nil {
		return result, err
	}
	if serviceAccountChanged || mappersChanged || defaultsChanged || optionalsChanged {
		result.Updated = true
	}
	return result, nil
}

// DeleteClient removes the Keycloak client declared by spec. It is a no-op when
// the client is already gone, so it is safe to call from a finalizer.
func DeleteClient(ctx context.Context, api *AdminAPI, spec neteye.KeycloakClientSpec) error {
	realm := clientRealm(spec)
	live, err := api.GetClient(ctx, realm, spec.ClientID)
	if err != nil {
		return fmt.Errorf("get keycloak client %q: %w", spec.ClientID, err)
	}
	if live == nil {
		return nil
	}
	if err := api.DeleteClient(ctx, realm, stringValue(live, "id")); err != nil {
		return fmt.Errorf("delete keycloak client %q: %w", spec.ClientID, err)
	}
	return nil
}

func clientRealm(spec neteye.KeycloakClientSpec) string {
	if spec.Realm == "" {
		return "master"
	}
	return spec.Realm
}

// desiredClientRepresentation renders the spec fields the operator manages.
// Every value is a plain JSON type so it compares equal to what the Admin API
// returns.
func desiredClientRepresentation(spec neteye.KeycloakClientSpec, clientSecret string) representation {
	desired := representation{
		"clientId":                  spec.ClientID,
		"protocol":                  openIDConnect,
		"enabled":                   boolValue(spec.Enabled, true),
		"publicClient":              spec.PublicClient,
		"standardFlowEnabled":       boolValue(spec.StandardFlow, true),
		"directAccessGrantsEnabled": spec.DirectAccess,
		"serviceAccountsEnabled":    spec.ServiceAccount != nil && spec.ServiceAccount.Enabled,
	}
	if spec.Name != "" {
		desired["name"] = spec.Name
	}
	if spec.Description != "" {
		desired["description"] = spec.Description
	}
	if spec.RootURL != "" {
		desired["rootUrl"] = spec.RootURL
	}
	if spec.RedirectUris != nil {
		desired["redirectUris"] = anySlice(spec.RedirectUris)
	}
	if spec.WebOrigins != nil {
		desired["webOrigins"] = anySlice(spec.WebOrigins)
	}
	if !spec.PublicClient && clientSecret != "" {
		desired["secret"] = clientSecret
	}
	return desired
}

// mergeRepresentation copies live and overwrites the keys the operator manages,
// so fields Keycloak owns or an administrator set outside the spec survive the
// update.
//
// Nested objects are merged key by key rather than replaced. Keycloak fills in
// its own entries inside them — a protocol mapper gains
// introspection.token.claim, for instance — and replacing the whole object would
// drop those, only for Keycloak to add them back: every reconciliation would
// then find drift and rewrite the resource forever.
func mergeRepresentation(live, desired representation) representation {
	merged := make(representation, len(live)+len(desired))
	for key, value := range live {
		merged[key] = value
	}
	for key, value := range desired {
		if nested, ok := asMap(value); ok {
			if current, ok := asMap(merged[key]); ok {
				merged[key] = map[string]any(mergeRepresentation(current, nested))
				continue
			}
		}
		merged[key] = value
	}
	return merged
}

// asMap reports whether value is a JSON object, in either of the two shapes the
// Admin API and the operator produce for one.
func asMap(value any) (representation, bool) {
	switch typed := value.(type) {
	case representation:
		return typed, true
	case map[string]any:
		return representation(typed), true
	default:
		return nil, false
	}
}

func reconcileProtocolMappers(ctx context.Context, api *AdminAPI, realm, uuid string, desired []neteye.KeycloakProtocolMapper) (bool, error) {
	if desired == nil {
		return false, nil
	}
	live, err := api.ListProtocolMappers(ctx, realm, uuid)
	if err != nil {
		return false, fmt.Errorf("list protocol mappers: %w", err)
	}
	liveByName := make(map[string]representation, len(live))
	for _, mapper := range live {
		liveByName[stringValue(mapper, "name")] = mapper
	}

	changed := false
	for _, mapper := range desired {
		want := desiredMapperRepresentation(mapper)
		current, found := liveByName[mapper.Name]
		if !found {
			if err := api.CreateProtocolMapper(ctx, realm, uuid, want); err != nil {
				return changed, fmt.Errorf("create protocol mapper %q: %w", mapper.Name, err)
			}
			changed = true
			continue
		}
		merged := mergeRepresentation(current, want)
		if reflect.DeepEqual(map[string]any(current), map[string]any(merged)) {
			continue
		}
		if err := api.UpdateProtocolMapper(ctx, realm, uuid, stringValue(current, "id"), merged); err != nil {
			return changed, fmt.Errorf("update protocol mapper %q: %w", mapper.Name, err)
		}
		changed = true
	}

	// Mappers the spec does not declare are left alone: the operator only owns
	// what the resource names, so a client adopted from an existing Keycloak
	// keeps the mappers someone else configured on it.
	return changed, nil
}

func desiredMapperRepresentation(mapper neteye.KeycloakProtocolMapper) representation {
	protocol := mapper.Protocol
	if protocol == "" {
		protocol = openIDConnect
	}
	config := make(map[string]any, len(mapper.Config))
	for key, value := range mapper.Config {
		config[key] = value
	}
	return representation{
		"name":           mapper.Name,
		"protocol":       protocol,
		"protocolMapper": mapper.ProtocolMapper,
		"config":         config,
	}
}

func reconcileClientScopes(ctx context.Context, api *AdminAPI, realm, uuid, kind string, desired []string) (bool, error) {
	if desired == nil {
		return false, nil
	}
	assigned, err := api.ListClientScopes(ctx, realm, uuid, kind)
	if err != nil {
		return false, fmt.Errorf("list %s client scopes: %w", kind, err)
	}
	realmScopes, err := api.ListRealmClientScopes(ctx, realm)
	if err != nil {
		return false, fmt.Errorf("list realm client scopes: %w", err)
	}

	changed := false
	for _, name := range desired {
		if _, ok := assigned[name]; ok {
			continue
		}
		scopeID, ok := realmScopes[name]
		if !ok {
			return changed, fmt.Errorf("client scope %q does not exist in realm %q", name, realm)
		}
		if err := api.AddClientScope(ctx, realm, uuid, kind, scopeID); err != nil {
			return changed, fmt.Errorf("add %s client scope %q: %w", kind, name, err)
		}
		changed = true
	}

	// Scopes outside the declared list stay assigned, for the same reason the
	// undeclared protocol mappers do.
	return changed, nil
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func anySlice(values []string) []any {
	converted := make([]any, 0, len(values))
	for _, value := range values {
		converted = append(converted, value)
	}
	return converted
}

// reconcileServiceAccountRoles assigns client roles to the user Keycloak keeps
// for the client service account, which is how a client is granted permissions
// on another client's API — the NetEye client reading users and groups from
// master-realm, for instance.
//
// Roles are only ever granted, never revoked, so adopting a service account
// never strips permissions granted outside this resource.
func reconcileServiceAccountRoles(ctx context.Context, api *AdminAPI, realm, uuid string, spec neteye.KeycloakClientSpec) (bool, error) {
	if spec.ServiceAccount == nil || spec.ServiceAccount.ClientRoles == nil {
		return false, nil
	}
	if !spec.ServiceAccount.Enabled {
		return false, fmt.Errorf("client %q declares service account roles but its service account is disabled", spec.ClientID)
	}

	account, err := api.GetServiceAccountUser(ctx, realm, uuid)
	if err != nil {
		return false, fmt.Errorf("get service account user of client %q: %w", spec.ClientID, err)
	}
	if account == nil {
		return false, fmt.Errorf("client %q has no service account user", spec.ClientID)
	}
	accountID := stringValue(account, "id")

	changed := false
	for clientID, roles := range spec.ServiceAccount.ClientRoles {
		roleOwner, err := api.GetClient(ctx, realm, clientID)
		if err != nil {
			return changed, fmt.Errorf("get client %q owning service account roles: %w", clientID, err)
		}
		if roleOwner == nil {
			return changed, fmt.Errorf("client %q does not exist in realm %q", clientID, realm)
		}
		ownerUUID := stringValue(roleOwner, "id")

		assigned, err := api.ListUserClientRoles(ctx, realm, accountID, ownerUUID)
		if err != nil {
			return changed, fmt.Errorf("list %q roles of the service account: %w", clientID, err)
		}

		missing := make([]representation, 0, len(roles))
		for _, name := range roles {
			if _, ok := assigned[name]; ok {
				continue
			}
			role, err := api.GetClientRole(ctx, realm, ownerUUID, name)
			if err != nil {
				return changed, fmt.Errorf("get role %q of client %q: %w", name, clientID, err)
			}
			if role == nil {
				return changed, fmt.Errorf("client %q has no role %q in realm %q", clientID, name, realm)
			}
			missing = append(missing, representation{"id": stringValue(role, "id"), "name": stringValue(role, "name")})
		}
		if len(missing) > 0 {
			if err := api.AddUserClientRoles(ctx, realm, accountID, ownerUUID, missing); err != nil {
				return changed, fmt.Errorf("add %q roles to the service account: %w", clientID, err)
			}
			changed = true
		}

		// Roles granted outside this spec are kept, so adopting a service account
		// never strips permissions the platform does not know about.
	}
	return changed, nil
}
