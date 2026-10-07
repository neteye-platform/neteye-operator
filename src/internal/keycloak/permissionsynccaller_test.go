// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"k8s.io/utils/ptr"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

// permissionSyncCallerComponent wires a Component to the in-memory Admin API,
// with the bootstrap credential the provider resolves.
func permissionSyncCallerComponent(t *testing.T, keycloak *fakeKeycloak, objects ...*neteye.KeycloakClient) (*Component, *fakeKeycloak) {
	t.Helper()
	api := keycloak.start(t)
	builder := fake.NewClientBuilder().WithScheme(internalAdminScheme(t))
	for _, secret := range adminSecrets("temp-admin", "boot", "") {
		builder = builder.WithObjects(secret)
	}
	for _, object := range objects {
		builder = builder.WithObjects(object)
	}
	component := NewComponent(builder.Build(), logr.Discard())
	component.AdminAPIFactory = func(_ string, credentials AdminCredentials) *AdminAPI {
		return api
	}
	return component, keycloak
}

func callerClient(t *testing.T, component *Component) *neteye.KeycloakClient {
	t.Helper()
	client := &neteye.KeycloakClient{}
	key := types.NamespacedName{Namespace: WorkloadNamespace, Name: permissionsyncconfig.CallerClientResourceName}
	if err := component.client.Get(context.Background(), key, client); err != nil {
		t.Fatalf("the caller KeycloakClient was not declared: %v", err)
	}
	return client
}

func TestEnsurePermissionSyncCallerProvisionsScopesAndClient(t *testing.T) {
	component, server := permissionSyncCallerComponent(t, newFakeKeycloak(masterRealm))

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi", "neteye"}); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	for _, target := range []string{"glpi", "neteye"} {
		name := permissionsyncconfig.ScopeName(target)
		if _, exists := server.realmScopes[name]; !exists {
			t.Fatalf("client scope %q was not created; scopes = %v", name, server.realmScopes)
		}
	}
	if len(server.createdScopes) != 2 {
		t.Fatalf("created scopes = %d, want one per target", len(server.createdScopes))
	}
	for _, scope := range server.createdScopes {
		attributes, ok := scope["attributes"].(map[string]any)
		if !ok || attributes[clientScopeTokenScopeAttribute] != "true" {
			t.Errorf("scope %v must reach the token scope claim", scope)
		}
	}
	// The audience travels with the scope, so a token without a target does
	// not carry the PermissionSync audience either.
	mappers := server.mappers["scope-"+permissionsyncconfig.ScopeName("glpi")]
	if len(mappers) != 1 {
		t.Fatalf("scope mappers = %+v, want the audience mapper", mappers)
	}
	config, ok := mappers[0]["config"].(map[string]any)
	if !ok || config["included.custom.audience"] != permissionsyncconfig.Audience || config["access.token.claim"] != "true" {
		t.Errorf("audience mapper config = %v", mappers[0])
	}

	client := callerClient(t, component)
	if client.Spec.ClientID != permissionsyncconfig.CallerClientID || client.Spec.Realm != masterRealm {
		t.Errorf("client identity = %q in realm %q", client.Spec.ClientID, client.Spec.Realm)
	}
	if !client.Spec.AllowClientCredentialsGrant {
		t.Error("the caller authenticates through the client credentials grant")
	}
	if client.Spec.PublicClient || client.Spec.DirectAccess || client.Spec.StandardFlow == nil || *client.Spec.StandardFlow {
		t.Errorf("a technical caller must be confidential and browser-flow free: %+v", client.Spec)
	}
	if len(client.Spec.DefaultClientScopes) != 0 {
		t.Error("target scopes must never be default: one token must carry at most one permissionsync scope")
	}
	want := []string{permissionsyncconfig.ScopeName("glpi"), permissionsyncconfig.ScopeName("neteye")}
	if !slices.Equal(client.Spec.OptionalClientScopes, want) {
		t.Errorf("optionalClientScopes = %v, want %v", client.Spec.OptionalClientScopes, want)
	}
	if client.Spec.DeletionPolicy != neteye.KeycloakDeletionPolicyOrphan {
		t.Errorf("deletionPolicy = %q, want Orphan for a resource the operator redeclares", client.Spec.DeletionPolicy)
	}
	assertCallerAudienceMapper(t, client)
}

// assertCallerAudienceMapper checks the client itself carries both audiences.
// Observed on a real cluster: without the PermissionSync one a targetless
// token is refused with 401, and the fail-closed authenticator blocks every
// login without a target. The provider one is what the Permission Provider
// validates on the very token PermissionSync forwards to it.
func assertCallerAudienceMapper(t *testing.T, client *neteye.KeycloakClient) {
	t.Helper()
	for name, audience := range map[string]string{
		permissionsyncconfig.AudienceMapperName:         permissionsyncconfig.Audience,
		permissionsyncconfig.ProviderAudienceMapperName: permissionsyncconfig.ProviderAudience,
	} {
		found := false
		for _, mapper := range client.Spec.ProtocolMappers {
			if mapper.Name != name {
				continue
			}
			found = true
			if mapper.ProtocolMapper != "oidc-audience-mapper" || mapper.Config["included.custom.audience"] != audience || mapper.Config["access.token.claim"] != "true" {
				t.Errorf("audience mapper %q = %+v", name, mapper)
			}
		}
		if !found {
			t.Errorf("protocolMappers = %+v, want the %q audience mapper", client.Spec.ProtocolMappers, name)
		}
	}
	// The authenticator authenticates with the operator-generated secret, so
	// the client must be set to it.
	if ref := client.Spec.ClientSecretRef; ref == nil || *ref != callerClientSecretRef() {
		t.Errorf("clientSecretRef = %+v, want the operator-generated login-sync secret", ref)
	}
}

// TestEnsurePermissionSyncCallerRepairsAnAdoptedClientScope covers the silent
// failure a scope-only adoption would otherwise leave behind: a scope that does
// not reach the token's scope claim makes the receiver answer every request for
// that target as a targetless no-op, while the component still reports Ready.
func TestEnsurePermissionSyncCallerRepairsAnAdoptedClientScope(t *testing.T) {
	scope := permissionsyncconfig.ScopeName("glpi")
	server := newFakeKeycloak(masterRealm)
	id := server.seedClientScope(scope, representation{
		"protocol":    "saml",
		"description": "created by hand",
		"attributes": map[string]any{
			clientScopeTokenScopeAttribute: "false",
			"gui.order":                    "7",
		},
	})
	component, server := permissionSyncCallerComponent(t, server)

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"}); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	if len(server.createdScopes) != 0 {
		t.Error("an existing client scope must be adopted rather than recreated")
	}
	live := server.scopeReps[id]
	if live["protocol"] != openIDConnect {
		t.Errorf("protocol = %v, want %q", live["protocol"], openIDConnect)
	}
	attributes, ok := live["attributes"].(map[string]any)
	if !ok || attributes[clientScopeTokenScopeAttribute] != "true" {
		t.Errorf("attributes = %v, want the scope to reach the token scope claim", live["attributes"])
	}
	// Keys the operator does not declare belong to whoever set them.
	if attributes["gui.order"] != "7" {
		t.Errorf("attributes = %v, want an undeclared attribute preserved", attributes)
	}
}

func TestEnsurePermissionSyncCallerLeavesAConformingClientScopeAlone(t *testing.T) {
	scope := permissionsyncconfig.ScopeName("glpi")
	server := newFakeKeycloak(masterRealm)
	id := server.seedClientScope(scope, representation{
		"protocol":    openIDConnect,
		"description": permissionSyncScopeDescription,
		"attributes": map[string]any{
			clientScopeTokenScopeAttribute: "true",
			clientScopeConsentAttribute:    "false",
		},
	})
	component, server := permissionSyncCallerComponent(t, server)
	before := server.scopeReps[id]

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"}); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	if !reflect.DeepEqual(map[string]any(before), map[string]any(server.scopeReps[id])) {
		t.Errorf("a conforming client scope was rewritten: %v", server.scopeReps[id])
	}
	for _, path := range server.requestPaths {
		if path == http.MethodPut+" /admin/realms/"+masterRealm+"/client-scopes/"+id {
			t.Error("a conforming client scope must not be updated on every reconciliation")
		}
	}
}

func TestEnsurePermissionSyncCallerReconcilesAnEditedAudienceMapper(t *testing.T) {
	scope := permissionsyncconfig.ScopeName("glpi")
	server := newFakeKeycloak(masterRealm, scope)
	server.mappers["scope-"+scope] = []representation{{
		"id":             "mapper-" + permissionsyncconfig.AudienceMapperName,
		"name":           permissionsyncconfig.AudienceMapperName,
		"protocol":       openIDConnect,
		"protocolMapper": "oidc-audience-mapper",
		"config":         map[string]any{"included.custom.audience": "wrong", "access.token.claim": "false"},
	}}
	component, server := permissionSyncCallerComponent(t, server)

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"}); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	if len(server.createdScopes) != 0 {
		t.Error("an existing client scope must be reused rather than recreated")
	}
	mappers := server.mappers["scope-"+scope]
	if len(mappers) != 1 {
		t.Fatalf("scope mappers = %+v, want the audience mapper reconciled in place", mappers)
	}
	config, _ := mappers[0]["config"].(map[string]any)
	if config["included.custom.audience"] != permissionsyncconfig.Audience || config["access.token.claim"] != "true" {
		t.Errorf("audience mapper config = %v, want the declared audience restored", config)
	}
}

func TestEnsurePermissionSyncCallerExtendsScopesWithoutRevokingThem(t *testing.T) {
	existing := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: permissionsyncconfig.CallerClientResourceName},
		Spec: neteye.KeycloakClientSpec{
			ClientID:             permissionsyncconfig.CallerClientID,
			OptionalClientScopes: []string{permissionsyncconfig.ScopeName("retired"), permissionsyncconfig.ScopeName("glpi")},
		},
	}
	component, _ := permissionSyncCallerComponent(t, newFakeKeycloak(masterRealm), existing)

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi", "neteye"}); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	want := []string{permissionsyncconfig.ScopeName("retired"), permissionsyncconfig.ScopeName("glpi"), permissionsyncconfig.ScopeName("neteye")}
	client := callerClient(t, component)
	if got := client.Spec.OptionalClientScopes; !slices.Equal(got, want) {
		t.Errorf("optionalClientScopes = %v, want %v", got, want)
	}
	// A caller declared before the client carried the audience gets it added.
	assertCallerAudienceMapper(t, client)
}

func TestEnsurePermissionSyncCallerKeepsAnUpToDateClientUnchanged(t *testing.T) {
	existing := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: permissionsyncconfig.CallerClientResourceName},
		Spec: neteye.KeycloakClientSpec{
			ClientID:             permissionsyncconfig.CallerClientID,
			OptionalClientScopes: []string{permissionsyncconfig.ScopeName("glpi")},
			ProtocolMappers: append([]neteye.KeycloakProtocolMapper{
				{Name: "administrator-mapper", ProtocolMapper: "oidc-hardcoded-claim-mapper"},
			}, callerAudienceMappers()...),
			ClientSecretRef: ptr.To(callerClientSecretRef()),
		},
	}
	component, _ := permissionSyncCallerComponent(t, newFakeKeycloak(masterRealm), existing)
	before := callerClient(t, component).ResourceVersion

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"}); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	client := callerClient(t, component)
	if client.ResourceVersion != before {
		t.Error("an up-to-date caller must not be rewritten on every reconciliation")
	}
	if len(client.Spec.ProtocolMappers) != 3 {
		t.Errorf("protocolMappers = %+v, want the administrator's mapper kept", client.Spec.ProtocolMappers)
	}
}

func TestEnsurePermissionSyncCallerWithoutTargetsDeclaresTheClientOnly(t *testing.T) {
	component, server := permissionSyncCallerComponent(t, newFakeKeycloak(masterRealm))

	if _, _, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, nil); err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}

	if len(server.createdScopes) != 0 {
		t.Errorf("created scopes = %v, want none without a declared target", server.createdScopes)
	}
	if got := callerClient(t, component).Spec.OptionalClientScopes; len(got) != 0 {
		t.Errorf("optionalClientScopes = %v, want none", got)
	}
}

func TestIsClientReady(t *testing.T) {
	ready := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: permissionsyncconfig.CallerClientResourceName},
		Status:     neteye.KeycloakClientStatus{Status: neteye.ServiceStateReady},
	}
	pending := &neteye.KeycloakClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: WorkloadNamespace, Name: "pending"},
		Status:     neteye.KeycloakClientStatus{Status: neteye.ServiceStateNotReady, Message: "reconciling"},
	}
	c := fake.NewClientBuilder().WithScheme(internalAdminScheme(t)).WithObjects(ready, pending).Build()
	component := NewComponent(c, logr.Discard())

	for _, test := range []struct {
		name      string
		resource  string
		wantReady bool
	}{
		{"ready", permissionsyncconfig.CallerClientResourceName, true},
		{"not ready", "pending", false},
		{"absent", "missing", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, message, err := component.IsClientReady(context.Background(), WorkloadNamespace, test.resource)
			if err != nil {
				t.Fatalf("IsClientReady: %v", err)
			}
			if got != test.wantReady {
				t.Errorf("ready = %t, want %t (message %q)", got, test.wantReady, message)
			}
			if !got && message == "" {
				t.Error("a component that is not ready must explain why")
			}
		})
	}
}

// TestEnsurePermissionSyncCallerBindsTheFlowToTargetClientsOnly pins the
// blast radius of the fail-closed authenticator: only a target client runs the
// PermissionSync flow, and a client that stopped being a target goes back to
// Keycloak's own browser flow instead of failing every login.
func TestEnsurePermissionSyncCallerBindsTheFlowToTargetClientsOnly(t *testing.T) {
	server := newFakeKeycloak(masterRealm)
	server.flows[permissionsyncconfig.BrowserFlowAlias] = "flow-permissionsync"
	server.clients["uuid-glpi"] = representation{"clientId": "glpi"}
	server.clients["uuid-neteye"] = representation{"clientId": "neteye"}
	server.clients["uuid-retired"] = representation{"clientId": "retired", "authenticationFlowBindingOverrides": map[string]any{"browser": "flow-permissionsync", "direct_grant": "flow-kept"}}
	component, server := permissionSyncCallerComponent(t, server)

	ready, message, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"})
	if err != nil {
		t.Fatalf("EnsurePermissionSyncCaller: %v", err)
	}
	if !ready {
		t.Fatalf("not ready: %s", message)
	}
	if got := browserFlowOverride(server.clients["uuid-glpi"]); got != "flow-permissionsync" {
		t.Errorf("glpi browser flow = %q, want the PermissionSync flow", got)
	}
	if got := browserFlowOverride(server.clients["uuid-neteye"]); got != "" {
		t.Errorf("neteye browser flow = %q, want Keycloak's own: it is not a target", got)
	}
	retired, _ := server.clients["uuid-retired"]["authenticationFlowBindingOverrides"].(map[string]any)
	if retired["browser"] != nil || retired["direct_grant"] != "flow-kept" {
		t.Errorf("retired overrides = %v, want only the PermissionSync binding removed", retired)
	}

	flow := &neteye.KeycloakAuthFlow{}
	if err := component.client.Get(context.Background(), types.NamespacedName{Namespace: WorkloadNamespace, Name: PermissionSyncFlowResourceName}, flow); err != nil {
		t.Fatalf("the PermissionSync flow was not declared: %v", err)
	}
	if len(flow.Spec.Bindings) != 0 {
		t.Errorf("bindings = %v, want none: binding the realm would lock out every client", flow.Spec.Bindings)
	}
}

func TestEnsurePermissionSyncCallerWaitsForTheFlowAndTheTargetClient(t *testing.T) {
	component, _ := permissionSyncCallerComponent(t, newFakeKeycloak(masterRealm))
	ready, message, err := component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"})
	if err != nil || ready || !strings.Contains(message, permissionsyncconfig.BrowserFlowAlias) {
		t.Errorf("without the flow: ready=%t message=%q err=%v, want waiting for the flow", ready, message, err)
	}

	server := newFakeKeycloak(masterRealm)
	server.flows[permissionsyncconfig.BrowserFlowAlias] = "flow-permissionsync"
	component, _ = permissionSyncCallerComponent(t, server)
	ready, message, err = component.EnsurePermissionSyncCaller(context.Background(), WorkloadNamespace, []string{"glpi"})
	if err != nil || ready || !strings.Contains(message, `"glpi"`) {
		t.Errorf("without the target client: ready=%t message=%q err=%v, want waiting for it", ready, message, err)
	}
}

// TestPermissionSyncFlowWrapsTheBrowserFlow checks the structure Keycloak
// needs: authentication alternatives inside a REQUIRED subflow, because a
// REQUIRED sibling would make Keycloak ignore them, then login-sync, with the
// conditional second factor kept.
func TestPermissionSyncFlowWrapsTheBrowserFlow(t *testing.T) {
	spec := permissionSyncFlowSpec()
	if len(spec.Executions) != 2 {
		t.Fatalf("top-level executions = %d, want the authentication subflow and login-sync", len(spec.Executions))
	}
	authentication, sync := spec.Executions[0], spec.Executions[1]
	if authentication.Requirement != "REQUIRED" || authentication.Flow == nil {
		t.Errorf("first execution = %+v, want a REQUIRED subflow", authentication)
	}
	if sync.Requirement != "REQUIRED" || sync.Authenticator != loginSyncAuthenticator {
		t.Errorf("second execution = %+v, want REQUIRED login-sync", sync)
	}
	var otp bool
	for _, l2 := range authentication.Flow.Executions {
		if l2.Requirement == "REQUIRED" {
			t.Errorf("level-2 execution %+v must not be REQUIRED: the alternatives would be ignored", l2)
		}
		if l2.Flow == nil {
			continue
		}
		for _, l3 := range l2.Flow.Executions {
			if l3.Flow == nil {
				continue
			}
			for _, l4 := range l3.Flow.Executions {
				if l4.Authenticator == "auth-otp-form" {
					otp = true
				}
			}
		}
	}
	if !otp {
		t.Error("the conditional second factor was dropped")
	}
}
