// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func TestEnsureMasterRealmDeclaresTheRealm(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureMasterRealm(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureMasterRealm: %v", err)
	}

	kcr := &neteye.KeycloakRealm{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: MasterRealmResourceName}
	if err := c.Get(context.Background(), key, kcr); err != nil {
		t.Fatalf("the KeycloakRealm was not created: %v", err)
	}
	if kcr.Spec.Realm != masterRealm {
		t.Errorf("realm = %q, want %q", kcr.Spec.Realm, masterRealm)
	}
	if kcr.Spec.DisplayName != "NetEye" {
		t.Errorf("displayName = %q, want NetEye", kcr.Spec.DisplayName)
	}
	wantPolicy := "length(12) and digits(2) and upperCase(1) and lowerCase(1) and specialChars(1) and passwordAge(120)"
	if kcr.Spec.PasswordPolicy == nil || *kcr.Spec.PasswordPolicy != wantPolicy {
		t.Errorf("passwordPolicy = %v, want %q", kcr.Spec.PasswordPolicy, wantPolicy)
	}
	if kcr.Spec.Theme == nil {
		t.Fatal("theme must be set")
	}
	if kcr.Spec.Theme.LoginTheme != "neteye" || kcr.Spec.Theme.AdminTheme != "neteye" ||
		kcr.Spec.Theme.AccountTheme != "neteye" || kcr.Spec.Theme.EmailTheme != "neteye" {
		t.Errorf("theme = %+v, want every theme set to neteye", kcr.Spec.Theme)
	}
	if kcr.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan for a resource the operator redeclares", kcr.Spec.DeletionPolicy)
	}
}

func TestEnsureMasterRealmKeepsAdministratorEdits(t *testing.T) {
	edited := &neteye.KeycloakRealm{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: MasterRealmResourceName},
		Spec: neteye.KeycloakRealmSpec{
			Realm:          masterRealm,
			PasswordPolicy: ptr.To("length(20)"),
			Theme:          &neteye.KeycloakRealmTheme{LoginTheme: "custom"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).WithObjects(edited).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureMasterRealm(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureMasterRealm: %v", err)
	}

	kcr := &neteye.KeycloakRealm{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: MasterRealmResourceName}
	if err := c.Get(context.Background(), key, kcr); err != nil {
		t.Fatal(err)
	}
	if kcr.Spec.Theme.LoginTheme != "custom" {
		t.Errorf("loginTheme = %q, want the administrator's edit preserved", kcr.Spec.Theme.LoginTheme)
	}
	if kcr.Spec.PasswordPolicy == nil || *kcr.Spec.PasswordPolicy != "length(20)" {
		t.Errorf("passwordPolicy = %v, want the administrator's edit preserved", kcr.Spec.PasswordPolicy)
	}
}
