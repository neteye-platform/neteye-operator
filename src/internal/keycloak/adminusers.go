// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GetUser returns the user representation for username, or nil when the realm
// has no such account. The Admin API search is a substring match, so the exact
// username is checked again on the results.
func (a *AdminAPI) GetUser(ctx context.Context, realm, username string) (representation, error) {
	var users []representation
	path := fmt.Sprintf("/admin/realms/%s/users?username=%s&exact=true", url.PathEscape(realm), url.QueryEscape(username))
	if err := a.do(ctx, http.MethodGet, path, nil, &users); err != nil {
		return nil, err
	}
	for _, user := range users {
		if stringValue(user, "username") == strings.ToLower(username) || stringValue(user, "username") == username {
			return user, nil
		}
	}
	return nil, nil
}

// CreateUser creates an account and returns its internal Keycloak identifier.
func (a *AdminAPI) CreateUser(ctx context.Context, realm string, user representation) (string, error) {
	path := fmt.Sprintf("/admin/realms/%s/users", url.PathEscape(realm))
	if err := a.do(ctx, http.MethodPost, path, user, nil); err != nil {
		return "", err
	}
	// Keycloak returns the identifier in a Location header; re-reading the
	// account keeps this client free of header parsing.
	created, err := a.GetUser(ctx, realm, stringValue(user, "username"))
	if err != nil {
		return "", err
	}
	if created == nil {
		return "", fmt.Errorf("keycloak user %q was not found right after creation", stringValue(user, "username"))
	}
	return stringValue(created, "id"), nil
}

// UpdateUser replaces the user representation identified by userID.
func (a *AdminAPI) UpdateUser(ctx context.Context, realm, userID string, user representation) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s", url.PathEscape(realm), url.PathEscape(userID))
	return a.do(ctx, http.MethodPut, path, user, nil)
}

// DeleteUser removes the account identified by userID.
func (a *AdminAPI) DeleteUser(ctx context.Context, realm, userID string) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s", url.PathEscape(realm), url.PathEscape(userID))
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

// ResetUserPassword sets the account password. A temporary password forces the
// account owner to choose a new one at the next login.
func (a *AdminAPI) ResetUserPassword(ctx context.Context, realm, userID, password string, temporary bool) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/reset-password", url.PathEscape(realm), url.PathEscape(userID))
	credential := representation{"type": "password", "value": password, "temporary": temporary}
	return a.do(ctx, http.MethodPut, path, credential, nil)
}

// GetRealmRole returns the realm role representation for name, or nil when the
// realm has no such role.
func (a *AdminAPI) GetRealmRole(ctx context.Context, realm, name string) (representation, error) {
	var role representation
	path := fmt.Sprintf("/admin/realms/%s/roles/%s", url.PathEscape(realm), url.PathEscape(name))
	if err := a.do(ctx, http.MethodGet, path, nil, &role); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return role, nil
}

// ListUserRealmRoles returns the realm roles mapped to the account, keyed by
// role name.
func (a *AdminAPI) ListUserRealmRoles(ctx context.Context, realm, userID string) (map[string]representation, error) {
	var roles []representation
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/realm", url.PathEscape(realm), url.PathEscape(userID))
	if err := a.do(ctx, http.MethodGet, path, nil, &roles); err != nil {
		return nil, err
	}
	byName := make(map[string]representation, len(roles))
	for _, role := range roles {
		byName[stringValue(role, "name")] = role
	}
	return byName, nil
}

// AddUserRealmRoles maps realm roles to the account.
func (a *AdminAPI) AddUserRealmRoles(ctx context.Context, realm, userID string, roles []representation) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/realm", url.PathEscape(realm), url.PathEscape(userID))
	return a.do(ctx, http.MethodPost, path, roles, nil)
}

// GetGroupByPath returns the group representation at path, for example
// /neteye-admins, or nil when the realm has no such group.
func (a *AdminAPI) GetGroupByPath(ctx context.Context, realm, groupPath string) (representation, error) {
	var group representation
	segments := strings.Split(strings.Trim(groupPath, "/"), "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	path := fmt.Sprintf("/admin/realms/%s/group-by-path/%s", url.PathEscape(realm), strings.Join(segments, "/"))
	if err := a.do(ctx, http.MethodGet, path, nil, &group); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return group, nil
}

// ListUserGroups returns the groups the account belongs to, keyed by group path.
func (a *AdminAPI) ListUserGroups(ctx context.Context, realm, userID string) (map[string]string, error) {
	var groups []representation
	path := fmt.Sprintf("/admin/realms/%s/users/%s/groups", url.PathEscape(realm), url.PathEscape(userID))
	if err := a.do(ctx, http.MethodGet, path, nil, &groups); err != nil {
		return nil, err
	}
	byPath := make(map[string]string, len(groups))
	for _, group := range groups {
		byPath[stringValue(group, "path")] = stringValue(group, "id")
	}
	return byPath, nil
}

// AddUserToGroup adds the account to a group.
func (a *AdminAPI) AddUserToGroup(ctx context.Context, realm, userID, groupID string) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/groups/%s", url.PathEscape(realm), url.PathEscape(userID), url.PathEscape(groupID))
	return a.do(ctx, http.MethodPut, path, nil, nil)
}

// isNotFound reports whether err came from a 404 response. Keycloak answers a
// missing role or group with 404, which callers treat as "absent" rather than
// as a failure.
func isNotFound(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// GetServiceAccountUser returns the user representation Keycloak maintains for
// a client service account, or nil when the client has none.
func (a *AdminAPI) GetServiceAccountUser(ctx context.Context, realm, clientUUID string) (representation, error) {
	var user representation
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/service-account-user", url.PathEscape(realm), url.PathEscape(clientUUID))
	if err := a.do(ctx, http.MethodGet, path, nil, &user); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}

// GetClientRole returns a role defined by a client, or nil when it does not
// exist.
func (a *AdminAPI) GetClientRole(ctx context.Context, realm, clientUUID, name string) (representation, error) {
	var role representation
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/roles/%s", url.PathEscape(realm), url.PathEscape(clientUUID), url.PathEscape(name))
	if err := a.do(ctx, http.MethodGet, path, nil, &role); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return role, nil
}

// ListUserClientRoles returns the roles of one client mapped to a user, keyed
// by role name.
func (a *AdminAPI) ListUserClientRoles(ctx context.Context, realm, userID, clientUUID string) (map[string]representation, error) {
	var roles []representation
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/clients/%s", url.PathEscape(realm), url.PathEscape(userID), url.PathEscape(clientUUID))
	if err := a.do(ctx, http.MethodGet, path, nil, &roles); err != nil {
		return nil, err
	}
	byName := make(map[string]representation, len(roles))
	for _, role := range roles {
		byName[stringValue(role, "name")] = role
	}
	return byName, nil
}

// AddUserClientRoles maps roles of one client to a user.
func (a *AdminAPI) AddUserClientRoles(ctx context.Context, realm, userID, clientUUID string, roles []representation) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/clients/%s", url.PathEscape(realm), url.PathEscape(userID), url.PathEscape(clientUUID))
	return a.do(ctx, http.MethodPost, path, roles, nil)
}
