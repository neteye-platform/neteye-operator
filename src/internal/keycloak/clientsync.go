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
	if mappersChanged || defaultsChanged || optionalsChanged {
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
		"serviceAccountsEnabled":    spec.ServiceAccountEnabled,
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
func mergeRepresentation(live, desired representation) representation {
	merged := make(representation, len(live)+len(desired))
	for key, value := range live {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
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
	declared := make(map[string]struct{}, len(desired))
	for _, mapper := range desired {
		declared[mapper.Name] = struct{}{}
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

	for name, mapper := range liveByName {
		if _, ok := declared[name]; ok {
			continue
		}
		if err := api.DeleteProtocolMapper(ctx, realm, uuid, stringValue(mapper, "id")); err != nil {
			return changed, fmt.Errorf("delete protocol mapper %q: %w", name, err)
		}
		changed = true
	}
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
	declared := make(map[string]struct{}, len(desired))
	for _, name := range desired {
		declared[name] = struct{}{}
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

	for name, scopeID := range assigned {
		if _, ok := declared[name]; ok {
			continue
		}
		if err := api.RemoveClientScope(ctx, realm, uuid, kind, scopeID); err != nil {
			return changed, fmt.Errorf("remove %s client scope %q: %w", kind, name, err)
		}
		changed = true
	}
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
