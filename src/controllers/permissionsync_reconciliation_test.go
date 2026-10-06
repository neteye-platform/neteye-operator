// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/permissionsync"
)

func permissionSyncScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatalf("add neteye scheme: %v", err)
	}
	return s
}

func netEyeWithPermissionSync(spec neteye.NetEyePermissionSyncSpec) *neteye.NetEye {
	ne := &neteye.NetEye{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: neteye.NetEyeNamespace, UID: "owner"},
		Spec: neteye.NetEyeSpec{
			Version:        neteye.CurrentNetEyeVersion,
			Identity:       neteye.NetEyeIdentitySpec{Hostname: "keycloak.example.com"},
			PermissionSync: spec,
		},
	}
	ne.Status.ServicesStatus.PermissionSync = &neteye.NetEyeServiceStatus{Status: neteye.ServiceStateUnknown}
	return ne
}

// TestReconcilePermissionSyncRefusesAnUnusableConfiguration proves the
// component's own validation reaches the NetEye status, and that no Keycloak
// provisioning or workload happens for a configuration that cannot work.
func TestReconcilePermissionSyncRefusesAnUnusableConfiguration(t *testing.T) {
	ne := netEyeWithPermissionSync(neteye.NetEyePermissionSyncSpec{
		Targets: []neteye.NetEyePermissionSyncTarget{{LogicalTarget: "glpi", Adapter: neteye.PermissionSyncGLPIAdapter}},
	})
	c := fake.NewClientBuilder().WithScheme(permissionSyncScheme(t)).WithObjects(ne).Build()
	r := &NetEyeReconciler{
		Client:                  c,
		Log:                     logr.Discard(),
		KeycloakComponent:       keycloak.NewComponent(c, logr.Discard()),
		PermissionSyncComponent: permissionsync.NewComponent(c),
	}

	result, err := r.reconcilePermissionSync(context.Background(), ne, "image")
	if err != nil {
		t.Fatalf("reconcilePermissionSync returned a systemic error: %v", err)
	}
	if result.State != componentStateDegraded || result.Reason != permissionsync.ReasonInvalidConfiguration {
		t.Fatalf("result = %+v, want degraded with an invalid configuration", result)
	}
	if status := ne.Status.ServicesStatus.PermissionSync; status == nil || status.Status != neteye.ServiceStateFailed || status.ResolvedImage != "image" {
		t.Errorf("status = %+v, want Failed with the resolved image reported", status)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: keycloak.WorkloadNamespace, Name: permissionsync.ConfigSecretName}, &corev1.Secret{}); err == nil {
		t.Error("a document was rendered for a refused configuration")
	}
	// Keycloak objects must not be provisioned from a configuration this
	// reconciliation rejects: the operator never removes them again.
	callers := &neteye.KeycloakClientList{}
	if err := c.List(context.Background(), callers); err != nil {
		t.Fatal(err)
	}
	if len(callers.Items) != 0 {
		t.Errorf("KeycloakClients = %d, want none declared for a refused configuration", len(callers.Items))
	}
}

func TestMapPermissionSyncOutcome(t *testing.T) {
	failure := errors.New("boom")
	for _, test := range []struct {
		name    string
		outcome permissionsync.Outcome
		state   componentState
		wantErr bool
	}{
		{name: "ready", outcome: permissionsync.Outcome{Phase: permissionsync.PhaseReady, Reason: permissionsync.ReasonAvailable}, state: componentStateReady},
		{name: "progressing", outcome: permissionsync.Outcome{Phase: permissionsync.PhaseProgressing, Reason: permissionsync.ReasonDeploymentNotAvailable}, state: componentStateProgressing},
		{name: "degraded", outcome: permissionsync.Outcome{Phase: permissionsync.PhaseDegraded, Reason: permissionsync.ReasonReconcileFailed, Err: failure}, state: componentStateDegraded},
		{name: "unknown phase", outcome: permissionsync.Outcome{Phase: "Surprise"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := mapPermissionSyncOutcome(test.outcome, DefaultWaitForProgressingRequeueAfter, DefaultFailureRequeueAfter)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if result.ID != permissionSyncComponentID || result.State != test.state {
				t.Errorf("result = %+v, want %q in state %q", result, permissionSyncComponentID, test.state)
			}
		})
	}
}

func TestPermissionSyncServiceState(t *testing.T) {
	for phase, want := range map[permissionsync.Phase]neteye.ServiceState{
		permissionsync.PhaseReady:       neteye.ServiceStateReady,
		permissionsync.PhaseProgressing: neteye.ServiceStateNotReady,
		permissionsync.PhaseDegraded:    neteye.ServiceStateFailed,
		permissionsync.Phase("other"):   neteye.ServiceStateFailed,
	} {
		if got := permissionSyncServiceState(phase); got != want {
			t.Errorf("permissionSyncServiceState(%q) = %q, want %q", phase, got, want)
		}
	}
}

// TestPermissionSyncWaitsForTheIdentityComponent pins the dependency: the
// caller provisioning needs a reachable Admin API, so PermissionSync must not
// be reconciled before the identity component is ready.
func TestPermissionSyncWaitsForTheIdentityComponent(t *testing.T) {
	graph, err := newLifecycleGraph([]lifecycleNode{
		{ID: identityComponentID},
		{ID: permissionSyncComponentID, Dependencies: []componentID{identityComponentID}},
	})
	if err != nil {
		t.Fatalf("newLifecycleGraph: %v", err)
	}
	blocking, err := graph.blockingDependencies(permissionSyncComponentID, map[componentID]componentResult{
		identityComponentID: {ID: identityComponentID, State: componentStateProgressing},
	})
	if err != nil {
		t.Fatalf("blockingDependencies: %v", err)
	}
	if len(blocking) != 1 || blocking[0] != identityComponentID {
		t.Errorf("blocking = %v, want the identity component", blocking)
	}
}
