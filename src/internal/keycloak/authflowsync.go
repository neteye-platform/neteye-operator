// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

type FlowSyncResult struct {
	Created bool
	Updated bool
}

// flowExecution is a runtime-only mirror of KeycloakAuthFlowExecution that
// can nest arbitrarily, unlike the API type which caps nesting at 3 fixed
// levels for CRD schema generation. Reconciliation recurses freely over this
// type; toRuntimeExecutions and friends build it once from the depth-capped
// API types so the reconciliation logic below is written once, not per level.
type flowExecution struct {
	Alias         string
	Requirement   string
	Authenticator string
	Config        *neteye.KeycloakAuthFlowConfig
	Flow          *flowSpec
}

type flowSpec struct {
	Alias      string
	Provider   string
	Executions []flowExecution
}

func toRuntimeExecutions(execs []neteye.KeycloakAuthFlowExecution) []flowExecution {
	out := make([]flowExecution, len(execs))
	for i, e := range execs {
		out[i] = flowExecution{Alias: e.Alias, Requirement: e.Requirement, Authenticator: e.Authenticator, Config: e.Config, Flow: toRuntimeSpecL2(e.Flow)}
	}
	return out
}

func toRuntimeSpecL2(spec *neteye.KeycloakAuthFlowExecutionSpecL2) *flowSpec {
	if spec == nil {
		return nil
	}
	execs := make([]flowExecution, len(spec.Executions))
	for i, e := range spec.Executions {
		execs[i] = flowExecution{Alias: e.Alias, Requirement: e.Requirement, Authenticator: e.Authenticator, Config: e.Config, Flow: toRuntimeSpecL3(e.Flow)}
	}
	return &flowSpec{Alias: spec.Alias, Provider: spec.Provider, Executions: execs}
}

func toRuntimeSpecL3(spec *neteye.KeycloakAuthFlowExecutionSpecL3) *flowSpec {
	if spec == nil {
		return nil
	}
	execs := make([]flowExecution, len(spec.Executions))
	for i, e := range spec.Executions {
		// Level 3 is the deepest allowed level: its executions are leaves and
		// never carry a further nested Flow.
		execs[i] = flowExecution{Alias: e.Alias, Requirement: e.Requirement, Authenticator: e.Authenticator, Config: e.Config}
	}
	return &flowSpec{Alias: spec.Alias, Provider: spec.Provider, Executions: execs}
}

func ReconcileFlow(ctx context.Context, api *AdminAPI, spec neteye.KeycloakAuthFlowSpec) (FlowSyncResult, error) {
	result := FlowSyncResult{}
	rootCreated, err := ensureRootFlow(ctx, api, flowRealm(spec), spec.Alias, spec.Provider)
	if err != nil {
		return result, err
	}
	created, updated, err := reconcileFlowExecutions(ctx, api, flowRealm(spec), spec.Alias, toRuntimeExecutions(spec.Executions))
	if err != nil {
		result.Created, result.Updated = rootCreated || created, updated
		return result, err
	}
	boundChanged, err := reconcileBindings(ctx, api, flowRealm(spec), spec.Alias, spec.Bindings)
	result.Created, result.Updated = rootCreated || created, updated || boundChanged
	return result, err
}

// realmFlowFields maps a KeycloakAuthFlowSpec binding purpose to the realm
// representation field Keycloak uses to record it.
var realmFlowFields = map[string]string{
	"browser":              "browserFlow",
	"registration":         "registrationFlow",
	"directGrant":          "directGrantFlow",
	"resetCredentials":     "resetCredentialsFlow",
	"clientAuthentication": "clientAuthenticationFlow",
	"dockerAuthentication": "dockerAuthenticationFlow",
}

// reconcileBindings points each named realm binding at alias. Binding is
// additive and best-effort per entry: an unrecognized binding name is
// skipped rather than failing the whole reconciliation.
func reconcileBindings(ctx context.Context, api *AdminAPI, realm, alias string, bindings []neteye.KeycloakAuthFlowBinding) (bool, error) {
	if len(bindings) == 0 {
		return false, nil
	}
	realmRep, err := api.GetRealm(ctx, realm)
	if err != nil {
		return false, fmt.Errorf("get realm %q: %w", realm, err)
	}
	if realmRep == nil {
		return false, fmt.Errorf("realm %q not found", realm)
	}
	update := representation{}
	for _, binding := range bindings {
		field, ok := realmFlowFields[string(binding)]
		if !ok {
			continue
		}
		if stringValue(realmRep, field) == alias {
			continue
		}
		update[field] = alias
	}
	if len(update) == 0 {
		return false, nil
	}
	for k, v := range realmRep {
		if _, overridden := update[k]; !overridden {
			update[k] = v
		}
	}
	if err := api.UpdateRealm(ctx, realm, update); err != nil {
		return false, fmt.Errorf("bind flow %q to realm %q: %w", alias, realm, err)
	}
	return true, nil
}

// PermanentDeleteError wraps a DeleteFlow failure that no retry can fix, such
// as a built-in flow Keycloak refuses to delete, or a flow representation
// missing its ID. Callers should stop retrying and release the finalizer
// instead of requeuing.
type PermanentDeleteError struct{ err error }

func (e *PermanentDeleteError) Error() string { return e.err.Error() }
func (e *PermanentDeleteError) Unwrap() error { return e.err }

func DeleteFlow(ctx context.Context, api *AdminAPI, spec neteye.KeycloakAuthFlowSpec) error {
	flow, err := api.GetAuthFlow(ctx, flowRealm(spec), spec.Alias)
	if err != nil || flow == nil {
		return err
	}
	if builtIn, _ := flow["builtIn"].(bool); builtIn {
		return &PermanentDeleteError{fmt.Errorf("refusing to delete built-in Keycloak flow %q", spec.Alias)}
	}
	id := stringValue(flow, "id")
	if id == "" {
		return &PermanentDeleteError{fmt.Errorf("flow %q exists but has no ID", spec.Alias)}
	}
	return api.DeleteAuthFlow(ctx, flowRealm(spec), id)
}

func flowRealm(spec neteye.KeycloakAuthFlowSpec) string {
	if spec.Realm == "" {
		return "master"
	}
	return spec.Realm
}
func flowProvider(provider string) string {
	if provider == "" {
		return "basic-flow"
	}
	return provider
}

func ensureRootFlow(ctx context.Context, api *AdminAPI, realm, alias, provider string) (bool, error) {
	flow, err := api.GetAuthFlow(ctx, realm, alias)
	if err != nil {
		return false, fmt.Errorf("get flow %q: %w", alias, err)
	}
	if flow != nil {
		return false, nil
	}
	if err := api.CreateAuthFlow(ctx, realm, representation{"alias": alias, "providerId": flowProvider(provider), "topLevel": true, "builtIn": false}); err != nil {
		return false, fmt.Errorf("create flow %q: %w", alias, err)
	}
	return true, nil
}

func reconcileFlowExecutions(ctx context.Context, api *AdminAPI, realm, alias string, desired []flowExecution) (bool, bool, error) {
	live, err := api.ListDirectExecutions(ctx, realm, alias)
	if err != nil {
		return false, false, fmt.Errorf("list executions of flow %q: %w", alias, err)
	}
	created, updated := false, false
	kept := map[string]bool{}
	for _, want := range desired {
		current := matchingExecution(live, want, kept)
		if current == nil && want.Flow != nil {
			f := want.Flow
			if err := api.CreateSubflow(ctx, realm, alias, representation{"alias": f.Alias, "type": flowProvider(f.Provider), "provider": flowProvider(f.Provider)}); err != nil {
				return created, updated, fmt.Errorf("create subflow %q: %w", f.Alias, err)
			}
			created = true
			live, err = api.ListDirectExecutions(ctx, realm, alias)
			if err != nil {
				return created, updated, err
			}
			current = matchingExecution(live, want, kept)
		} else if current == nil {
			if want.Authenticator == "" {
				return created, updated, fmt.Errorf("execution in flow %q has neither flow nor authenticator", alias)
			}
			if err := api.CreateExecution(ctx, realm, alias, want.Authenticator); err != nil {
				return created, updated, fmt.Errorf("create execution %q: %w", want.Authenticator, err)
			}
			created = true
			live, err = api.ListDirectExecutions(ctx, realm, alias)
			if err != nil {
				return created, updated, err
			}
			current = matchingExecution(live, want, kept)
		}
		if current == nil {
			return created, updated, fmt.Errorf("created execution %q was not found in flow %q", executionName(want), alias)
		}
		id := stringValue(current, "id")
		kept[id] = true
		if want.Requirement != "" && want.Requirement != stringValue(current, "requirement") {
			if err := api.UpdateExecutionRequirement(ctx, realm, alias, id, want.Requirement); err != nil {
				return created, updated, err
			}
			updated = true
		}
		// A leaf execution's displayName is read-only, so Spec.Alias is only
		// matching input (see matchingExecution), never written back.
		if want.Flow != nil {
			flowID := stringValue(current, "flowId")
			if flowID == "" {
				return created, updated, fmt.Errorf("subflow %q has no flow ID", want.Flow.Alias)
			}
			child, err := api.GetAuthFlowByID(ctx, realm, flowID)
			if err != nil {
				return created, updated, err
			}
			if stringValue(child, "alias") != want.Flow.Alias {
				return created, updated, fmt.Errorf("subflow execution points to %q, expected %q", stringValue(child, "alias"), want.Flow.Alias)
			}
			childCreated, childUpdated, err := reconcileFlowExecutions(ctx, api, realm, want.Flow.Alias, want.Flow.Executions)
			if err != nil {
				return created, updated, err
			}
			created = created || childCreated
			updated = updated || childUpdated
		} else if want.Config != nil {
			changed, err := reconcileAuthenticatorConfig(ctx, api, realm, current, want.Config)
			if err != nil {
				return created, updated, err
			}
			updated = updated || changed
		}
	}
	live, err = api.ListDirectExecutions(ctx, realm, alias)
	if err != nil {
		return created, updated, err
	}
	for _, execution := range live {
		if !kept[stringValue(execution, "id")] {
			if err := api.DeleteExecution(ctx, realm, stringValue(execution, "id")); err != nil {
				return created, updated, err
			}
			updated = true
		}
	}
	changed, err := reconcileExecutionOrder(ctx, api, realm, alias, desired)
	if err != nil {
		return created, updated, err
	}
	updated = updated || changed
	return created, updated, nil
}

// matchingExecution finds the live execution for want, skipping any live
// execution already claimed by an earlier desired entry (tracked in used) so
// that duplicate desired authenticators or subflow aliases each match a
// distinct live execution instead of collapsing onto the first one found.
func matchingExecution(live []representation, want flowExecution, used map[string]bool) representation {
	unclaimed := func(e representation) bool { return used == nil || !used[stringValue(e, "id")] }
	if want.Flow != nil {
		for _, e := range live {
			if authenticationFlow(e) && unclaimed(e) && (stringValue(e, "displayName") == want.Flow.Alias || stringValue(e, "alias") == want.Flow.Alias) {
				return e
			}
		}
		return nil
	}
	for _, e := range live {
		if !authenticationFlow(e) && unclaimed(e) && want.Alias != "" && stringValue(e, "alias") == want.Alias {
			return e
		}
	}
	for _, e := range live {
		if !authenticationFlow(e) && unclaimed(e) && want.Authenticator != "" && stringValue(e, "providerId") == want.Authenticator {
			return e
		}
	}
	return nil
}
func authenticationFlow(e representation) bool {
	value, _ := e["authenticationFlow"].(bool)
	return value
}
func executionName(e flowExecution) string {
	if e.Flow != nil {
		return e.Flow.Alias
	}
	if e.Alias != "" {
		return e.Alias
	}
	return e.Authenticator
}

func reconcileAuthenticatorConfig(ctx context.Context, api *AdminAPI, realm string, execution representation, desired *neteye.KeycloakAuthFlowConfig) (bool, error) {
	want := representation{"alias": desired.Alias, "config": stringMapRepresentation(desired.Values)}
	id := stringValue(execution, "authenticationConfig")
	if id == "" {
		return true, api.CreateAuthenticatorConfig(ctx, realm, stringValue(execution, "id"), want)
	}
	live, err := api.GetAuthenticatorConfig(ctx, realm, id)
	if err != nil {
		return false, err
	}
	if reflect.DeepEqual(live["alias"], want["alias"]) && jsonEqual(live["config"], want["config"]) {
		return false, nil
	}
	return true, api.UpdateAuthenticatorConfig(ctx, realm, id, want)
}

// stringMapRepresentation renders an authenticator config, dropping empty
// values: Keycloak discards those, so keeping them would rewrite the config on
// every reconciliation.
func stringMapRepresentation(values map[string]string) representation {
	result := representation{}
	for key, value := range values {
		if value == "" {
			continue
		}
		result[key] = value
	}
	return result
}
func jsonEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	var normalizedLeft, normalizedRight any
	return json.Unmarshal(leftJSON, &normalizedLeft) == nil && json.Unmarshal(rightJSON, &normalizedRight) == nil && reflect.DeepEqual(normalizedLeft, normalizedRight)
}

// reconcileExecutionOrder gives each desired execution an explicit priority
// matching its position in the spec.
//
// Not raise-priority/lower-priority: those swap the priority of two adjacent
// siblings, and Keycloak creates every execution with priority 0, so each swap
// is a no-op and the order never moves.
func reconcileExecutionOrder(ctx context.Context, api *AdminAPI, realm, alias string, desired []flowExecution) (bool, error) {
	changed := false
	live, err := api.ListDirectExecutions(ctx, realm, alias)
	if err != nil {
		return changed, err
	}
	sort.SliceStable(live, func(i, j int) bool { return intValue(live[i], "index") < intValue(live[j], "index") })
	used := map[string]bool{}
	for wantPriority, want := range desired {
		current := matchingExecution(live, want, used)
		if current == nil {
			continue
		}
		used[stringValue(current, "id")] = true
		if intValue(current, "priority") == wantPriority {
			continue
		}
		update := representation{}
		for k, v := range current {
			update[k] = v
		}
		update["priority"] = wantPriority
		if err := api.UpdateExecution(ctx, realm, alias, update); err != nil {
			return changed, fmt.Errorf("set priority of execution %q in flow %q: %w", stringValue(current, "id"), alias, err)
		}
		changed = true
	}
	return changed, nil
}
