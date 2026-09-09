// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

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
	// Unlike DisplayName, DisplayNameHTML is sent unconditionally: it is
	// always-enforced ansible parity (default ""), not an opt-in override
	// like DisplayName or Theme. An empty value here deliberately clears any
	// HTML variant set outside this resource, matching the ansible task this
	// CRD replaces, which always resets it to "".
	desired["displayNameHtml"] = spec.DisplayNameHTML
	desired["rememberMe"] = boolValue(spec.RememberMe, true)

	applyEvents(desired, spec.Events)
	applyBruteForceProtection(desired, spec.BruteForceProtection)
	applyTheme(desired, spec.Theme)

	return desired
}

// applyEvents adds the realm's login and admin action event logging
// settings. Always enforced by the operator; see ADR-0004.
func applyEvents(desired representation, events neteye.KeycloakRealmEvents) {
	desired["eventsEnabled"] = boolValue(events.EventsEnabled, true)
	desired["adminEventsEnabled"] = boolValue(events.AdminEventsEnabled, true)
	desired["adminEventsDetailsEnabled"] = boolValue(events.AdminEventsDetailsEnabled, true)
	desired["eventsExpiration"] = float64Value(events.EventsExpiration, 15552000)
	// adminEventsExpiration has no dedicated realm field in the Keycloak
	// Admin API; the admin console itself stores it as a realm attribute.
	// Realm attributes are Map<String,String> in Keycloak, so the value must
	// be sent as a string or it never compares equal to what GetRealm reads
	// back, causing every reconcile to report drift.
	desired["attributes"] = map[string]any{
		"adminEventsExpiration": strconv.FormatInt(int64Value(events.AdminEventsExpiration, 15552000), 10),
	}
}

// applyBruteForceProtection adds the realm's account-lockout defense
// settings. Always enforced by the operator; see ADR-0004. permanentLockout
// is never exposed as a spec field: see ADR-0004.
func applyBruteForceProtection(desired representation, bfp neteye.KeycloakRealmBruteForceProtection) {
	desired["bruteForceProtected"] = boolValue(bfp.BruteForceProtected, true)
	desired["maxDeltaTimeSeconds"] = float64Value(bfp.MaxDeltaTimeSeconds, 43200)
	desired["maxFailureWaitSeconds"] = float64Value(bfp.MaxFailureWaitSeconds, 900)
	desired["minimumQuickLoginWaitSeconds"] = float64Value(bfp.MinimumQuickLoginWaitSeconds, 60)
	desired["quickLoginCheckMilliSeconds"] = float64Value(bfp.QuickLoginCheckMilliSeconds, 1000)
	desired["failureFactor"] = float64Value(bfp.FailureFactor, 30)
	desired["waitIncrementSeconds"] = float64Value(bfp.WaitIncrementSeconds, 60)
	desired["permanentLockout"] = false
}

// applyTheme adds the realm's theme settings. Unlike Events and
// BruteForceProtection, this is optional and opt-in: theme names are
// installation-specific branding, not a Keycloak-side default worth
// enforcing. A nil theme, or a theme with an empty field, leaves whatever is
// already set in Keycloak untouched (see mergeRepresentation: an absent key
// never overwrites a live one). This also means a theme once set outside
// this resource cannot be cleared back to Keycloak's built-in default
// through this field alone; clearing it here only stops the operator from
// managing it.
func applyTheme(desired representation, theme *neteye.KeycloakRealmTheme) {
	if theme == nil {
		return
	}
	fields := map[string]string{
		"loginTheme":   theme.LoginTheme,
		"adminTheme":   theme.AdminTheme,
		"accountTheme": theme.AccountTheme,
		"emailTheme":   theme.EmailTheme,
	}
	for key, value := range fields {
		if value != "" {
			desired[key] = value
		}
	}
}

// int64Value returns value's contents, or fallback when value is nil.
func int64Value(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}

// float64Value returns value's contents as a float64, or fallback when value
// is nil. Top-level realm numbers are stored as float64 throughout realm
// representations so they compare equal to the values the Keycloak Admin API
// returns, which encoding/json always decodes as float64. Realm attributes
// are the exception: Keycloak types them Map<String,String>, so those go
// through strconv.FormatInt instead (see desiredRealmRepresentation).
func float64Value(value *int64, fallback int64) float64 {
	return float64(int64Value(value, fallback))
}
