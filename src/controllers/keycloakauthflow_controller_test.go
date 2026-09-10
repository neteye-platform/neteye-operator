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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

type stubKeycloakFlows struct {
	flows      map[string]map[string]any
	executions map[string][]map[string]any
	realms     map[string]map[string]any
}

func newStubKeycloakFlows() *stubKeycloakFlows {
	return &stubKeycloakFlows{flows: map[string]map[string]any{}, executions: map[string][]map[string]any{}}
}
func (s *stubKeycloakFlows) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/admin/realms/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/admin/realms/"), "/") {
		realm := parts[len(parts)-1]
		if s.realms == nil {
			s.realms = map[string]map[string]any{}
		}
		rep, ok := s.realms[realm]
		if !ok {
			rep = map[string]any{"realm": realm}
			s.realms[realm] = rep
		}
		_ = json.NewEncoder(w).Encode(rep)
		return
	}
	if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/admin/realms/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/admin/realms/"), "/") {
		realm := parts[len(parts)-1]
		var rep map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rep)
		if s.realms == nil {
			s.realms = map[string]map[string]any{}
		}
		s.realms[realm] = rep
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/authentication/flows") {
		values := []map[string]any{}
		for _, flow := range s.flows {
			values = append(values, flow)
		}
		_ = json.NewEncoder(w).Encode(values)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/authentication/flows") {
		var flow map[string]any
		_ = json.NewDecoder(r.Body).Decode(&flow)
		alias := flow["alias"].(string)
		flow["id"] = "flow-" + alias
		s.flows[alias] = flow
		w.WriteHeader(http.StatusCreated)
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/executions") {
		alias := parts[len(parts)-2]
		_ = json.NewEncoder(w).Encode(s.executions[alias])
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/executions/execution") {
		alias := parts[len(parts)-3]
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		provider := body["provider"].(string)
		s.executions[alias] = append(s.executions[alias], map[string]any{"id": "execution-" + provider, "providerId": provider, "level": 0, "index": len(s.executions[alias])})
		w.WriteHeader(http.StatusCreated)
		return
	}
	if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/authentication/flows/") {
		id := parts[len(parts)-1]
		for alias, flow := range s.flows {
			if flow["id"] == id {
				delete(s.flows, alias)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/authentication/executions/") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
}
func newKeycloakAuthFlowReconciler(t *testing.T, stub *stubKeycloakFlows, objects ...client.Object) (*KeycloakAuthFlowReconciler, client.Client) {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	scheme := keycloakClientScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&neteye.KeycloakAuthFlow{}).Build()
	return &KeycloakAuthFlowReconciler{KeycloakAPIReconciler: KeycloakAPIReconciler{Client: c, Log: logr.Discard(), Scheme: scheme, KeycloakNamespace: keycloak.WorkloadNamespace, AdminAPIFactory: func(_ string, credentials keycloak.AdminCredentials) *keycloak.AdminAPI {
		return keycloak.NewAdminAPI(server.URL, credentials)
	}}}, c
}
func TestKeycloakAuthFlowReconcileCreatesRootFlowWithExecutions(t *testing.T) {
	stub := newStubKeycloakFlows()
	flow := &neteye.KeycloakAuthFlow{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "browser"}, Spec: neteye.KeycloakAuthFlowSpec{Alias: "browser", Executions: []neteye.KeycloakAuthFlowExecution{{Authenticator: "auth-cookie", Requirement: "ALTERNATIVE"}}}}
	r, c := newKeycloakAuthFlowReconciler(t, stub, adminSecret(flow.Namespace), flow)
	if _, err := r.Reconcile(context.Background(), requestFor(flow)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.flows["browser"]; !ok {
		t.Fatal("expected root flow to be created")
	}
	if len(stub.executions["browser"]) != 1 {
		t.Fatal("expected execution to be created")
	}
	updated := &neteye.KeycloakAuthFlow{}
	if err := c.Get(context.Background(), requestFor(flow).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Errorf("status = %q", updated.Status.Status)
	}
}
func TestKeycloakAuthFlowReconcileBindsFlowToRealm(t *testing.T) {
	stub := newStubKeycloakFlows()
	flow := &neteye.KeycloakAuthFlow{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "browser"}, Spec: neteye.KeycloakAuthFlowSpec{Realm: "master", Alias: "neteye-first-broker-login-flow", Bindings: []neteye.KeycloakAuthFlowBinding{"browser"}}}
	r, _ := newKeycloakAuthFlowReconciler(t, stub, adminSecret(flow.Namespace), flow)
	if _, err := r.Reconcile(context.Background(), requestFor(flow)); err != nil {
		t.Fatal(err)
	}
	if got := stub.realms["master"]["browserFlow"]; got != "neteye-first-broker-login-flow" {
		t.Fatalf("realm browserFlow = %v, want neteye-first-broker-login-flow", got)
	}
}
func TestKeycloakAuthFlowDeleteHonorsOrphanAndBuiltInRefusal(t *testing.T) {
	for name, test := range map[string]struct {
		policy  neteye.KeycloakDeletionPolicy
		builtIn bool
	}{"orphan": {neteye.KeycloakDeletionPolicyOrphan, false}, "builtin": {neteye.KeycloakDeletionPolicyDelete, true}} {
		t.Run(name, func(t *testing.T) {
			stub := newStubKeycloakFlows()
			stub.flows["browser"] = map[string]any{"id": "flow-browser", "alias": "browser", "builtIn": test.builtIn}
			now := metav1.Now()
			flow := &neteye.KeycloakAuthFlow{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "browser", Finalizers: []string{KeycloakAuthFlowFinalizer}, DeletionTimestamp: &now}, Spec: neteye.KeycloakAuthFlowSpec{Alias: "browser", DeletionPolicy: test.policy}}
			r, c := newKeycloakAuthFlowReconciler(t, stub, adminSecret(flow.Namespace), flow)
			if _, err := r.Reconcile(context.Background(), requestFor(flow)); err != nil {
				t.Fatal(err)
			}
			if _, ok := stub.flows["browser"]; !ok {
				t.Fatal("flow must remain")
			}
			updated := &neteye.KeycloakAuthFlow{}
			err := c.Get(context.Background(), requestFor(flow).NamespacedName, updated)
			if err == nil && controllerutil.ContainsFinalizer(updated, KeycloakAuthFlowFinalizer) {
				t.Error("finalizer must be removed")
			} else if err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
		})
	}
}

func TestKeycloakAuthFlowMissingTenantCredentialsFailsAndDeleteReleasesFinalizer(t *testing.T) {
	stub := newStubKeycloakFlows()
	flow := &neteye.KeycloakAuthFlow{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "browser"}, Spec: neteye.KeycloakAuthFlowSpec{Alias: "browser"}}
	r, c := newKeycloakAuthFlowReconciler(t, stub, flow)
	if _, err := r.Reconcile(context.Background(), requestFor(flow)); err != nil {
		t.Fatal(err)
	}
	updated := &neteye.KeycloakAuthFlow{}
	if err := c.Get(context.Background(), requestFor(flow).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateFailed || !strings.Contains(updated.Status.Message, "no Keycloak admin credentials") {
		t.Errorf("status = %q (%s)", updated.Status.Status, updated.Status.Message)
	}
	now := metav1.Now()
	deleting := &neteye.KeycloakAuthFlow{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-delete", Name: "browser", Finalizers: []string{KeycloakAuthFlowFinalizer}, DeletionTimestamp: &now}, Spec: neteye.KeycloakAuthFlowSpec{Alias: "browser"}}
	r, c = newKeycloakAuthFlowReconciler(t, stub, deleting)
	if _, err := r.Reconcile(context.Background(), requestFor(deleting)); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), requestFor(deleting).NamespacedName, &neteye.KeycloakAuthFlow{}); err == nil {
		t.Error("finalizer was not removed")
	}
}
