// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"
	"reflect"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// UserCredential is the password material a reconciliation may apply. The
// credential is create-only: it is written when the account is created and
// afterwards only when Rotate is set, so a password owned by a person is never
// silently overwritten.
type UserCredential struct {
	// Password is the password to set. An empty value leaves credentials alone.
	Password string
	// Temporary forces the account owner to choose a new password at next login.
	Temporary bool
	// Rotate applies the password to an account that already exists.
	Rotate bool
}

// UserSyncResult reports what reconciling a KeycloakUser did.
type UserSyncResult struct {
	// UserID is the internal Keycloak identifier of the account.
	UserID string
	// Created is true when the account did not exist and was created.
	Created bool
	// Adopted is true when the account already existed and was taken over as it
	// was found, credential included.
	Adopted bool
	// Updated is true when remote state drifted and was corrected.
	Updated bool
	// PasswordSet is true when the password was written to Keycloak.
	PasswordSet bool
}

// ReconcileUser makes the Keycloak account match spec. Only the fields the spec
// declares are reconciled: profile fields left empty stay under the account
// owner's control, and nil role or group lists mean "not managed here".
//
// An account that already exists is adopted rather than rejected, which is what
// makes the type usable against a pre-existing Keycloak database.
func ReconcileUser(ctx context.Context, api *AdminAPI, spec neteye.KeycloakUserSpec, credential UserCredential) (UserSyncResult, error) {
	realm := userRealm(spec)
	result := UserSyncResult{}

	desired := desiredUserRepresentation(spec)
	live, err := api.GetUser(ctx, realm, spec.Username)
	if err != nil {
		return result, fmt.Errorf("get keycloak user %q: %w", spec.Username, err)
	}

	if live == nil {
		userID, err := api.CreateUser(ctx, realm, desired)
		if err != nil {
			return result, fmt.Errorf("create keycloak user %q: %w", spec.Username, err)
		}
		result.UserID, result.Created = userID, true
	} else {
		result.UserID, result.Adopted = stringValue(live, "id"), true
		merged := mergeRepresentation(live, desired)
		if !reflect.DeepEqual(map[string]any(live), map[string]any(merged)) {
			if err := api.UpdateUser(ctx, realm, result.UserID, merged); err != nil {
				return result, fmt.Errorf("update keycloak user %q: %w", spec.Username, err)
			}
			result.Updated = true
		}
	}

	if credential.Password != "" && (result.Created || credential.Rotate) {
		if err := api.ResetUserPassword(ctx, realm, result.UserID, credential.Password, credential.Temporary); err != nil {
			return result, fmt.Errorf("set password of keycloak user %q: %w", spec.Username, err)
		}
		result.PasswordSet = true
	}

	rolesChanged, err := reconcileRealmRoles(ctx, api, realm, result.UserID, spec.RealmRoles)
	if err != nil {
		return result, err
	}
	groupsChanged, err := reconcileUserGroups(ctx, api, realm, result.UserID, spec.Groups)
	if err != nil {
		return result, err
	}
	if rolesChanged || groupsChanged {
		result.Updated = true
	}
	return result, nil
}

// DeleteUser removes the Keycloak account declared by spec. It is a no-op when
// the account is already gone, so it is safe to call from a finalizer.
func DeleteUser(ctx context.Context, api *AdminAPI, spec neteye.KeycloakUserSpec) error {
	realm := userRealm(spec)
	live, err := api.GetUser(ctx, realm, spec.Username)
	if err != nil {
		return fmt.Errorf("get keycloak user %q: %w", spec.Username, err)
	}
	if live == nil {
		return nil
	}
	if err := api.DeleteUser(ctx, realm, stringValue(live, "id")); err != nil {
		return fmt.Errorf("delete keycloak user %q: %w", spec.Username, err)
	}
	return nil
}

// masterRealm is the realm holding the administrative accounts.
const masterRealm = "master"

func userRealm(spec neteye.KeycloakUserSpec) string {
	if spec.Realm == "" {
		return masterRealm
	}
	return spec.Realm
}

// desiredUserRepresentation renders the spec fields the operator manages. A
// profile field left empty is never sent, so it stays as the account owner set
// it instead of being cleared on every pass.
func desiredUserRepresentation(spec neteye.KeycloakUserSpec) representation {
	desired := representation{
		"username": spec.Username,
		"enabled":  boolValue(spec.Enabled, true),
	}
	if spec.Email != "" {
		desired["email"] = spec.Email
		desired["emailVerified"] = spec.EmailVerified
	}
	if spec.FirstName != "" {
		desired["firstName"] = spec.FirstName
	}
	if spec.LastName != "" {
		desired["lastName"] = spec.LastName
	}
	return desired
}

func reconcileRealmRoles(ctx context.Context, api *AdminAPI, realm, userID string, desired []string) (bool, error) {
	if desired == nil {
		return false, nil
	}
	assigned, err := api.ListUserRealmRoles(ctx, realm, userID)
	if err != nil {
		return false, fmt.Errorf("list realm roles: %w", err)
	}

	changed := false
	missing := make([]representation, 0, len(desired))
	for _, name := range desired {
		if _, ok := assigned[name]; ok {
			continue
		}
		role, err := api.GetRealmRole(ctx, realm, name)
		if err != nil {
			return changed, fmt.Errorf("get realm role %q: %w", name, err)
		}
		if role == nil {
			return changed, fmt.Errorf("realm role %q does not exist in realm %q", name, realm)
		}
		missing = append(missing, representation{"id": stringValue(role, "id"), "name": stringValue(role, "name")})
	}
	if len(missing) > 0 {
		if err := api.AddUserRealmRoles(ctx, realm, userID, missing); err != nil {
			return changed, fmt.Errorf("add realm roles: %w", err)
		}
		changed = true
	}

	// Roles the spec does not declare are left mapped: an adopted account keeps
	// whatever an administrator granted it outside this resource.
	return changed, nil
}

func reconcileUserGroups(ctx context.Context, api *AdminAPI, realm, userID string, desired []string) (bool, error) {
	if desired == nil {
		return false, nil
	}
	assigned, err := api.ListUserGroups(ctx, realm, userID)
	if err != nil {
		return false, fmt.Errorf("list user groups: %w", err)
	}

	changed := false
	for _, groupPath := range desired {
		if _, ok := assigned[groupPath]; ok {
			continue
		}
		group, err := api.GetGroupByPath(ctx, realm, groupPath)
		if err != nil {
			return changed, fmt.Errorf("get group %q: %w", groupPath, err)
		}
		if group == nil {
			return changed, fmt.Errorf("group %q does not exist in realm %q", groupPath, realm)
		}
		if err := api.AddUserToGroup(ctx, realm, userID, stringValue(group, "id")); err != nil {
			return changed, fmt.Errorf("add user to group %q: %w", groupPath, err)
		}
		changed = true
	}

	// Group memberships outside the declared list are kept, as for realm roles.
	return changed, nil
}
