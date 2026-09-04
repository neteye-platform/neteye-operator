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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// stubKeycloakUsers answers the Admin API calls a KeycloakUser without groups
// makes, and records the password Keycloak was told to set.
type stubKeycloakUsers struct {
	users     map[string]map[string]any // username -> representation
	passwords map[string]string         // user id -> password
	roles     map[string][]string       // user id -> realm role names
	deleted   []string
}

func newStubKeycloakUsers() *stubKeycloakUsers {
	return &stubKeycloakUsers{
		users:     map[string]map[string]any{},
		passwords: map[string]string{},
		roles:     map[string][]string{},
	}
}

func (s *stubKeycloakUsers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	last := segments[len(segments)-1]

	switch {
	case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 60})

	case r.Method == http.MethodGet && last == "users":
		found := []map[string]any{}
		if user, ok := s.users[r.URL.Query().Get("username")]; ok {
			found = append(found, user)
		}
		_ = json.NewEncoder(w).Encode(found)

	case r.Method == http.MethodPost && last == "users":
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		username, _ := body["username"].(string)
		body["id"] = "user-" + username
		s.users[username] = body
		w.WriteHeader(http.StatusCreated)

	case r.Method == http.MethodPut && last == "reset-password":
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		password, _ := body["value"].(string)
		s.passwords[segments[len(segments)-2]] = password
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && segments[len(segments)-2] == "roles":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "role-" + last, "name": last})

	case last == "realm" && strings.Contains(r.URL.Path, "role-mappings"):
		userID := segments[len(segments)-3]
		if r.Method == http.MethodGet {
			mapped := []map[string]any{}
			for _, name := range s.roles[userID] {
				mapped = append(mapped, map[string]any{"id": "role-" + name, "name": name})
			}
			_ = json.NewEncoder(w).Encode(mapped)
			return
		}
		body := []map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, role := range body {
			name, _ := role["name"].(string)
			s.roles[userID] = append(s.roles[userID], name)
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, last)
		for username, user := range s.users {
			if user["id"] == last {
				delete(s.users, username)
			}
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPut:
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func keycloakUserCR(namespace string) *neteye.KeycloakUser {
	return &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "neteye-internal-keycloak-admin"},
		Spec: neteye.KeycloakUserSpec{
			Realm:      "master",
			Username:   "neteye-internal-keycloak-admin",
			RealmRoles: []string{"admin"},
			Credential: &neteye.KeycloakUserCredentialSpec{
				SecretRef: neteye.NetEyeSecretKeySelector{Name: "neteye-internal-keycloak-admin", Key: "password"},
				Generate:  true,
			},
		},
	}
}

func newKeycloakUserReconciler(t *testing.T, stub *stubKeycloakUsers, objects ...client.Object) (*KeycloakUserReconciler, client.Client) {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)

	s := keycloakClientScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).
		WithStatusSubresource(&neteye.KeycloakUser{}).Build()
	r := &KeycloakUserReconciler{
		KeycloakAPIReconciler: KeycloakAPIReconciler{
			Client:            c,
			Log:               logr.Discard(),
			Scheme:            s,
			KeycloakNamespace: keycloak.WorkloadNamespace,
			AdminAPIFactory: func(_ string, credentials keycloak.AdminCredentials) *keycloak.AdminAPI {
				return keycloak.NewAdminAPI(server.URL, credentials)
			},
		},
	}
	return r, c
}

func TestKeycloakUserReconcileCreatesUserAndStoresGeneratedPassword(t *testing.T) {
	stub := newStubKeycloakUsers()
	kcu := keycloakUserCR("neteye-tenant-shared")
	r, c := newKeycloakUserReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcu)

	result, err := r.Reconcile(context.Background(), requestFor(kcu))
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != DefaultReconciliationRequeueAfter {
		t.Errorf("requeueAfter = %s", result.RequeueAfter)
	}
	if _, ok := stub.users["neteye-internal-keycloak-admin"]; !ok {
		t.Fatal("expected the Keycloak account to be created")
	}
	if got := stub.roles["user-neteye-internal-keycloak-admin"]; len(got) != 1 || got[0] != "admin" {
		t.Errorf("realm roles = %v, want [admin]", got)
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: kcu.Namespace, Name: "neteye-internal-keycloak-admin"}
	if err := c.Get(context.Background(), key, secret); err != nil {
		t.Fatalf("the generated password was not stored: %v", err)
	}
	password := string(secret.Data["password"])
	if len(password) != generatedPasswordLength {
		t.Errorf("stored password length = %d, want %d", len(password), generatedPasswordLength)
	}
	if stub.passwords["user-neteye-internal-keycloak-admin"] != password {
		t.Error("the stored password differs from the one set on the account")
	}

	updated := &neteye.KeycloakUser{}
	if err := c.Get(context.Background(), requestFor(kcu).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Errorf("status = %q (%s)", updated.Status.Status, updated.Status.Message)
	}
	if updated.Status.UserID != "user-neteye-internal-keycloak-admin" {
		t.Errorf("userID = %q", updated.Status.UserID)
	}
	if !containsString(updated.Finalizers, KeycloakUserFinalizer) {
		t.Errorf("finalizers = %v", updated.Finalizers)
	}
}

func TestKeycloakUserReconcileReusesStoredPassword(t *testing.T) {
	stub := newStubKeycloakUsers()
	kcu := keycloakUserCR("neteye-tenant-shared")
	stored := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: kcu.Namespace, Name: "neteye-internal-keycloak-admin"},
		Data:       map[string][]byte{"password": []byte("already-there")},
	}
	r, _ := newKeycloakUserReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcu, stored)

	if _, err := r.Reconcile(context.Background(), requestFor(kcu)); err != nil {
		t.Fatal(err)
	}
	if got := stub.passwords["user-neteye-internal-keycloak-admin"]; got != "already-there" {
		t.Errorf("password set on the account = %q, want the stored one", got)
	}
}

func TestKeycloakUserReconcileAdoptsExistingAccount(t *testing.T) {
	stub := newStubKeycloakUsers()
	stub.users["neteye-internal-keycloak-admin"] = map[string]any{
		"id": "user-neteye-internal-keycloak-admin", "username": "neteye-internal-keycloak-admin", "enabled": true,
	}
	kcu := keycloakUserCR("neteye-tenant-shared")
	r, c := newKeycloakUserReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcu)

	if _, err := r.Reconcile(context.Background(), requestFor(kcu)); err != nil {
		t.Fatal(err)
	}
	if len(stub.passwords) != 0 {
		t.Error("an adopted account must keep the password it was found with")
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: kcu.Namespace, Name: "neteye-internal-keycloak-admin"}
	if err := c.Get(context.Background(), key, secret); err == nil {
		t.Error("no password should be stored for an account whose credential was left alone")
	}

	updated := &neteye.KeycloakUser{}
	if err := c.Get(context.Background(), requestFor(kcu).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Status.Adopted {
		t.Error("expected the account to be reported as adopted")
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Errorf("expected reconciliation to succeed after preserving the adopted account's password, got state %q", updated.Status.Status)
	}
}

func TestKeycloakUserReconcileDeletesTheAccountByDefault(t *testing.T) {
	stub := newStubKeycloakUsers()
	stub.users["svc"] = map[string]any{"id": "user-svc", "username": "svc", "enabled": true}
	now := metav1.Now()
	kcu := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "neteye-tenant-shared",
			Name:              "svc",
			Finalizers:        []string{KeycloakUserFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: neteye.KeycloakUserSpec{Username: "svc"},
	}
	r, _ := newKeycloakUserReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcu)

	if _, err := r.Reconcile(context.Background(), requestFor(kcu)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.users["svc"]; ok {
		t.Fatal("the default deletion policy must remove the account from Keycloak")
	}
}

func TestKeycloakUserReconcileOrphanPolicyKeepsTheAccount(t *testing.T) {
	stub := newStubKeycloakUsers()
	stub.users["svc"] = map[string]any{"id": "user-svc", "username": "svc", "enabled": true}
	now := metav1.Now()
	kcu := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "neteye-tenant-shared",
			Name:              "svc",
			Finalizers:        []string{KeycloakUserFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: neteye.KeycloakUserSpec{Username: "svc", DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan},
	}
	r, _ := newKeycloakUserReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcu)

	if _, err := r.Reconcile(context.Background(), requestFor(kcu)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.users["svc"]; !ok {
		t.Fatal("the Orphan policy must leave the account in Keycloak")
	}
}

func TestKeycloakUserReconcileDeletePolicyDeletesAccount(t *testing.T) {
	stub := newStubKeycloakUsers()
	stub.users["svc"] = map[string]any{"id": "user-svc", "username": "svc", "enabled": true}
	now := metav1.Now()
	kcu := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "neteye-tenant-shared",
			Name:              "svc",
			Finalizers:        []string{KeycloakUserFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: neteye.KeycloakUserSpec{Username: "svc", DeletionPolicy: neteye.KeycloakDeletionPolicyDelete},
	}
	r, _ := newKeycloakUserReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcu)

	if _, err := r.Reconcile(context.Background(), requestFor(kcu)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.users["svc"]; ok {
		t.Fatal("the Delete policy must remove the account from Keycloak")
	}
}

func TestKeycloakUserReconcileWithoutAdminSecret(t *testing.T) {
	stub := newStubKeycloakUsers()
	kcu := keycloakUserCR("neteye-tenant-shared")
	r, c := newKeycloakUserReconciler(t, stub, kcu)

	result, err := r.Reconcile(context.Background(), requestFor(kcu))
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != DefaultFailureRequeueAfter {
		t.Errorf("requeueAfter = %s", result.RequeueAfter)
	}
	updated := &neteye.KeycloakUser{}
	if err := c.Get(context.Background(), requestFor(kcu).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateNotReady {
		t.Errorf("status = %q", updated.Status.Status)
	}
}
