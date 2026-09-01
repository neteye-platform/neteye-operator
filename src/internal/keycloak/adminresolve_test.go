// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	_, username, err := ResolveAdminAPI(context.Background(), builder.Build(), WorkloadNamespace, factory)
	return username, err
}

func TestResolveAdminAPIUsesTheBootstrapAdminBeforeTheInternalOneExists(t *testing.T) {
	server := &tokenServer{username: "temp-admin", password: "boot"}
	username, err := resolve(t, server, adminSecrets("temp-admin", "boot", ""))
	if err != nil {
		t.Fatalf("ResolveAdminAPI: %v", err)
	}
	if username != "temp-admin" {
		t.Errorf("authenticated as %q, want the bootstrap admin", username)
	}
}

func TestResolveAdminAPIPrefersTheInternalAdminOnceItWorks(t *testing.T) {
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

func TestResolveAdminAPIFallsBackWhenTheInternalCredentialIsRejected(t *testing.T) {
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

func TestResolveAdminAPIFailsWithoutAnyCredential(t *testing.T) {
	server := &tokenServer{username: "temp-admin", password: "boot"}
	if _, err := resolve(t, server, nil); err == nil {
		t.Fatal("expected an error when no admin Secret exists")
	}
}
