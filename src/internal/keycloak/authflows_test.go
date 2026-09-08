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

func TestEnsureFirstBrokerLoginFlowDeclaresTheFlow(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureFirstBrokerLoginFlow(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureFirstBrokerLoginFlow: %v", err)
	}

	flow := &neteye.KeycloakAuthFlow{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: FirstBrokerLoginFlowResourceName}
	if err := c.Get(context.Background(), key, flow); err != nil {
		t.Fatalf("the KeycloakAuthFlow was not created: %v", err)
	}
	if flow.Spec.Alias != FirstBrokerLoginFlowResourceName {
		t.Errorf("alias = %q, want %q", flow.Spec.Alias, FirstBrokerLoginFlowResourceName)
	}
	if len(flow.Spec.Executions) != 2 {
		t.Fatalf("executions = %d, want 2", len(flow.Spec.Executions))
	}
	if got := flow.Spec.Executions[0].Authenticator; got != "idp-detect-existing-broker-user" {
		t.Errorf("first execution authenticator = %q, want idp-detect-existing-broker-user", got)
	}
	if flow.Spec.Executions[0].Config != nil {
		t.Errorf("idp-detect-existing-broker-user config = %+v, want none: this authenticator takes no configuration, so any value shipped here would be a placeholder written straight to Keycloak", flow.Spec.Executions[0].Config)
	}
	if got := flow.Spec.Executions[1].Authenticator; got != "idp-auto-link" {
		t.Errorf("second execution authenticator = %q, want idp-auto-link", got)
	}
	if flow.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan for a resource the operator redeclares", flow.Spec.DeletionPolicy)
	}
}

func TestEnsureFirstBrokerLoginFlowKeepsAdministratorEdits(t *testing.T) {
	edited := &neteye.KeycloakAuthFlow{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: FirstBrokerLoginFlowResourceName},
		Spec: neteye.KeycloakAuthFlowSpec{
			Alias:      FirstBrokerLoginFlowResourceName,
			Executions: []neteye.KeycloakAuthFlowExecution{{Authenticator: "idp-auto-link"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).WithObjects(edited).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureFirstBrokerLoginFlow(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureFirstBrokerLoginFlow: %v", err)
	}

	flow := &neteye.KeycloakAuthFlow{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: FirstBrokerLoginFlowResourceName}
	if err := c.Get(context.Background(), key, flow); err != nil {
		t.Fatal(err)
	}
	if len(flow.Spec.Executions) != 1 {
		t.Errorf("executions = %v, want the administrator's edit preserved", flow.Spec.Executions)
	}
}

func TestEnsureIdpDiscoveryFlowDeclaresTheFlow(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).Build()
	component := NewComponent(c, logr.Discard())

	if err := component.EnsureIdpDiscoveryFlow(context.Background(), WorkloadNamespace); err != nil {
		t.Fatalf("EnsureIdpDiscoveryFlow: %v", err)
	}

	flow := &neteye.KeycloakAuthFlow{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: IdpDiscoveryFlowResourceName}
	if err := c.Get(context.Background(), key, flow); err != nil {
		t.Fatalf("the KeycloakAuthFlow was not created: %v", err)
	}
	if len(flow.Spec.Executions) != 5 {
		t.Fatalf("executions = %d, want 5", len(flow.Spec.Executions))
	}
	blockLDAP := flow.Spec.Executions[3]
	if blockLDAP.Flow == nil || len(blockLDAP.Flow.Executions) != 2 {
		t.Fatalf("Block LDAP logins subflow = %+v, want 2 nested executions", blockLDAP.Flow)
	}
	usernamePassword := flow.Spec.Executions[4]
	if usernamePassword.Flow == nil || len(usernamePassword.Flow.Executions) != 1 {
		t.Fatalf("username-password subflow = %+v, want 1 nested execution", usernamePassword.Flow)
	}
	if flow.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan for a resource the operator redeclares", flow.Spec.DeletionPolicy)
	}
}
