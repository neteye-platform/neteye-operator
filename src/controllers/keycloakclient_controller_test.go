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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// stubKeycloak answers the handful of Admin API calls a KeycloakClient without
// protocol mappers or client scopes makes.
type stubKeycloak struct {
	clients map[string]map[string]any // clientId -> representation
}

func (s *stubKeycloak) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 60})

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/clients"):
		found := []map[string]any{}
		if client, ok := s.clients[r.URL.Query().Get("clientId")]; ok {
			found = append(found, client)
		}
		_ = json.NewEncoder(w).Encode(found)

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/clients"):
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		clientID, _ := body["clientId"].(string)
		body["id"] = "uuid-" + clientID
		s.clients[clientID] = body
		w.WriteHeader(http.StatusCreated)

	case r.Method == http.MethodDelete:
		segments := strings.Split(r.URL.Path, "/")
		uuid := segments[len(segments)-1]
		for clientID, client := range s.clients {
			if client["id"] == uuid {
				delete(s.clients, clientID)
			}
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPut:
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func keycloakClientScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func adminSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: keycloak.AdminSecretName},
		Data: map[string][]byte{
			keycloak.AdminSecretUsernameKey: []byte("admin"),
			keycloak.AdminSecretPasswordKey: []byte("secret"),
		},
	}
}

func keycloakClientCR(namespace string) *neteye.KeycloakClient {
	return &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "neteye"},
		Spec: neteye.KeycloakClientSpec{
			Realm:        "neteye",
			ClientID:     "neteye",
			RedirectUris: []string{"/neteye/*"},
			DirectAccess: true,
		},
	}
}

func newKeycloakClientReconciler(t *testing.T, stub *stubKeycloak, objects ...client.Object) (*KeycloakClientReconciler, client.Client) {
	t.Helper()
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)

	s := keycloakClientScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).
		WithStatusSubresource(&neteye.KeycloakClient{}).Build()
	r := &KeycloakClientReconciler{
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

func requestFor(obj client.Object) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}}
}

func TestKeycloakClientReconcileCreatesClient(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	r, c := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcc)

	result, err := r.Reconcile(context.Background(), requestFor(kcc))
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != DefaultReconciliationRequeueAfter {
		t.Errorf("requeueAfter = %s", result.RequeueAfter)
	}
	if _, ok := stub.clients["neteye"]; !ok {
		t.Fatal("expected the Keycloak client to be created")
	}

	updated := &neteye.KeycloakClient{}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Errorf("status = %q (%s)", updated.Status.Status, updated.Status.Message)
	}
	if updated.Status.ClientUUID != "uuid-neteye" {
		t.Errorf("clientUUID = %q", updated.Status.ClientUUID)
	}
	if !containsString(updated.Finalizers, KeycloakClientFinalizer) {
		t.Errorf("finalizers = %v", updated.Finalizers)
	}
}

func TestKeycloakClientReconcileWithoutAdminSecret(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	r, c := newKeycloakClientReconciler(t, stub, kcc)

	result, err := r.Reconcile(context.Background(), requestFor(kcc))
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != DefaultFailureRequeueAfter {
		t.Errorf("requeueAfter = %s", result.RequeueAfter)
	}
	updated := &neteye.KeycloakClient{}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateNotReady {
		t.Errorf("status = %q", updated.Status.Status)
	}
}

func TestKeycloakClientReconcileMissingClientSecret(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	kcc.Spec.SecretRef = &neteye.NetEyeSecretKeySelector{Name: "neteye-client-secret", Key: "client_secret"}
	r, c := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcc)

	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}
	updated := &neteye.KeycloakClient{}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateFailed {
		t.Errorf("status = %q", updated.Status.Status)
	}
	if len(stub.clients) != 0 {
		t.Errorf("expected no client to be reconciled without its secret, got %v", stub.clients)
	}
}

func TestKeycloakClientReconcileUsesClientSecret(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	kcc.Spec.SecretRef = &neteye.NetEyeSecretKeySelector{Name: "neteye-client-secret", Key: "client_secret"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: kcc.Namespace, Name: "neteye-client-secret"},
		Data:       map[string][]byte{"client_secret": []byte("s3cr3t")},
	}
	r, _ := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), secret, kcc)

	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}
	if got := stub.clients["neteye"]["secret"]; got != "s3cr3t" {
		t.Errorf("secret = %v", got)
	}
}

func TestKeycloakClientReconcileDeletesClient(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	r, c := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcc)
	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}

	live := &neteye.KeycloakClient{}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, live); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}

	if len(stub.clients) != 0 {
		t.Errorf("expected the Keycloak client to be deleted, got %v", stub.clients)
	}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, live); err == nil {
		t.Error("expected the KeycloakClient to be gone once the finalizer was removed")
	}
}

func TestKeycloakClientReconcileOrphanPolicyKeepsTheClient(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{
		"neteye": {"id": "uuid-neteye", "clientId": "neteye"},
	}}
	now := metav1.Now()
	kcc := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "neteye-tenant-shared",
			Name:              "neteye",
			Finalizers:        []string{KeycloakClientFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: neteye.KeycloakClientSpec{ClientID: "neteye", DeletionPolicy: neteye.KeycloakDeletionPolicyOrphan},
	}
	r, _ := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcc)

	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.clients["neteye"]; !ok {
		t.Fatal("the Orphan policy must leave the client — and its secret — in Keycloak")
	}
}

func TestKeycloakClientReconcileDeletesTheClientByDefault(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{
		"neteye-test": {"id": "uuid-neteye-test", "clientId": "neteye-test"},
	}}
	now := metav1.Now()
	kcc := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "neteye-tenant-shared",
			Name:              "neteye-test",
			Finalizers:        []string{KeycloakClientFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: neteye.KeycloakClientSpec{ClientID: "neteye-test"},
	}
	r, _ := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcc)

	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}
	if _, ok := stub.clients["neteye-test"]; ok {
		t.Fatal("an unset policy means Delete")
	}
}

func TestKeycloakClientRequeueOverrides(t *testing.T) {
	r := &KeycloakClientReconciler{KeycloakAPIReconciler: KeycloakAPIReconciler{FailureRequeueAfter: time.Second, ReconciliationRequeueAfter: 2 * time.Second}}
	if r.failureRequeue() != time.Second {
		t.Errorf("failureRequeue = %s", r.failureRequeue())
	}
	if r.reconciliationRequeue() != 2*time.Second {
		t.Errorf("reconciliationRequeue = %s", r.reconciliationRequeue())
	}
	if r.keycloakNamespace() != keycloak.WorkloadNamespace {
		t.Errorf("keycloakNamespace = %s", r.keycloakNamespace())
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestKeycloakClientReadyMessageReportsKeycloakManagedSecret(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	r, c := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), kcc)

	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}

	updated := &neteye.KeycloakClient{}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Fatalf("status = %q (%s)", updated.Status.Status, updated.Status.Message)
	}
	if !strings.Contains(updated.Status.Message, "managed by Keycloak") {
		t.Errorf("message = %q", updated.Status.Message)
	}
}

func TestKeycloakClientReadyMessagePlainWithSecretRef(t *testing.T) {
	stub := &stubKeycloak{clients: map[string]map[string]any{}}
	kcc := keycloakClientCR("neteye-tenant-shared")
	kcc.Spec.SecretRef = &neteye.NetEyeSecretKeySelector{Name: "neteye-client", Key: "secret"}
	clientSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: kcc.Namespace, Name: "neteye-client"},
		Data:       map[string][]byte{"secret": []byte("s3cr3t")},
	}
	r, c := newKeycloakClientReconciler(t, stub, adminSecret(keycloak.WorkloadNamespace), clientSecret, kcc)

	if _, err := r.Reconcile(context.Background(), requestFor(kcc)); err != nil {
		t.Fatal(err)
	}

	updated := &neteye.KeycloakClient{}
	if err := c.Get(context.Background(), requestFor(kcc).NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Status != neteye.ServiceStateReady {
		t.Fatalf("status = %q (%s)", updated.Status.Status, updated.Status.Message)
	}
	if strings.Contains(updated.Status.Message, "managed by Keycloak") {
		t.Errorf("message = %q", updated.Status.Message)
	}
}
