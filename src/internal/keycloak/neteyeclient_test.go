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

func TestEnsureNetEyeClientDeclaresTheClient(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureNetEyeClient(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureNetEyeClient: %v", err)
	}

	kcc := &neteye.KeycloakClient{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: NetEyeClientResourceName}
	if err := c.Get(context.Background(), key, kcc); err != nil {
		t.Fatalf("the KeycloakClient was not created: %v", err)
	}
	if kcc.Spec.ClientID != NetEyeClientID {
		t.Errorf("clientId = %q, want %q", kcc.Spec.ClientID, NetEyeClientID)
	}
	if kcc.Spec.PublicClient {
		t.Error("the NetEye client is confidential")
	}
	if !kcc.Spec.DirectAccess {
		t.Error("direct access grants must be enabled")
	}
	if kcc.Spec.ServiceAccount == nil || !kcc.Spec.ServiceAccount.Enabled {
		t.Fatal("the service account must be enabled")
	}
	roles := kcc.Spec.ServiceAccount.ClientRoles["master-realm"]
	if len(roles) != 3 {
		t.Errorf("master-realm roles = %v, want view-users, query-users and query-groups", roles)
	}
	if got := kcc.Spec.RedirectUris; len(got) != 1 || got[0] != "/neteye/*" {
		t.Errorf("redirectUris = %v, want [/neteye/*]", got)
	}
	if len(kcc.Spec.ProtocolMappers) != 1 {
		t.Fatalf("protocolMappers = %d, want the group membership mapper", len(kcc.Spec.ProtocolMappers))
	}
	mapper := kcc.Spec.ProtocolMappers[0]
	if mapper.ProtocolMapper != "oidc-group-membership-mapper" || mapper.Config["claim.name"] != "groups" {
		t.Errorf("mapper = %+v, want the group membership mapper on the groups claim", mapper)
	}
	if kcc.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan for a resource the operator redeclares", kcc.Spec.DeletionPolicy)
	}
	if kcc.Spec.SecretRef != nil {
		t.Error("no secret reference: the Keycloak-generated client secret is left alone")
	}
}

func TestEnsureNetEyeClientKeepsAdministratorEdits(t *testing.T) {
	edited := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: NetEyeClientResourceName},
		Spec: neteye.KeycloakClientSpec{
			ClientID:     NetEyeClientID,
			RedirectUris: []string{"/neteye/*", "/custom/*"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).WithObjects(edited).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureNetEyeClient(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureNetEyeClient: %v", err)
	}

	kcc := &neteye.KeycloakClient{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: NetEyeClientResourceName}
	if err := c.Get(context.Background(), key, kcc); err != nil {
		t.Fatal(err)
	}
	if len(kcc.Spec.RedirectUris) != 2 {
		t.Errorf("redirectUris = %v, want the administrator's edit preserved", kcc.Spec.RedirectUris)
	}
}
