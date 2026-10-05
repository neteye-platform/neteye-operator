// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"strings"
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

func TestReconcileUserSetsThePasswordOnCreationWithoutASeparateReset(t *testing.T) {
	fake := newFakeKeycloakUsers("master")
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{Username: "svc"}
	result, err := ReconcileUser(context.Background(), api, spec, UserCredential{Password: "Svc-Pass42x"}) // #nosec G101 -- False positive
	if err != nil {
		t.Fatalf("ReconcileUser: %v", err)
	}
	if !result.Created || !result.PasswordSet {
		t.Fatalf("expected a created account carrying its password, got %+v", result)
	}
	// The password has to travel with the creation: a separate reset-password
	// call is what used to leave a credential-less account behind when the
	// realm policy refused the password.
	if fake.resetCalls != 0 {
		t.Errorf("reset-password calls = %d, want the password to be set inline on creation", fake.resetCalls)
	}
	if got := stringValue(fake.passwords[result.UserID], "value"); got != "Svc-Pass42x" {
		t.Errorf("stored password = %q, want the created one", got)
	}
	// Keycloak never echoes credentials back, so they must not linger on the
	// stored representation and be mistaken for drift later.
	if _, ok := fake.users[result.UserID]["credentials"]; ok {
		t.Error("the stored account representation still carries credentials")
	}
}

func TestReconcileUserCreationFailsAtomicallyWhenThePolicyRefusesThePassword(t *testing.T) {
	fake := newFakeKeycloakUsers("master")
	fake.rejectCredential = func(password string) bool { return !strings.ContainsAny(password, "-._~") }
	api := fake.start(t)

	spec := neteye.KeycloakUserSpec{Username: "svc"}
	result, err := ReconcileUser(context.Background(), api, spec, UserCredential{Password: "NoSymbols42x"})
	if err == nil {
		t.Fatal("expected the creation to fail when the policy refuses the password")
	}
	if result.Created || result.PasswordSet {
		t.Errorf("result = %+v, want nothing reported as created", result)
	}
	if fake.userByUsername("svc") != nil {
		t.Fatal("a refused password must not leave an account behind")
	}

	// Because nothing was created, the next pass still takes the create path
	// and can repair itself with a compliant password. The old create-then-reset
	// order left an adopted account that no later reconciliation would fix.
	result, err = ReconcileUser(context.Background(), api, spec, UserCredential{Password: "test-password"})
	if err != nil {
		t.Fatalf("ReconcileUser after a refused password: %v", err)
	}
	if !result.Created || !result.PasswordSet {
		t.Fatalf("expected the retry to create the account with its password, got %+v", result)
	}
	if got := stringValue(fake.passwords[result.UserID], "value"); got != "test-password" {
		t.Errorf("stored password = %q, want the compliant one", got)
	}
}
