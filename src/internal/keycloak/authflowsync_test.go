// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeExecutions is an in-memory stand-in for the execution endpoints of one
// authentication flow: the listing the reconciliation reads, and the update it
// writes a priority through. It records every update so a test can assert both
// what was sent and that nothing was sent at all.
type fakeExecutions struct {
	realm, flow string
	executions  []representation
	updates     []representation
}

func (f *fakeExecutions) start(t *testing.T) *AdminAPI {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return NewAdminAPI(server.URL, AdminCredentials{Username: "admin", Password: "secret"})
}

func (f *fakeExecutions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		writeJSON(w, map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	if r.URL.Path != "/admin/realms/"+f.realm+"/authentication/flows/"+f.flow+"/executions" {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, f.executions)
	case http.MethodPut:
		update := decode(r)
		f.updates = append(f.updates, update)
		for i, existing := range f.executions {
			if stringValue(existing, "id") == stringValue(update, "id") {
				f.executions[i] = update
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected method "+r.Method, http.StatusNotFound)
	}
}

// execution builds a live leaf execution as Keycloak reports it: level 0, so
// ListDirectExecutions keeps it, and the priority every execution is created
// with unless one was set explicitly.
func execution(id, provider string, index, priority int) representation {
	return representation{
		"id":          id,
		"providerId":  provider,
		"displayName": provider,
		"requirement": "ALTERNATIVE",
		"level":       0,
		"index":       index,
		"priority":    priority,
	}
}

func orderTestExecutions() []flowExecution {
	return []flowExecution{
		{Authenticator: "auth-cookie"},
		{Authenticator: "identity-provider-redirector"},
		{Authenticator: "home-idp-discovery"},
	}
}

// Keycloak creates every execution with priority 0, which is why the previous
// raise/lower-priority approach never moved anything: this asserts each
// execution is given the priority its position in the spec calls for.
func TestReconcileExecutionOrderSetsAnExplicitPriority(t *testing.T) {
	fake := &fakeExecutions{
		realm: "master",
		flow:  "neteye-idp-discovery-flow",
		executions: []representation{
			execution("exec-cookie", "auth-cookie", 0, 0),
			execution("exec-redirector", "identity-provider-redirector", 1, 0),
			execution("exec-discovery", "home-idp-discovery", 2, 0),
		},
	}
	api := fake.start(t)

	changed, err := reconcileExecutionOrder(context.Background(), api, fake.realm, fake.flow, orderTestExecutions())
	if err != nil {
		t.Fatalf("reconcileExecutionOrder: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true: two executions still carried the priority they were created with")
	}

	// The first execution already sits at priority 0 and needs no update.
	wantPriorities := map[string]int{"exec-redirector": 1, "exec-discovery": 2}
	if len(fake.updates) != len(wantPriorities) {
		t.Fatalf("updates = %d (%v), want %d", len(fake.updates), fake.updates, len(wantPriorities))
	}
	for _, update := range fake.updates {
		id := stringValue(update, "id")
		want, expected := wantPriorities[id]
		if !expected {
			t.Errorf("unexpected update of execution %q", id)
			continue
		}
		if got := intValue(update, "priority"); got != want {
			t.Errorf("priority of %q = %d, want %d", id, got, want)
		}
		// Keycloak replaces the whole representation, so an update that
		// dropped the other fields would reset them.
		if got := stringValue(update, "requirement"); got != "ALTERNATIVE" {
			t.Errorf("requirement of %q = %q, want the live value ALTERNATIVE to be preserved", id, got)
		}
	}
}

func TestReconcileExecutionOrderIsIdempotent(t *testing.T) {
	fake := &fakeExecutions{
		realm: "master",
		flow:  "neteye-idp-discovery-flow",
		executions: []representation{
			execution("exec-cookie", "auth-cookie", 0, 0),
			execution("exec-redirector", "identity-provider-redirector", 1, 1),
			execution("exec-discovery", "home-idp-discovery", 2, 2),
		},
	}
	api := fake.start(t)

	changed, err := reconcileExecutionOrder(context.Background(), api, fake.realm, fake.flow, orderTestExecutions())
	if err != nil {
		t.Fatalf("reconcileExecutionOrder: %v", err)
	}
	if changed {
		t.Error("changed = true, want false: every execution already carries its desired priority")
	}
	if len(fake.updates) != 0 {
		t.Errorf("updates = %v, want none: rewriting an unchanged priority marks the resource updated on every reconciliation", fake.updates)
	}
}

// The desired order is the spec order, not the order Keycloak happens to
// report: a flow whose executions come back reversed must be renumbered.
func TestReconcileExecutionOrderRenumbersAReorderedFlow(t *testing.T) {
	fake := &fakeExecutions{
		realm: "master",
		flow:  "neteye-idp-discovery-flow",
		executions: []representation{
			execution("exec-discovery", "home-idp-discovery", 0, 0),
			execution("exec-redirector", "identity-provider-redirector", 1, 1),
			execution("exec-cookie", "auth-cookie", 2, 2),
		},
	}
	api := fake.start(t)

	changed, err := reconcileExecutionOrder(context.Background(), api, fake.realm, fake.flow, orderTestExecutions())
	if err != nil {
		t.Fatalf("reconcileExecutionOrder: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	wantPriorities := map[string]int{"exec-cookie": 0, "exec-discovery": 2}
	if len(fake.updates) != len(wantPriorities) {
		t.Fatalf("updates = %d (%v), want %d: only the two executions out of place move", len(fake.updates), fake.updates, len(wantPriorities))
	}
	for _, update := range fake.updates {
		id := stringValue(update, "id")
		want, expected := wantPriorities[id]
		if !expected {
			t.Errorf("unexpected update of execution %q", id)
			continue
		}
		if got := intValue(update, "priority"); got != want {
			t.Errorf("priority of %q = %d, want %d", id, got, want)
		}
	}
}
