// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func internalAdminScheme(t *testing.T) *runtime.Scheme {
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

func TestEnsureInternalAdminUserDeclaresTheAccount(t *testing.T) {
	s := internalAdminScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureInternalAdminUser(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureInternalAdminUser: %v", err)
	}

	user := &neteye.KeycloakUser{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: InternalAdminResourceName}
	if err := c.Get(context.Background(), key, user); err != nil {
		t.Fatalf("the KeycloakUser was not created: %v", err)
	}
	if user.Spec.Username != InternalAdminUsername {
		t.Errorf("username = %q, want %q", user.Spec.Username, InternalAdminUsername)
	}
	if got := user.Spec.RealmRoles; len(got) != 1 || got[0] != InternalAdminRealmRole {
		t.Errorf("realmRoles = %v, want [%s]", got, InternalAdminRealmRole)
	}
	if user.Spec.Credential == nil || !user.Spec.Credential.Generate {
		t.Fatal("the account must own a generated credential")
	}
	if user.Spec.Credential.SecretRef.Name != InternalAdminSecretName {
		t.Errorf("secretRef.name = %q, want %q", user.Spec.Credential.SecretRef.Name, InternalAdminSecretName)
	}
	if user.Spec.Credential.SecretRef.Name == AdminSecretName {
		t.Error("the internal admin must not share the bootstrap admin Secret")
	}
	if user.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan", user.Spec.DeletionPolicy)
	}
}

func TestEnsureInternalAdminUserKeepsAdministratorEdits(t *testing.T) {
	s := internalAdminScheme(t)
	edited := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: InternalAdminResourceName},
		Spec: neteye.KeycloakUserSpec{
			Username:   InternalAdminUsername,
			RealmRoles: []string{"admin", "offline_access"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(edited).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureInternalAdminUser(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureInternalAdminUser: %v", err)
	}

	user := &neteye.KeycloakUser{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: InternalAdminResourceName}
	if err := c.Get(context.Background(), key, user); err != nil {
		t.Fatal(err)
	}
	if len(user.Spec.RealmRoles) != 2 {
		t.Errorf("realmRoles = %v, want the administrator's edit preserved", user.Spec.RealmRoles)
	}
}

// bootstrapAdminServer serves the token endpoint plus the user lookup and
// update calls that disabling the bootstrap account needs.
type bootstrapAdminServer struct {
	acceptedUser     string
	acceptedPassword string
	users            map[string]representation // username -> representation
}

func (b *bootstrapAdminServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
		_ = r.ParseForm()
		if r.Form.Get("username") != b.acceptedUser || r.Form.Get("password") != b.acceptedPassword {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"access_token": "token", "expires_in": 60})
		return
	}
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && segments[len(segments)-1] == "users":
		found := []representation{}
		if user, ok := b.users[r.URL.Query().Get("username")]; ok {
			found = append(found, user)
		}
		writeJSON(w, found)
	case r.Method == http.MethodPut:
		updated := decode(r)
		b.users[stringValue(updated, "username")] = updated
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func bootstrapComponent(t *testing.T, server *bootstrapAdminServer, internalPassword string) *Component {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	builder := fake.NewClientBuilder().WithScheme(internalAdminScheme(t))
	for _, secret := range adminSecrets("temp-admin", "boot", internalPassword) {
		builder = builder.WithObjects(secret)
	}
	component := NewComponent(builder.Build(), logr.Discard())
	component.AdminAPIFactory = func(_ string, credentials AdminCredentials) *AdminAPI {
		return NewAdminAPI(httpServer.URL, credentials)
	}
	return component
}

func TestEnsureBootstrapAdminDisabledDisablesItOnceTheInternalAdminWorks(t *testing.T) {
	server := &bootstrapAdminServer{
		acceptedUser: InternalAdminUsername, acceptedPassword: "internal",
		users: map[string]representation{
			"temp-admin": {"id": "user-temp", "username": "temp-admin", "enabled": true},
		},
	}
	component := bootstrapComponent(t, server, "internal")

	if err := component.EnsureBootstrapAdminDisabled(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureBootstrapAdminDisabled: %v", err)
	}
	if enabled, _ := server.users["temp-admin"]["enabled"].(bool); enabled {
		t.Error("the bootstrap admin should have been disabled")
	}
}

func TestEnsureBootstrapAdminDisabledKeepsItWhileItIsTheOnlyCredential(t *testing.T) {
	// The internal admin credential is not accepted, so the operator is still
	// authenticating as the bootstrap account: disabling it would lock it out.
	server := &bootstrapAdminServer{
		acceptedUser: "temp-admin", acceptedPassword: "boot",
		users: map[string]representation{
			"temp-admin": {"id": "user-temp", "username": "temp-admin", "enabled": true},
		},
	}
	component := bootstrapComponent(t, server, "stale")

	if err := component.EnsureBootstrapAdminDisabled(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureBootstrapAdminDisabled: %v", err)
	}
	if enabled, _ := server.users["temp-admin"]["enabled"].(bool); !enabled {
		t.Error("the bootstrap admin must stay enabled while it is the only usable credential")
	}
}

func TestEnsureBootstrapAdminDisabledIsIdempotent(t *testing.T) {
	server := &bootstrapAdminServer{
		acceptedUser: InternalAdminUsername, acceptedPassword: "internal",
		users: map[string]representation{
			"temp-admin": {"id": "user-temp", "username": "temp-admin", "enabled": false},
		},
	}
	component := bootstrapComponent(t, server, "internal")

	for range 2 {
		if err := component.EnsureBootstrapAdminDisabled(context.Background(), WorkloadNamespace); err != nil {
			t.Fatalf("EnsureBootstrapAdminDisabled: %v", err)
		}
	}
}
