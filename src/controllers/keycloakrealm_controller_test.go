// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

type stubKeycloakRealms struct{ realms map[string]map[string]any }

func (s *stubKeycloakRealms) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/admin/realms" {
		realm := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&realm)
		s.realms[realm["realm"].(string)] = realm
		w.WriteHeader(http.StatusCreated)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/admin/realms/")
	if r.Method == http.MethodGet {
		realm, ok := s.realms[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(realm)
		return
	}
	http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
}

func newKeycloakRealmReconciler(t *testing.T, stub *stubKeycloakRealms, objects ...client.Object) (*KeycloakRealmReconciler, client.Client) {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	scheme := keycloakClientScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&neteye.KeycloakRealm{}).Build()
	return &KeycloakRealmReconciler{KeycloakAPIReconciler: KeycloakAPIReconciler{Client: c, Log: logr.Discard(), Scheme: scheme, KeycloakNamespace: keycloak.WorkloadNamespace, AdminAPIFactory: func(_ string, credentials keycloak.AdminCredentials) *keycloak.AdminAPI {
		return keycloak.NewAdminAPI(server.URL, credentials)
	}}}, c
}

func TestKeycloakRealmReconcileCreatesRealm(t *testing.T) {
	stub := &stubKeycloakRealms{realms: map[string]map[string]any{}}
	realm := &neteye.KeycloakRealm{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "neteye"}, Spec: neteye.KeycloakRealmSpec{Realm: "neteye", DisplayName: "NetEye"}}
	r, c := newKeycloakRealmReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), realm)
	if _, err := r.Reconcile(context.Background(), requestFor(realm)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.realms["neteye"]; !ok {
		t.Fatal("expected realm to be created")
	}
	updated := &neteye.KeycloakRealm{}
	if err := c.Get(context.Background(), requestFor(realm).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Errorf("status = %q", updated.Status.Status)
	}
}
