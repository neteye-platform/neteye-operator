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

const (
	// TenantAttribute marks the realm of a tenant with the name of the
	// tenant. The operator changes and deletes only a realm that carries the
	// mark, so a realm that someone else created with the same name is never
	// adopted. Accounts cannot carry it, because the user profile of Keycloak
	// drops the attributes that it does not declare: see
	// TenantRealmAdminEmail.
	TenantAttribute = "neteye.cloud/tenant"

	// realmManagementClientSuffix completes the name of the client that
	// Keycloak creates in the master realm for each other realm. Its roles
	// give administration of that realm, and of that realm only.
	realmManagementClientSuffix = "-realm"

	// impersonationRole lets an administrator act as any user of the realm.
	// The tenant realm administrator does not get it.
	impersonationRole = "impersonation"

	// passwordCredential is the only credential type that the tenant realm
	// administrator keeps.
	passwordCredential = "password"
)

// ErrNotTenantOwned reports a realm or an account with the name that a tenant
// uses but without the mark of that tenant.
var ErrNotTenantOwned = errors.New("not marked as owned by the tenant")

// TenantRealmAdminUsername is the master realm account that administers the
// realm of a tenant.
func TenantRealmAdminUsername(realm string) string {
	return realm + "-admin"
}

// TenantRealmAdminEmail is the email of the account that administers the
// realm of a tenant, built like the one of the internal administrator. The
// email is a field that the user profile keeps and that is unique in the
// realm, so the operator recognizes its account by it and never adopts an
// account that someone else created with the same name.
func TenantRealmAdminEmail(realm string) string {
	return TenantRealmAdminUsername(realm) + "@neteyelocal"
}

// IsUnavailable reports whether err means that Keycloak cannot answer now: the
// connection failed or Keycloak answered with a server error. Such errors go
// away when Keycloak is up again.
func IsUnavailable(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= http.StatusInternalServerError
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// EnsureTenantRealm creates the realm of tenant when it is missing, marked
// with the tenant. The realm gets no content of its own: only what Keycloak
// adds to every new realm, such as its signing keys and its built-in clients.
// The KeycloakRealm resource of the tenant manages its settings afterwards.
func EnsureTenantRealm(ctx context.Context, api *AdminAPI, realm, tenant string) (bool, error) {
	if realm == masterRealm {
		return false, fmt.Errorf("the master realm is not a tenant realm")
	}
	live, err := api.GetRealm(ctx, realm)
	if err != nil {
		return false, fmt.Errorf("get keycloak realm %q: %w", realm, err)
	}
	if live != nil {
		if !realmMarkedFor(live, tenant) {
			return false, fmt.Errorf("keycloak realm %q exists and is %w %q", realm, ErrNotTenantOwned, tenant)
		}
		return false, nil
	}
	desired := representation{"realm": realm, "enabled": true, "attributes": map[string]any{TenantAttribute: tenant}}
	if err := api.CreateRealm(ctx, desired); err != nil {
		return false, fmt.Errorf("create keycloak realm %q: %w", realm, err)
	}
	return true, nil
}

// EnsureTenantRealmAdmin makes sure that the master realm has the account
// that administers realm: enabled, with the email of TenantRealmAdminEmail,
// without required actions, which would stop it from getting a token, with
// every role of the management client of realm except impersonation, and with
// no other role, group or credential.
// The password is set when the account is created, and EnsureTenantRealmAdmin
// reports it.
//
// The roles come from the client that Keycloak keeps for realm in the master
// realm, so the account cannot change the master realm, create realms, or
// see the users of other realms. An existing account without that email is
// refused, so roles granted elsewhere never reach a tenant credential.
func EnsureTenantRealmAdmin(ctx context.Context, api *AdminAPI, realm, username, password string) (bool, error) {
	client, err := api.GetClient(ctx, masterRealm, realm+realmManagementClientSuffix)
	if err != nil {
		return false, fmt.Errorf("get the management client of keycloak realm %q: %w", realm, err)
	}
	if client == nil {
		return false, fmt.Errorf("keycloak has no management client for realm %q", realm)
	}
	clientUUID := stringValue(client, "id")

	user, err := api.GetUser(ctx, masterRealm, username)
	if err != nil {
		return false, fmt.Errorf("get keycloak user %q: %w", username, err)
	}
	var userID string
	created := false
	email := TenantRealmAdminEmail(realm)
	if user == nil {
		desired := representation{"username": username, "email": email, "emailVerified": true, "enabled": true, "requiredActions": []any{}}
		userID, err = api.CreateUser(ctx, masterRealm, desired)
		if err != nil {
			return false, fmt.Errorf("create keycloak user %q: %w", username, err)
		}
		if err := api.ResetUserPassword(ctx, masterRealm, userID, password, false); err != nil {
			return false, fmt.Errorf("set the password of keycloak user %q: %w", username, err)
		}
		created = true
	} else {
		if !strings.EqualFold(stringValue(user, "email"), email) {
			return false, fmt.Errorf("keycloak user %q exists and is %w", username, ErrNotTenantOwned)
		}
		userID = stringValue(user, "id")
		enabled, _ := user["enabled"].(bool)
		actions, _ := user["requiredActions"].([]any)
		if !enabled || len(actions) > 0 {
			fixed := mergeRepresentation(user, representation{"enabled": true})
			fixed["requiredActions"] = []any{}
			if err := api.UpdateUser(ctx, masterRealm, userID, fixed); err != nil {
				return false, fmt.Errorf("enable keycloak user %q: %w", username, err)
			}
		}
	}

	roles, err := api.ListClientRoles(ctx, masterRealm, clientUUID)
	if err != nil {
		return false, fmt.Errorf("list the roles of the management client of keycloak realm %q: %w", realm, err)
	}
	mapped, err := api.ListUserClientRoles(ctx, masterRealm, userID, clientUUID)
	if err != nil {
		return false, fmt.Errorf("list the roles of keycloak user %q: %w", username, err)
	}
	var missing []representation
	for _, role := range roles {
		name := stringValue(role, "name")
		if name == impersonationRole {
			continue
		}
		if _, ok := mapped[name]; !ok {
			missing = append(missing, role)
		}
	}
	if len(missing) > 0 {
		if err := api.AddUserClientRoles(ctx, masterRealm, userID, clientUUID, missing); err != nil {
			return false, fmt.Errorf("grant the roles of keycloak realm %q to user %q: %w", realm, username, err)
		}
	}
	if role, ok := mapped[impersonationRole]; ok {
		if err := api.RemoveUserClientRoles(ctx, masterRealm, userID, clientUUID, []representation{role}); err != nil {
			return false, fmt.Errorf("remove the impersonation role from keycloak user %q: %w", username, err)
		}
	}
	if err := removeOtherAccess(ctx, api, userID, username, clientUUID); err != nil {
		return false, err
	}
	return created, nil
}

// removeOtherAccess removes from the account every realm role, every role of
// another client, every group and every credential except the password.
//
//   - A role or a group can give administration outside the tenant realm.
//     Keycloak adds the default role and the default groups of the master
//     realm to a new account, and a person can add others.
//   - The default role gives the account console, where the owner of the
//     account could change its email, which marks the account, or its
//     password, which the Secret holds.
//   - Another credential, such as an OTP, would stop the operator from getting
//     a token with the password.
func removeOtherAccess(ctx context.Context, api *AdminAPI, userID, username, clientUUID string) error {
	mappings, err := api.GetUserRoleMappings(ctx, masterRealm, userID)
	if err != nil {
		return fmt.Errorf("list the role mappings of keycloak user %q: %w", username, err)
	}
	if realmRoles := mappings.RealmMappings; len(realmRoles) > 0 {
		if err := api.RemoveUserRealmRoles(ctx, masterRealm, userID, realmRoles); err != nil {
			return fmt.Errorf("remove the realm roles of keycloak user %q: %w", username, err)
		}
	}
	for _, client := range mappings.ClientMappings {
		if client.ID == clientUUID || len(client.Mappings) == 0 {
			continue
		}
		if err := api.RemoveUserClientRoles(ctx, masterRealm, userID, client.ID, client.Mappings); err != nil {
			return fmt.Errorf("remove the roles of client %q from keycloak user %q: %w", client.ClientID, username, err)
		}
	}
	groups, err := api.ListUserGroups(ctx, masterRealm, userID)
	if err != nil {
		return fmt.Errorf("list the groups of keycloak user %q: %w", username, err)
	}
	for path, groupID := range groups {
		if err := api.RemoveUserFromGroup(ctx, masterRealm, userID, groupID); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove keycloak user %q from group %q: %w", username, path, err)
		}
	}
	credentials, err := api.ListUserCredentials(ctx, masterRealm, userID)
	if err != nil {
		return fmt.Errorf("list the credentials of keycloak user %q: %w", username, err)
	}
	for _, credential := range credentials {
		if stringValue(credential, "type") == passwordCredential {
			continue
		}
		if err := api.DeleteUserCredential(ctx, masterRealm, userID, stringValue(credential, "id")); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove a %s credential from keycloak user %q: %w", stringValue(credential, "type"), username, err)
		}
	}
	return nil
}

// SetTenantRealmAdminPassword sets the password of the account that
// administers realm.
func SetTenantRealmAdminPassword(ctx context.Context, api *AdminAPI, realm, password string) error {
	username := TenantRealmAdminUsername(realm)
	user, err := api.GetUser(ctx, masterRealm, username)
	if err != nil {
		return fmt.Errorf("get keycloak user %q: %w", username, err)
	}
	if user == nil {
		return fmt.Errorf("keycloak user %q does not exist", username)
	}
	if err := api.ResetUserPassword(ctx, masterRealm, stringValue(user, "id"), password, false); err != nil {
		return fmt.Errorf("set the password of keycloak user %q: %w", username, err)
	}
	return nil
}

// TenantRealmExists reports whether the realm of tenant is still in Keycloak.
func TenantRealmExists(ctx context.Context, api *AdminAPI, realm string) (bool, error) {
	live, err := api.GetRealm(ctx, realm)
	if err != nil {
		return false, fmt.Errorf("get keycloak realm %q: %w", realm, err)
	}
	return live != nil, nil
}

// TenantRealmUnmarked reports whether realm exists without the mark of
// tenant.
func TenantRealmUnmarked(ctx context.Context, api *AdminAPI, realm, tenant string) (bool, error) {
	live, err := api.GetRealm(ctx, realm)
	if err != nil {
		return false, fmt.Errorf("get keycloak realm %q: %w", realm, err)
	}
	return live != nil && !realmMarkedFor(live, tenant), nil
}

// DeleteTenantRealm removes the realm when it exists and carries the mark of
// tenant. A realm without the mark is left alone. It refuses the master
// realm.
func DeleteTenantRealm(ctx context.Context, api *AdminAPI, realm, tenant string) error {
	if realm == masterRealm {
		return fmt.Errorf("the master realm is not a tenant realm")
	}
	live, err := api.GetRealm(ctx, realm)
	if err != nil {
		return fmt.Errorf("get keycloak realm %q: %w", realm, err)
	}
	if live == nil || !realmMarkedFor(live, tenant) {
		return nil
	}
	if err := api.DeleteRealm(ctx, realm); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete keycloak realm %q: %w", realm, err)
	}
	return nil
}

// DeleteTenantRealmAdmin removes the master realm account that administers
// realm, when it exists and has the email of TenantRealmAdminEmail.
func DeleteTenantRealmAdmin(ctx context.Context, api *AdminAPI, realm string) error {
	username := TenantRealmAdminUsername(realm)
	user, err := api.GetUser(ctx, masterRealm, username)
	if err != nil {
		return fmt.Errorf("get keycloak user %q: %w", username, err)
	}
	if user == nil || !strings.EqualFold(stringValue(user, "email"), TenantRealmAdminEmail(realm)) {
		return nil
	}
	if err := api.DeleteUser(ctx, masterRealm, stringValue(user, "id")); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete keycloak user %q: %w", username, err)
	}
	return nil
}

// realmMarkedFor reports whether the realm attributes name tenant. Realm
// attributes are single strings.
func realmMarkedFor(realm representation, tenant string) bool {
	attributes, ok := asMap(realm["attributes"])
	if !ok {
		return false
	}
	value, _ := attributes[TenantAttribute].(string)
	return value == tenant
}

// ListClientRoles returns the roles that a client defines.
func (a *AdminAPI) ListClientRoles(ctx context.Context, realm, clientUUID string) ([]representation, error) {
	var roles []representation
	path := fmt.Sprintf("/admin/realms/%s/clients/%s/roles", url.PathEscape(realm), url.PathEscape(clientUUID))
	if err := a.do(ctx, http.MethodGet, path, nil, &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

// userRoleMappings is the direct role mappings of a user.
type userRoleMappings struct {
	RealmMappings  []representation              `json:"realmMappings"`
	ClientMappings map[string]clientRoleMappings `json:"clientMappings"`
}

// clientRoleMappings is the direct roles of one client that a user has.
type clientRoleMappings struct {
	ID       string           `json:"id"`
	ClientID string           `json:"client"`
	Mappings []representation `json:"mappings"`
}

// GetUserRoleMappings returns the realm and client roles mapped directly to a
// user.
func (a *AdminAPI) GetUserRoleMappings(ctx context.Context, realm, userID string) (userRoleMappings, error) {
	var mappings userRoleMappings
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings", url.PathEscape(realm), url.PathEscape(userID))
	err := a.do(ctx, http.MethodGet, path, nil, &mappings)
	return mappings, err
}

// RemoveUserRealmRoles removes realm roles from a user.
func (a *AdminAPI) RemoveUserRealmRoles(ctx context.Context, realm, userID string, roles []representation) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/realm", url.PathEscape(realm), url.PathEscape(userID))
	return a.do(ctx, http.MethodDelete, path, roles, nil)
}

// ListUserCredentials returns the credentials of a user, without their
// secrets.
func (a *AdminAPI) ListUserCredentials(ctx context.Context, realm, userID string) ([]representation, error) {
	var credentials []representation
	path := fmt.Sprintf("/admin/realms/%s/users/%s/credentials", url.PathEscape(realm), url.PathEscape(userID))
	if err := a.do(ctx, http.MethodGet, path, nil, &credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

// DeleteUserCredential removes one credential of a user.
func (a *AdminAPI) DeleteUserCredential(ctx context.Context, realm, userID, credentialID string) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/credentials/%s", url.PathEscape(realm), url.PathEscape(userID), url.PathEscape(credentialID))
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

// RemoveUserFromGroup removes a user from a group.
func (a *AdminAPI) RemoveUserFromGroup(ctx context.Context, realm, userID, groupID string) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/groups/%s", url.PathEscape(realm), url.PathEscape(userID), url.PathEscape(groupID))
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

// RemoveUserClientRoles removes roles of one client from a user.
func (a *AdminAPI) RemoveUserClientRoles(ctx context.Context, realm, userID, clientUUID string, roles []representation) error {
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/clients/%s", url.PathEscape(realm), url.PathEscape(userID), url.PathEscape(clientUUID))
	return a.do(ctx, http.MethodDelete, path, roles, nil)
}
