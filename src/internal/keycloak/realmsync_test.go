// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/utils/ptr"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// fakeKeycloakRealms is an in-memory stand-in for the realm endpoints of the
// Keycloak Admin API.
type fakeKeycloakRealms struct {
	realms map[string]representation
}

func newFakeKeycloakRealms() *fakeKeycloakRealms {
	return &fakeKeycloakRealms{realms: map[string]representation{}}
}

func (f *fakeKeycloakRealms) start(t *testing.T) *AdminAPI {
	t.Helper()
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return NewAdminAPI(server.URL, AdminCredentials{Username: "admin", Password: "secret"})
}

func (f *fakeKeycloakRealms) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		writeJSON(w, map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/admin/realms" {
		realm := decode(r)
		coerceAttributesToStrings(realm)
		f.realms[stringValue(realm, "realm")] = realm
		w.WriteHeader(http.StatusCreated)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/admin/realms/")
	switch r.Method {
	case http.MethodGet:
		realm, ok := f.realms[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, realm)
	case http.MethodPut:
		realm := decode(r)
		coerceAttributesToStrings(realm)
		f.realms[name] = realm
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		delete(f.realms, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func attributes(realm representation) map[string]any {
	attrs, _ := asMap(realm["attributes"])
	return attrs
}

// coerceAttributesToStrings mimics Keycloak's RealmRepresentation.attributes,
// which is typed Map<String,String>: any non-string value submitted for an
// attribute is coerced to its string form, same as the real Admin API.
func coerceAttributesToStrings(realm representation) {
	attrs, ok := asMap(realm["attributes"])
	if !ok {
		return
	}
	for key, value := range attrs {
		if _, isString := value.(string); isString {
			continue
		}
		attrs[key] = fmt.Sprintf("%v", value)
	}
	realm["attributes"] = map[string]any(attrs)
}

func TestDesiredRealmRepresentationDefaults(t *testing.T) {
	desired := desiredRealmRepresentation(neteye.KeycloakRealmSpec{Realm: "neteye"})

	wantBool := map[string]bool{
		"enabled":                   true,
		"eventsEnabled":             true,
		"adminEventsEnabled":        true,
		"adminEventsDetailsEnabled": true,
		"bruteForceProtected":       true,
		"permanentLockout":          false,
	}
	for key, want := range wantBool {
		if got := desired[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}

	wantFloat := map[string]float64{
		"eventsExpiration":             15552000,
		"maxDeltaTimeSeconds":          43200,
		"maxFailureWaitSeconds":        900,
		"minimumQuickLoginWaitSeconds": 60,
		"quickLoginCheckMilliSeconds":  1000,
		"failureFactor":                30,
		"waitIncrementSeconds":         60,
	}
	for key, want := range wantFloat {
		if got := desired[key]; got != want {
			t.Errorf("%s = %v (%T), want %v", key, got, got, want)
		}
	}

	if got := attributes(desired)["adminEventsExpiration"]; got != "15552000" {
		t.Errorf("attributes.adminEventsExpiration = %v (%T), want \"15552000\"", got, got)
	}
}

func TestDesiredRealmRepresentationOverrides(t *testing.T) {
	spec := neteye.KeycloakRealmSpec{
		Realm: "neteye",
		Events: neteye.KeycloakRealmEvents{
			EventsExpiration:      ptr.To(int64(3600)),
			AdminEventsExpiration: ptr.To(int64(7200)),
			EventsEnabled:         ptr.To(false),
		},
		BruteForceProtection: neteye.KeycloakRealmBruteForceProtection{
			FailureFactor:       ptr.To(int64(5)),
			BruteForceProtected: ptr.To(false),
		},
	}
	desired := desiredRealmRepresentation(spec)

	if got := desired["eventsExpiration"]; got != float64(3600) {
		t.Errorf("eventsExpiration = %v, want 3600", got)
	}
	if got := attributes(desired)["adminEventsExpiration"]; got != "7200" {
		t.Errorf("attributes.adminEventsExpiration = %v, want \"7200\"", got)
	}
	if got := desired["eventsEnabled"]; got != false {
		t.Errorf("eventsEnabled = %v, want false", got)
	}
	if got := desired["failureFactor"]; got != float64(5) {
		t.Errorf("failureFactor = %v, want 5", got)
	}
	if got := desired["bruteForceProtected"]; got != false {
		t.Errorf("bruteForceProtected = %v, want false", got)
	}
	// permanentLockout is never exposed as a spec field: always false.
	if got := desired["permanentLockout"]; got != false {
		t.Errorf("permanentLockout = %v, want false", got)
	}
}

func TestReconcileRealmCreatesWithEventsAndBruteForceProtectionDefaults(t *testing.T) {
	fake := newFakeKeycloakRealms()
	api := fake.start(t)

	result, err := ReconcileRealm(context.Background(), api, neteye.KeycloakRealmSpec{Realm: "neteye"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("expected the realm to be created")
	}
	created := fake.realms["neteye"]
	if got := created["bruteForceProtected"]; got != true {
		t.Errorf("bruteForceProtected = %v", got)
	}
	if got := created["eventsExpiration"]; got != float64(15552000) {
		t.Errorf("eventsExpiration = %v", got)
	}
	if got := attributes(created)["adminEventsExpiration"]; got != "15552000" {
		t.Errorf("attributes.adminEventsExpiration = %v (%T), want \"15552000\"", got, got)
	}
}

func TestReconcileRealmIsIdempotent(t *testing.T) {
	fake := newFakeKeycloakRealms()
	api := fake.start(t)
	spec := neteye.KeycloakRealmSpec{Realm: "neteye"}

	if _, err := ReconcileRealm(context.Background(), api, spec); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileRealm(context.Background(), api, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Error("expected the existing realm to be reused")
	}
	if result.Updated {
		t.Error("expected no update on an unchanged realm")
	}
}

func TestReconcileRealmUpdatesDriftedBruteForceSettings(t *testing.T) {
	fake := newFakeKeycloakRealms()
	api := fake.start(t)
	spec := neteye.KeycloakRealmSpec{Realm: "neteye"}
	if _, err := ReconcileRealm(context.Background(), api, spec); err != nil {
		t.Fatal(err)
	}

	// Simulate drift: someone disabled brute force protection directly in Keycloak.
	drifted := fake.realms["neteye"]
	drifted["bruteForceProtected"] = false
	fake.realms["neteye"] = drifted

	result, err := ReconcileRealm(context.Background(), api, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated {
		t.Error("expected drift to be corrected")
	}
	if got := fake.realms["neteye"]["bruteForceProtected"]; got != true {
		t.Errorf("bruteForceProtected = %v, want true after drift correction", got)
	}
}
