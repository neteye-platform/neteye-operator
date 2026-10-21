// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
)

const (
	// clientScopeConsentAttribute and clientScopeTokenScopeAttribute are the
	// two client-scope attributes that decide whether the scope reaches the
	// token's scope claim and whether it is shown on a consent screen.
	clientScopeTokenScopeAttribute = "include.in.token.scope"
	clientScopeConsentAttribute    = "display.on.consent.screen"
)

// GetRealmClientScope returns the full representation of a client scope.
// ListRealmClientScopes only yields names and identifiers, which is not enough
// to tell whether an adopted scope still carries the attributes it needs.
func (a *AdminAPI) GetRealmClientScope(ctx context.Context, realm, scopeID string) (representation, error) {
	var scope representation
	path := fmt.Sprintf("/admin/realms/%s/client-scopes/%s", url.PathEscape(realm), url.PathEscape(scopeID))
	if err := a.do(ctx, http.MethodGet, path, nil, &scope); err != nil {
		return nil, err
	}
	return scope, nil
}

// UpdateRealmClientScope replaces an existing client scope representation.
func (a *AdminAPI) UpdateRealmClientScope(ctx context.Context, realm, scopeID string, scope representation) error {
	path := fmt.Sprintf("/admin/realms/%s/client-scopes/%s", url.PathEscape(realm), url.PathEscape(scopeID))
	return a.do(ctx, http.MethodPut, path, scope, nil)
}

// CreateRealmClientScope creates a client scope in the realm.
func (a *AdminAPI) CreateRealmClientScope(ctx context.Context, realm string, scope representation) error {
	path := fmt.Sprintf("/admin/realms/%s/client-scopes", url.PathEscape(realm))
	return a.do(ctx, http.MethodPost, path, scope, nil)
}

// ListClientScopeProtocolMappers returns the protocol mappers attached to a
// client scope.
func (a *AdminAPI) ListClientScopeProtocolMappers(ctx context.Context, realm, scopeID string) ([]representation, error) {
	var mappers []representation
	path := fmt.Sprintf("/admin/realms/%s/client-scopes/%s/protocol-mappers/models", url.PathEscape(realm), url.PathEscape(scopeID))
	if err := a.do(ctx, http.MethodGet, path, nil, &mappers); err != nil {
		return nil, err
	}
	return mappers, nil
}

// CreateClientScopeProtocolMapper attaches a protocol mapper to a client scope.
func (a *AdminAPI) CreateClientScopeProtocolMapper(ctx context.Context, realm, scopeID string, mapper representation) error {
	path := fmt.Sprintf("/admin/realms/%s/client-scopes/%s/protocol-mappers/models", url.PathEscape(realm), url.PathEscape(scopeID))
	return a.do(ctx, http.MethodPost, path, mapper, nil)
}

// UpdateClientScopeProtocolMapper replaces a protocol mapper on a client scope.
func (a *AdminAPI) UpdateClientScopeProtocolMapper(ctx context.Context, realm, scopeID, mapperID string, mapper representation) error {
	path := fmt.Sprintf("/admin/realms/%s/client-scopes/%s/protocol-mappers/models/%s", url.PathEscape(realm), url.PathEscape(scopeID), url.PathEscape(mapperID))
	return a.do(ctx, http.MethodPut, path, mapper, nil)
}

// ensureAudienceClientScope makes the realm hold a client scope named name
// that adds audience to the access tokens it is requested for. The scope is
// created when missing, and both the scope itself and its audience mapper are
// reconciled afterwards, so an adopted or externally edited scope is brought
// back to what the contract needs; other mappers on the scope are left
// untouched, like the undeclared mappers on a KeycloakClient.
//
// Reconciling the scope and not only the mapper matters: a scope that exists
// with include.in.token.scope disabled never reaches the token's scope claim,
// and the receiver then treats every request for that target as a targetless
// no-op. That failure is silent on both sides, so it must not survive a
// reconciliation that reports the component Ready.
func ensureAudienceClientScope(ctx context.Context, api *AdminAPI, realm, name, description, audience, mapperName string) error {
	scopes, err := api.ListRealmClientScopes(ctx, realm)
	if err != nil {
		return fmt.Errorf("list client scopes in realm %q: %w", realm, err)
	}
	scopeID, exists := scopes[name]
	if !exists {
		if err := api.CreateRealmClientScope(ctx, realm, desiredClientScopeRepresentation(name, description)); err != nil {
			return fmt.Errorf("create client scope %q in realm %q: %w", name, realm, err)
		}
		scopes, err = api.ListRealmClientScopes(ctx, realm)
		if err != nil {
			return fmt.Errorf("list client scopes in realm %q: %w", realm, err)
		}
		if scopeID, exists = scopes[name]; !exists {
			return fmt.Errorf("client scope %q was not found in realm %q right after creation", name, realm)
		}
	} else if err := reconcileClientScope(ctx, api, realm, scopeID, name, description); err != nil {
		return err
	}
	return reconcileClientScopeAudienceMapper(ctx, api, realm, scopeID, mapperName, audience)
}

// reconcileClientScope restores the attributes and protocol the contract needs
// on a scope the operator adopted. Keys the operator does not declare are
// preserved by mergeRepresentation, so an administrator's own additions on the
// scope survive.
func reconcileClientScope(ctx context.Context, api *AdminAPI, realm, scopeID, name, description string) error {
	live, err := api.GetRealmClientScope(ctx, realm, scopeID)
	if err != nil {
		return fmt.Errorf("get client scope %q in realm %q: %w", name, realm, err)
	}
	merged := mergeRepresentation(live, desiredClientScopeRepresentation(name, description))
	if reflect.DeepEqual(map[string]any(live), map[string]any(merged)) {
		return nil
	}
	if err := api.UpdateRealmClientScope(ctx, realm, scopeID, merged); err != nil {
		return fmt.Errorf("update client scope %q in realm %q: %w", name, realm, err)
	}
	return nil
}

// desiredClientScopeRepresentation describes an optional, token-scope-bearing
// client scope. It must reach the token's scope claim: the receiver authorizes
// on that claim, not on the scope assignment.
func desiredClientScopeRepresentation(name, description string) representation {
	return representation{
		"name":        name,
		"description": description,
		"protocol":    openIDConnect,
		"attributes": map[string]any{
			clientScopeTokenScopeAttribute: "true",
			clientScopeConsentAttribute:    "false",
		},
	}
}

func reconcileClientScopeAudienceMapper(ctx context.Context, api *AdminAPI, realm, scopeID, mapperName, audience string) error {
	mappers, err := api.ListClientScopeProtocolMappers(ctx, realm, scopeID)
	if err != nil {
		return fmt.Errorf("list protocol mappers of client scope %q: %w", scopeID, err)
	}
	desired := desiredAudienceMapperRepresentation(mapperName, audience)
	for _, live := range mappers {
		if stringValue(live, "name") != mapperName {
			continue
		}
		merged := mergeRepresentation(live, desired)
		if reflect.DeepEqual(map[string]any(live), map[string]any(merged)) {
			return nil
		}
		if err := api.UpdateClientScopeProtocolMapper(ctx, realm, scopeID, stringValue(live, "id"), merged); err != nil {
			return fmt.Errorf("update protocol mapper %q of client scope %q: %w", mapperName, scopeID, err)
		}
		return nil
	}
	if err := api.CreateClientScopeProtocolMapper(ctx, realm, scopeID, desired); err != nil {
		return fmt.Errorf("create protocol mapper %q on client scope %q: %w", mapperName, scopeID, err)
	}
	return nil
}

// desiredAudienceMapperRepresentation puts the audience in the access token
// only. The receiver verifies a Client Credentials access token, and no ID
// token is involved.
func desiredAudienceMapperRepresentation(name, audience string) representation {
	return representation{
		"name":           name,
		"protocol":       openIDConnect,
		"protocolMapper": "oidc-audience-mapper",
		"config": map[string]any{
			"included.custom.audience":  audience,
			"access.token.claim":        "true",
			"id.token.claim":            "false",
			"introspection.token.claim": "true",
		},
	}
}
