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
	if len(fake.mappers[uuid]) != 1 {
		t.Fatalf("mappers = %v", fake.mappers[uuid])
	}
	config, _ := fake.mappers[uuid][0]["config"].(map[string]any)
	if config["claim.name"] != "groups" || config["full.path"] != "true" {
		t.Errorf("mapper config = %v", config)
	}
	if got := fake.scopes[uuid]["default"]; !reflect.DeepEqual(got, []string{"profile", "email"}) {
		t.Errorf("default client scopes = %v", got)
	}
	if got := fake.scopes[uuid]["optional"]; !reflect.DeepEqual(got, []string{"phone"}) {
		t.Errorf("optional client scopes = %v", got)
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
