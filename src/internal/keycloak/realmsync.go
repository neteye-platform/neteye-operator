// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"
	"reflect"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// RealmSyncResult reports what reconciling a KeycloakRealm did.
type RealmSyncResult struct {
	Created bool
	Updated bool
}

// ReconcileRealm makes the Keycloak realm match spec.
func ReconcileRealm(ctx context.Context, api *AdminAPI, spec neteye.KeycloakRealmSpec) (RealmSyncResult, error) {
	result := RealmSyncResult{}
	desired := desiredRealmRepresentation(spec)
	live, err := api.GetRealm(ctx, spec.Realm)
	if err != nil {
		return result, fmt.Errorf("get keycloak realm %q: %w", spec.Realm, err)
	}
	if live == nil {
		if err := api.CreateRealm(ctx, desired); err != nil {
			return result, fmt.Errorf("create keycloak realm %q: %w", spec.Realm, err)
		}
		result.Created = true
		return result, nil
	}
	merged := mergeRepresentation(live, desired)
	if reflect.DeepEqual(map[string]any(live), map[string]any(merged)) {
		return result, nil
	}
	if err := api.UpdateRealm(ctx, spec.Realm, merged); err != nil {
		return result, fmt.Errorf("update keycloak realm %q: %w", spec.Realm, err)
	}
	result.Updated = true
	return result, nil
}

// DeleteRealm removes the realm declared by spec. The master realm is never
// removed because Keycloak requires it for administration.
func DeleteRealm(ctx context.Context, api *AdminAPI, spec neteye.KeycloakRealmSpec) error {
	if spec.Realm == masterRealm {
		return nil
	}
	live, err := api.GetRealm(ctx, spec.Realm)
	if err != nil {
		return fmt.Errorf("get keycloak realm %q: %w", spec.Realm, err)
	}
	if live == nil {
		return nil
	}
	if err := api.DeleteRealm(ctx, spec.Realm); err != nil {
		return fmt.Errorf("delete keycloak realm %q: %w", spec.Realm, err)
	}
	return nil
}

func desiredRealmRepresentation(spec neteye.KeycloakRealmSpec) representation {
	desired := representation{"realm": spec.Realm, "enabled": boolValue(spec.Enabled, true)}
	if spec.DisplayName != "" {
		desired["displayName"] = spec.DisplayName
	}
	return desired
}
