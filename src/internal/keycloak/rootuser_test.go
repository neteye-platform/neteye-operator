// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func TestEnsureRootUserDeclaresTheAccount(t *testing.T) {
	s := internalAdminScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureRootUser(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureRootUser: %v", err)
	}

	user := &neteye.KeycloakUser{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: RootResourceName}
	if err := c.Get(context.Background(), key, user); err != nil {
		t.Fatalf("the KeycloakUser was not created: %v", err)
	}
	if user.Spec.Username != RootUsername {
		t.Errorf("username = %q, want %q", user.Spec.Username, RootUsername)
	}
	if got := user.Spec.RealmRoles; len(got) != 1 || got[0] != RootRealmRole {
		t.Errorf("realmRoles = %v, want [%s]", got, RootRealmRole)
	}
	if user.Spec.Credential == nil || !user.Spec.Credential.Generate {
		t.Fatal("the account must own a generated credential")
	}
	if user.Spec.Credential.SecretRef.Name != RootSecretName {
		t.Errorf("secretRef.name = %q, want %q", user.Spec.Credential.SecretRef.Name, RootSecretName)
	}
	if user.Spec.Credential.SecretRef.Name == InternalAdminSecretName {
		t.Error("the root user must not share the internal admin Secret")
	}
	if user.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan", user.Spec.DeletionPolicy)
	}
}

func TestEnsureRootUserKeepsAdministratorEdits(t *testing.T) {
	s := internalAdminScheme(t)
	edited := &neteye.KeycloakUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: RootResourceName},
		Spec: neteye.KeycloakUserSpec{
			Username:   RootUsername,
			RealmRoles: []string{"admin", "offline_access"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(edited).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureRootUser(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureRootUser: %v", err)
	}

	user := &neteye.KeycloakUser{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: RootResourceName}
	if err := c.Get(context.Background(), key, user); err != nil {
		t.Fatal(err)
	}
	if len(user.Spec.RealmRoles) != 2 {
		t.Errorf("realmRoles = %v, want the administrator's edit preserved", user.Spec.RealmRoles)
	}
}
