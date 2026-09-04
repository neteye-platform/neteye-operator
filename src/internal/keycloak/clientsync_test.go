// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/utils/ptr"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func sampleSpec() neteye.KeycloakClientSpec {
	return neteye.KeycloakClientSpec{
		Realm:                       "neteye",
		ClientID:                    "neteye",
		RootURL:                     "https://neteye.example.com",
		RedirectUris:                []string{"/neteye/*"},
		DirectAccess:                true,
		AllowClientCredentialsGrant: true,
		ProtocolMappers: []neteye.KeycloakProtocolMapper{{
			Name:           "groups membership",
			ProtocolMapper: "oidc-group-membership-mapper",
			Config:         map[string]string{"claim.name": "groups", "full.path": "true"},
		}},
		DefaultClientScopes:  []string{"profile", "email"},
		OptionalClientScopes: []string{"phone"},
	}
}

func TestReconcileClientCreates(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile", "email", "phone")
	api := fake.start(t)

	result, err := ReconcileClient(context.Background(), api, sampleSpec(), "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Error("expected the client to be created")
	}
	if result.UUID != "uuid-neteye" {
		t.Errorf("uuid = %q", result.UUID)
	}

	created := fake.clients[result.UUID]
	if got := created["directAccessGrantsEnabled"]; got != true {
		t.Errorf("directAccessGrantsEnabled = %v", got)
	}
	if got := created["serviceAccountsEnabled"]; got != true {
		t.Errorf("serviceAccountsEnabled = %v", got)
	}
	if got := created["secret"]; got != "client-secret" {
		t.Errorf("secret = %v", got)
	}
	if got := created["redirectUris"]; !reflect.DeepEqual(got, []any{"/neteye/*"}) {
		t.Errorf("redirectUris = %v", got)
	}
	if mappers := fake.mappers[result.UUID]; len(mappers) != 1 || stringValue(mappers[0], "name") != "groups membership" {
		t.Errorf("mappers = %v", mappers)
	}
	if got := fake.scopes[result.UUID]["default"]; !reflect.DeepEqual(got, []string{"profile", "email"}) {
		t.Errorf("default client scopes = %v", got)
	}
	if got := fake.scopes[result.UUID]["optional"]; !reflect.DeepEqual(got, []string{"phone"}) {
		t.Errorf("optional client scopes = %v", got)
	}
}

func TestReconcileClientIsIdempotent(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile", "email", "phone")
	api := fake.start(t)
	spec := sampleSpec()

	if _, err := ReconcileClient(context.Background(), api, spec, "client-secret"); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileClient(context.Background(), api, spec, "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Error("expected the existing client to be reused")
	}
	if result.Updated {
		t.Error("expected no update on an unchanged client")
	}
}

func TestReconcileClientRevertsDrift(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile", "email", "phone")
	api := fake.start(t)
	spec := sampleSpec()
	if _, err := ReconcileClient(context.Background(), api, spec, "client-secret"); err != nil {
		t.Fatal(err)
	}

	// Somebody edits the client, its mapper, and its scopes in the admin console.
	uuid := "uuid-neteye"
	fake.clients[uuid]["directAccessGrantsEnabled"] = false
	fake.clients[uuid]["redirectUris"] = []any{"https://evil.example.com/*"}
	fake.mappers[uuid][0]["config"] = map[string]any{"claim.name": "roles"}
	fake.mappers[uuid] = append(fake.mappers[uuid], representation{"id": "mapper-extra", "name": "extra"})
	fake.scopes[uuid]["default"] = []string{"profile"}
	fake.scopes[uuid]["optional"] = []string{"phone", "email"}

	result, err := ReconcileClient(context.Background(), api, spec, "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated {
		t.Fatal("expected drift to be reconciled")
	}
	if got := fake.clients[uuid]["directAccessGrantsEnabled"]; got != true {
		t.Errorf("directAccessGrantsEnabled = %v", got)
	}
	if got := fake.clients[uuid]["redirectUris"]; !reflect.DeepEqual(got, []any{"/neteye/*"}) {
		t.Errorf("redirectUris = %v", got)
	}
	// The declared mapper is corrected, the one added outside the spec survives.
	if len(fake.mappers[uuid]) != 2 {
		t.Fatalf("mappers = %v, want the declared one plus the untouched extra", fake.mappers[uuid])
	}
	if stringValue(fake.mappers[uuid][1], "name") != "extra" {
		t.Errorf("mapper added outside the spec was removed: %v", fake.mappers[uuid])
	}
	config, _ := fake.mappers[uuid][0]["config"].(map[string]any)
	if config["claim.name"] != "groups" || config["full.path"] != "true" {
		t.Errorf("mapper config = %v", config)
	}
	if got := fake.scopes[uuid]["default"]; !reflect.DeepEqual(got, []string{"profile", "email"}) {
		t.Errorf("default client scopes = %v", got)
	}
	// "email" was assigned as optional outside the spec and is not declared
	// there, so it stays.
	if got := fake.scopes[uuid]["optional"]; !reflect.DeepEqual(got, []string{"phone", "email"}) {
		t.Errorf("optional client scopes = %v, want the undeclared \"email\" kept", got)
	}
}

func TestReconcileClientKeepsUnmanagedFields(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile", "email", "phone")
	api := fake.start(t)
	spec := sampleSpec()
	if _, err := ReconcileClient(context.Background(), api, spec, "client-secret"); err != nil {
		t.Fatal(err)
	}
	uuid := "uuid-neteye"
	fake.clients[uuid]["frontchannelLogout"] = true
	fake.clients[uuid]["directAccessGrantsEnabled"] = false

	if _, err := ReconcileClient(context.Background(), api, spec, "client-secret"); err != nil {
		t.Fatal(err)
	}
	if got := fake.clients[uuid]["frontchannelLogout"]; got != true {
		t.Errorf("expected the unmanaged field to survive, got %v", got)
	}
}

func TestReconcileClientLeavesNilListsAlone(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile")
	api := fake.start(t)
	spec := neteye.KeycloakClientSpec{Realm: "neteye", ClientID: "neteye", Enabled: ptr.To(true)}

	result, err := ReconcileClient(context.Background(), api, spec, "")
	if err != nil {
		t.Fatal(err)
	}
	fake.mappers[result.UUID] = []representation{{"id": "mapper-extra", "name": "extra"}}
	fake.scopes[result.UUID] = map[string][]string{"default": {"profile"}}

	if _, err := ReconcileClient(context.Background(), api, spec, ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.mappers[result.UUID]) != 1 {
		t.Errorf("unmanaged mappers were changed: %v", fake.mappers[result.UUID])
	}
	if got := fake.scopes[result.UUID]["default"]; !reflect.DeepEqual(got, []string{"profile"}) {
		t.Errorf("unmanaged client scopes were changed: %v", got)
	}
}

func TestReconcileClientRejectsUnknownScope(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile")
	api := fake.start(t)
	spec := neteye.KeycloakClientSpec{Realm: "neteye", ClientID: "neteye", DefaultClientScopes: []string{"missing"}}

	if _, err := ReconcileClient(context.Background(), api, spec, ""); err == nil {
		t.Fatal("expected an error for a scope that does not exist in the realm")
	}
}

func TestDeleteClient(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile", "email", "phone")
	api := fake.start(t)
	spec := sampleSpec()
	if _, err := ReconcileClient(context.Background(), api, spec, "client-secret"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteClient(context.Background(), api, spec); err != nil {
		t.Fatal(err)
	}
	if len(fake.clients) != 0 {
		t.Errorf("clients = %v", fake.clients)
	}
	// Deleting an already absent client must stay a no-op for the finalizer.
	if err := DeleteClient(context.Background(), api, spec); err != nil {
		t.Fatal(err)
	}
}

func TestAdminAPIReusesToken(t *testing.T) {
	fake := newFakeKeycloak("neteye", "profile")
	api := fake.start(t)
	spec := neteye.KeycloakClientSpec{Realm: "neteye", ClientID: "neteye"}

	if _, err := ReconcileClient(context.Background(), api, spec, ""); err != nil {
		t.Fatal(err)
	}
	if fake.tokenIssued != 1 {
		t.Errorf("token requests = %d, expected the cached token to be reused", fake.tokenIssued)
	}
}

// seedRoleOwner registers a client that defines roles, as master-realm does.
func (f *fakeKeycloak) seedRoleOwner(clientID string, roles ...string) string {
	uuid := "uuid-" + clientID
	f.clients[uuid] = representation{"clientId": clientID, "id": uuid}
	f.clientRoles[uuid] = roles
	return uuid
}

func TestReconcileClientAssignsServiceAccountClientRoles(t *testing.T) {
	fake := newFakeKeycloak("master")
	ownerUUID := fake.seedRoleOwner("master-realm", "view-users", "query-users", "query-groups", "manage-users")
	api := fake.start(t)

	spec := neteye.KeycloakClientSpec{
		ClientID:                    "neteye",
		AllowClientCredentialsGrant: true,
		ServiceAccount: &neteye.KeycloakServiceAccountSpec{
			ClientRoles: map[string][]string{"master-realm": {"view-users", "query-users", "query-groups"}},
		},
	}
	result, err := ReconcileClient(context.Background(), api, spec, "")
	if err != nil {
		t.Fatalf("ReconcileClient: %v", err)
	}

	assigned := fake.accountRoles["service-account-"+result.UUID+"/"+ownerUUID]
	if len(assigned) != 3 {
		t.Fatalf("assigned roles = %v, want the three read-only roles", assigned)
	}
	for _, want := range []string{"view-users", "query-users", "query-groups"} {
		if !containsRole(assigned, want) {
			t.Errorf("role %q was not assigned (got %v)", want, assigned)
		}
	}
}

func TestReconcileClientKeepsUndeclaredServiceAccountRoles(t *testing.T) {
	fake := newFakeKeycloak("master")
	ownerUUID := fake.seedRoleOwner("master-realm", "view-users", "manage-users")
	api := fake.start(t)

	spec := neteye.KeycloakClientSpec{
		ClientID:                    "neteye",
		AllowClientCredentialsGrant: true,
		ServiceAccount: &neteye.KeycloakServiceAccountSpec{
			ClientRoles: map[string][]string{"master-realm": {"view-users"}},
		},
	}
	// Someone granted manage-users by hand: the operator owns only what it
	// declares, so that grant must survive the next reconciliation.
	result, err := ReconcileClient(context.Background(), api, spec, "")
	if err != nil {
		t.Fatalf("ReconcileClient: %v", err)
	}
	key := "service-account-" + result.UUID + "/" + ownerUUID
	fake.accountRoles[key] = append(fake.accountRoles[key], "manage-users")

	if _, err := ReconcileClient(context.Background(), api, spec, ""); err != nil {
		t.Fatalf("ReconcileClient (second pass): %v", err)
	}
	assigned := fake.accountRoles[key]
	if len(assigned) != 2 || !containsRole(assigned, "view-users") || !containsRole(assigned, "manage-users") {
		t.Errorf("assigned roles = %v, want view-users kept and manage-users left alone", assigned)
	}
}

func TestReconcileClientFailsOnUnknownServiceAccountRole(t *testing.T) {
	fake := newFakeKeycloak("master")
	fake.seedRoleOwner("master-realm", "view-users")
	api := fake.start(t)

	spec := neteye.KeycloakClientSpec{
		ClientID:                    "neteye",
		AllowClientCredentialsGrant: true,
		ServiceAccount: &neteye.KeycloakServiceAccountSpec{
			ClientRoles: map[string][]string{"master-realm": {"nonexistent"}},
		},
	}
	if _, err := ReconcileClient(context.Background(), api, spec, ""); err == nil {
		t.Fatal("expected an error for a role that does not exist")
	}
}

func TestReconcileClientRejectsRolesWithoutAServiceAccount(t *testing.T) {
	fake := newFakeKeycloak("master")
	fake.seedRoleOwner("master-realm", "view-users")
	api := fake.start(t)

	spec := neteye.KeycloakClientSpec{
		ClientID:                    "neteye",
		AllowClientCredentialsGrant: false,
		ServiceAccount: &neteye.KeycloakServiceAccountSpec{
			ClientRoles: map[string][]string{"master-realm": {"view-users"}},
		},
	}
	if _, err := ReconcileClient(context.Background(), api, spec, ""); err == nil {
		t.Fatal("expected an error: roles cannot be granted to a disabled service account")
	}
}

func containsRole(roles []string, name string) bool {
	for _, role := range roles {
		if role == name {
			return true
		}
	}
	return false
}

func TestReconcileClientDoesNotRewriteAMapperKeycloakEnriched(t *testing.T) {
	fake := newFakeKeycloak("neteye")
	api := fake.start(t)
	spec := neteye.KeycloakClientSpec{
		Realm:    "neteye",
		ClientID: "neteye",
		ProtocolMappers: []neteye.KeycloakProtocolMapper{{
			Name:           "groups membership",
			ProtocolMapper: "oidc-group-membership-mapper",
			Config:         map[string]string{"claim.name": "groups", "full.path": "true"},
		}},
	}
	result, err := ReconcileClient(context.Background(), api, spec, "")
	if err != nil {
		t.Fatalf("ReconcileClient: %v", err)
	}

	// Keycloak fills in its own config entries on the mapper it just stored.
	config, _ := fake.mappers[result.UUID][0]["config"].(map[string]any)
	config["introspection.token.claim"] = "true"

	second, err := ReconcileClient(context.Background(), api, spec, "")
	if err != nil {
		t.Fatalf("ReconcileClient (second pass): %v", err)
	}
	if second.Updated {
		t.Error("a settled client must not be rewritten on every reconciliation")
	}
	config, _ = fake.mappers[result.UUID][0]["config"].(map[string]any)
	if config["introspection.token.claim"] != "true" {
		t.Errorf("config = %v, want the entry Keycloak added to survive", config)
	}
	if config["claim.name"] != "groups" {
		t.Errorf("config = %v, want the declared entries kept", config)
	}
}
