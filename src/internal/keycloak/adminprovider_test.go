// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// tokenServer accepts exactly one username/password pair and rejects the rest,
// which is what makes the verification step observable.
type tokenServer struct {
	username string
	password string
	attempts []string
}

func (t *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form, _ := url.ParseQuery(r.Form.Encode())
	user := form.Get("username")
	t.attempts = append(t.attempts, user)
	if user != t.username || form.Get("password") != t.password {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{"access_token": "token", "expires_in": 60})
}

func adminSecrets(bootstrapUser, bootstrapPassword, internalPassword string) []*corev1.Secret {
	secrets := []*corev1.Secret{{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: AdminSecretName},
		Data: map[string][]byte{
			AdminSecretUsernameKey: []byte(bootstrapUser),
			AdminSecretPasswordKey: []byte(bootstrapPassword),
		},
	}}
	if internalPassword != "" {
		secrets = append(secrets, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: InternalAdminSecretName},
			Data:       map[string][]byte{InternalAdminSecretPasswordKey: []byte(internalPassword)},
		})
	}
	return secrets
}

func resolve(t *testing.T, server *tokenServer, secrets []*corev1.Secret) (string, error) {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	builder := fake.NewClientBuilder().WithScheme(internalAdminScheme(t))
	for _, secret := range secrets {
		builder = builder.WithObjects(secret)
	}
	factory := func(_ string, credentials AdminCredentials) *AdminAPI {
		return NewAdminAPI(httpServer.URL, credentials)
	}
	provider := NewAdminProvider(builder.Build(), WorkloadNamespace, factory)
	_, username, err := provider.Get(context.Background())
	return username, err
}

func TestAdminProviderUsesTheBootstrapAdminBeforeTheInternalOneExists(t *testing.T) {
	server := &tokenServer{username: "temp-admin", password: "boot"}
	username, err := resolve(t, server, adminSecrets("temp-admin", "boot", ""))
	if err != nil {
		t.Fatalf("ResolveAdminAPI: %v", err)
	}
	if username != "temp-admin" {
		t.Errorf("authenticated as %q, want the bootstrap admin", username)
	}
}

func TestAdminProviderPrefersTheInternalAdminOnceItWorks(t *testing.T) {
	server := &tokenServer{username: InternalAdminUsername, password: "internal"}
	username, err := resolve(t, server, adminSecrets("temp-admin", "boot", "internal"))
	if err != nil {
		t.Fatalf("ResolveAdminAPI: %v", err)
	}
	if username != InternalAdminUsername {
		t.Errorf("authenticated as %q, want the internal admin", username)
	}
	if len(server.attempts) == 0 || server.attempts[0] != InternalAdminUsername {
		t.Errorf("attempts = %v, want the internal admin verified first", server.attempts)
	}
}

func TestAdminProviderFallsBackWhenTheInternalCredentialIsRejected(t *testing.T) {
	// The Secret holds a password Keycloak never accepted: switching to it would
	// lock the operator out, so it must fall back instead.
	server := &tokenServer{username: "temp-admin", password: "boot"}
	username, err := resolve(t, server, adminSecrets("temp-admin", "boot", "stale"))
	if err != nil {
		t.Fatalf("ResolveAdminAPI: %v", err)
	}
	if username != "temp-admin" {
		t.Errorf("authenticated as %q, want the bootstrap admin after the rejection", username)
	}
	// Only the internal admin is verified eagerly; the bootstrap client
	// authenticates lazily on its first real request, so no second token call
	// happens here.
	if len(server.attempts) != 1 || server.attempts[0] != InternalAdminUsername {
		t.Errorf("attempts = %v, want exactly one verification of the internal admin", server.attempts)
	}
}

func TestAdminProviderFailsWithoutAnyCredential(t *testing.T) {
	server := &tokenServer{username: "temp-admin", password: "boot"}
	if _, err := resolve(t, server, nil); err == nil {
		t.Fatal("expected an error when no admin Secret exists")
	}
}

func TestAdminProviderReusesTheClientAcrossReconciliations(t *testing.T) {
	server := &tokenServer{username: InternalAdminUsername, password: "internal"}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	builder := fake.NewClientBuilder().WithScheme(internalAdminScheme(t))
	for _, secret := range adminSecrets("temp-admin", "boot", "internal") {
		builder = builder.WithObjects(secret)
	}
	provider := NewAdminProvider(builder.Build(), WorkloadNamespace, func(_ string, credentials AdminCredentials) *AdminAPI {
		return NewAdminAPI(httpServer.URL, credentials)
	})

	for range 5 {
		if _, _, err := provider.Get(context.Background()); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	// One verification, not one per call: the cached token covers the rest.
	if len(server.attempts) != 1 {
		t.Errorf("token requests = %d, want 1 across five reconciliations", len(server.attempts))
	}
}

func TestAdminProviderRebuildsWhenTheCredentialChanges(t *testing.T) {
	server := &tokenServer{username: InternalAdminUsername, password: "rotated"}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	secrets := adminSecrets("temp-admin", "boot", "stale")
	builder := fake.NewClientBuilder().WithScheme(internalAdminScheme(t))
	for _, secret := range secrets {
		builder = builder.WithObjects(secret)
	}
	c := builder.Build()
	provider := NewAdminProvider(c, WorkloadNamespace, func(_ string, credentials AdminCredentials) *AdminAPI {
		return NewAdminAPI(httpServer.URL, credentials)
	})
	provider.RetryInterval = time.Nanosecond

	if _, username, err := provider.Get(context.Background()); err != nil || username != "temp-admin" {
		t.Fatalf("first Get = %q, %v; want the bootstrap admin", username, err)
	}

	// The KeycloakUser controller rotates the password: the provider must pick
	// the new one up instead of serving the stale client forever.
	internal := &corev1.Secret{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: InternalAdminSecretName}
	if err := c.Get(context.Background(), key, internal); err != nil {
		t.Fatal(err)
	}
	internal.Data[InternalAdminSecretPasswordKey] = []byte("rotated")
	if err := c.Update(context.Background(), internal); err != nil {
		t.Fatal(err)
	}

	_, username, err := provider.Get(context.Background())
	if err != nil {
		t.Fatalf("Get after rotation: %v", err)
	}
	if username != InternalAdminUsername {
		t.Errorf("authenticated as %q, want the internal admin after the rotation", username)
	}
}
