// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"testing"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func TestReconcileUserCreatesAccountWithRoleAndPassword(t *testing.T) {
	fake := newFakeKeycloakUsers("master").withRole("admin").withGroup("/neteye-admins")
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{
		Username:   "neteye-internal-keycloak-admin",
		Email:      "admin@example.com",
		RealmRoles: []string{"admin"},
		Groups:     []string{"/neteye-admins"},
	}
	result, err := ReconcileUser(context.Background(), api, spec, UserCredential{Password: "s3cret"})
	if err != nil {
		t.Fatalf("ReconcileUser: %v", err)
	}
	if !result.Created || result.Adopted {
		t.Fatalf("expected a created account, got %+v", result)
	}
	if !result.PasswordSet {
		t.Fatal("expected the password to be set on creation")
	}

	user := fake.userByUsername(spec.Username)
	if user == nil {
		t.Fatal("the account was not created")
	}
	if enabled, _ := user["enabled"].(bool); !enabled {
		t.Error("the account should be enabled by default")
	}
	if stringValue(user, "email") != spec.Email {
		t.Errorf("email = %q, want %q", stringValue(user, "email"), spec.Email)
	}
	if got := fake.roleMap[result.UserID]; len(got) != 1 || got[0] != "admin" {
		t.Errorf("realm roles = %v, want [admin]", got)
	}
	if got := fake.groupMap[result.UserID]; len(got) != 1 || got[0] != "/neteye-admins" {
		t.Errorf("groups = %v, want [/neteye-admins]", got)
	}
	if password := fake.passwords[result.UserID]; stringValue(password, "value") != "s3cret" {
		t.Errorf("stored password = %v, want s3cret", password)
	}
}

func TestReconcileUserAdoptsExistingAccountWithoutTouchingItsPassword(t *testing.T) {
	fake := newFakeKeycloakUsers("master")
	fake.withUser(representation{"username": "root", "enabled": true, "firstName": "Root"})
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{Username: "root"}
	result, err := ReconcileUser(context.Background(), api, spec, UserCredential{Password: "new-password"})
	if err != nil {
		t.Fatalf("ReconcileUser: %v", err)
	}
	if result.Created || !result.Adopted {
		t.Fatalf("expected the account to be adopted, got %+v", result)
	}
	if result.PasswordSet || fake.resetCalls != 0 {
		t.Error("an adopted account must keep the password it was found with")
	}
	if got := stringValue(fake.users[result.UserID], "firstName"); got != "Root" {
		t.Errorf("firstName = %q, want it left untouched", got)
	}
}

func TestReconcileUserRotatesPasswordOnlyWhenAsked(t *testing.T) {
	fake := newFakeKeycloakUsers("master")
	fake.withUser(representation{"username": "svc", "enabled": true})
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{Username: "svc"}
	result, err := ReconcileUser(context.Background(), api, spec, UserCredential{Password: "rotated", Rotate: true, Temporary: true})
	if err != nil {
		t.Fatalf("ReconcileUser: %v", err)
	}
	if !result.PasswordSet {
		t.Fatal("a requested rotation must reset the password")
	}
	credential := fake.passwords[result.UserID]
	if stringValue(credential, "value") != "rotated" {
		t.Errorf("password = %v, want rotated", credential)
	}
	if temporary, _ := credential["temporary"].(bool); !temporary {
		t.Error("the credential should have been marked temporary")
	}
}

func TestReconcileUserCorrectsDriftOnManagedFieldsOnly(t *testing.T) {
	fake := newFakeKeycloakUsers("master").withRole("admin")
	fake.withUser(representation{"username": "svc", "enabled": false, "lastName": "Owner"})
	fake.roleMap["user-svc"] = []string{"admin"}
	api := fake.start(t)

	enabled := true
	spec := neteye.KeycloakUserSpec{Username: "svc", Enabled: &enabled, RealmRoles: []string{}}
	// An empty list no longer unassigns anything: the operator only adds what it
	// declares.
	result, err := ReconcileUser(context.Background(), api, spec, UserCredential{})
	if err != nil {
		t.Fatalf("ReconcileUser: %v", err)
	}
	if !result.Updated {
		t.Fatal("expected the drift to be reported as an update")
	}
	user := fake.users[result.UserID]
	if enabled, _ := user["enabled"].(bool); !enabled {
		t.Error("enabled drift was not corrected")
	}
	if stringValue(user, "lastName") != "Owner" {
		t.Error("a field the spec does not declare must survive the update")
	}
	if got := fake.roleMap[result.UserID]; len(got) != 1 || got[0] != "admin" {
		t.Errorf("realm roles = %v, want the role granted outside the spec kept", got)
	}
}

func TestReconcileUserFailsOnMissingRealmRole(t *testing.T) {
	fake := newFakeKeycloakUsers("master")
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{Username: "svc", RealmRoles: []string{"nonexistent"}}
	if _, err := ReconcileUser(context.Background(), api, spec, UserCredential{}); err == nil {
		t.Fatal("expected an error for a role that does not exist")
	}
}

func TestDeleteUserIsIdempotent(t *testing.T) {
	fake := newFakeKeycloakUsers("master")
	fake.withUser(representation{"username": "svc"})
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{Username: "svc"}
	if err := DeleteUser(context.Background(), api, spec); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if fake.userByUsername("svc") != nil {
		t.Fatal("the account was not deleted")
	}
	if err := DeleteUser(context.Background(), api, spec); err != nil {
		t.Fatalf("DeleteUser on a missing account: %v", err)
	}
}
