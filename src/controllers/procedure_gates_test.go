// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

var (
	gateNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	keycloakGVK     = schema.GroupVersionKind{Group: "k8s.keycloak.org", Version: "v2beta1", Kind: "Keycloak"}
)

func TestDefaultReleasePlansAreValid(t *testing.T) {
	known := map[componentID]bool{}
	for _, node := range lifecycleNodesFor(&neteye.NetEye{Spec: neteye.NetEyeSpec{ElasticStack: &neteye.NetEyeElasticStackSpec{Enabled: true}}}) {
		known[node.ID] = true
	}
	for version, release := range defaultReleasePlans {
		if !neteye.IsLatestVersion(version) {
			t.Errorf("release plan targets %q, want only the current release %q", version, neteye.CurrentNetEyeVersion)
		}
		for procedure, plan := range map[lifecycle]gatePlan{lifecycleInstall: release.Install, lifecycleUpgrade: release.Upgrade} {
			seen := map[string]componentID{}
			for id, step := range plan {
				if !known[id] {
					t.Errorf("%s %s plan references unknown component %q", version, procedure, id)
				}
				for _, gate := range append(append([]string(nil), step.Requires...), step.Produces...) {
					if len(gate) > 63 || !gateNamePattern.MatchString(gate) {
						t.Errorf("%s %s plan component %s has invalid gate %q", version, procedure, id, gate)
					}
					if owner, ok := seen[gate]; ok {
						t.Errorf("%s %s plan uses gate %q in both %s and %s", version, procedure, gate, owner, id)
					}
					seen[gate] = id
				}
			}
		}
	}
}

func TestLifecycleFor(t *testing.T) {
	tests := []struct {
		currentVersion string
		want           lifecycle
		wantOK         bool
	}{
		{currentVersion: "", want: lifecycleInstall, wantOK: true},
		{currentVersion: neteye.PreviousNetEyeVersion, want: lifecycleUpgrade, wantOK: true},
		{currentVersion: neteye.CurrentNetEyeVersion, want: lifecycleSteady, wantOK: true},
		{currentVersion: "4.10", want: lifecycleSteady, wantOK: false},
	}
	for _, tt := range tests {
		ne := newNetEye(neteye.CurrentNetEyeVersion)
		ne.Status.CurrentVersion = tt.currentVersion
		if got, ok := lifecycleFor(ne); got != tt.want || ok != tt.wantOK {
			t.Errorf("lifecycleFor(currentVersion=%q) = %q/%t, want %q/%t", tt.currentVersion, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestGatePlanFor(t *testing.T) {
	install := gatePlan{identityComponentID: {Requires: []string{"install"}}}
	upgrade := gatePlan{identityComponentID: {Requires: []string{"upgrade"}}}
	r := &NetEyeReconciler{releasePlans: map[string]releasePlan{"4.51": {Install: install, Upgrade: upgrade}}}
	if got := r.gatePlanFor("4.51", lifecycleInstall); !reflect.DeepEqual(got, install) {
		t.Errorf("install plan = %v, want %v", got, install)
	}
	if got := r.gatePlanFor("4.51", lifecycleUpgrade); !reflect.DeepEqual(got, upgrade) {
		t.Errorf("upgrade plan = %v, want %v", got, upgrade)
	}
	if got := r.gatePlanFor("4.51", lifecycleSteady); got != nil {
		t.Errorf("steady plan = %v, want none", got)
	}
}

func TestReconcileRejectsUnsupportedUpgradeSource(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	ne.Status.CurrentVersion = "4.10"
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s, KeycloakComponent: keycloak.NewComponent(c, logr.Discard()), OTelCollectorComponent: elasticstack.NewOTelCollectorComponent(c), EDOTGatewayComponent: elasticstack.NewEDOTGatewayComponent(c)}

	result, err := reconcileNetEye(t, r, ne)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != DefaultFailureRequeueAfter {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, DefaultFailureRequeueAfter)
	}
	got := &neteye.NetEye{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ne), got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != neteye.PhaseFailed || !strings.Contains(got.Status.Message, "4.10") {
		t.Errorf("status = %q %q, want Failed naming the unsupported source", got.Status.Phase, got.Status.Message)
	}
	if got.Status.CurrentVersion != "4.10" {
		t.Errorf("currentVersion = %q, want it left unchanged", got.Status.CurrentVersion)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: keycloak.WorkloadNamespace, Name: clusterAuthorityLeaseName}, &coordinationv1.Lease{}); !apierrors.IsNotFound(err) {
		t.Errorf("cluster authority lease lookup = %v, want not found", err)
	}
}

func TestProcedureGatesMatchVersion(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	gate := func(namespace, name, version, key string) *neteye.NetEyeProcedureGate {
		return &neteye.NetEyeProcedureGate{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
			Spec:       neteye.NetEyeProcedureGateSpec{Version: version, Gate: key, Producer: neteye.ProcedureGateProducerAnsible},
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		gate(ne.Namespace, "current", neteye.CurrentNetEyeVersion, "db-schema"),
		gate(ne.Namespace, "hand-made", neteye.CurrentNetEyeVersion, "manual"),
		gate(ne.Namespace, "old", neteye.PreviousNetEyeVersion, "old-release"),
		gate("elsewhere", "other-namespace", neteye.CurrentNetEyeVersion, "foreign"),
	).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}

	gates, err := r.procedureGates(context.Background(), ne)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{"db-schema": {}, "manual": {}}
	if !reflect.DeepEqual(gates, want) {
		t.Errorf("gates = %v, want %v", gates, want)
	}
}

func gatedTestNetEye(elasticStack bool) *neteye.NetEye {
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	ne.UID = "platform-uid"
	ne.Spec.ElasticStack = &neteye.NetEyeElasticStackSpec{Enabled: elasticStack}
	ne.Status.ServicesStatus = neteye.NetEyeServicesStatus{
		Identity:     &neteye.NetEyeServiceStatus{Status: neteye.ServiceStateUnknown},
		ElasticStack: &neteye.NetEyeElasticStackStatus{OTelCollector: &neteye.NetEyeServiceStatus{}, EDOTGateway: &neteye.NetEyeServiceStatus{}},
	}
	return ne
}

func TestGatedOperation(t *testing.T) {
	ready := func(id componentID) componentOperation {
		return func() (componentResult, error) { return readyResult(id, "Available", "ready") }
	}
	tests := []struct {
		name         string
		elastic      bool
		ignoreGates  bool
		id           componentID
		step         componentGates
		present      []string
		op           componentOperation
		prepareErr   error
		wantState    componentState
		wantReason   string
		wantRan      bool
		wantPrepared bool
		wantGates    []string
	}{
		{
			name: "missing gate holds the component", id: identityComponentID,
			step:      componentGates{Requires: []string{"db-schema", "realm-export"}, Produces: []string{"identity-done"}},
			present:   []string{"realm-export"},
			wantState: componentStateProgressing, wantReason: waitingForProcedureGatesReason,
		},
		{
			name: "present gates prepare, reconcile and signal", id: identityComponentID,
			step:      componentGates{Requires: []string{"db-schema"}, Produces: []string{"identity-done"}},
			present:   []string{"db-schema"},
			wantState: componentStateReady, wantReason: "Available", wantRan: true, wantPrepared: true, wantGates: []string{"identity-done"},
		},
		{
			name: "ignored gates neither hold nor signal", id: identityComponentID, ignoreGates: true,
			step:      componentGates{Requires: []string{"db-schema"}, Produces: []string{"identity-done"}},
			wantState: componentStateReady, wantReason: "Available", wantRan: true, wantPrepared: true,
		},
		{
			name: "unready component does not signal", id: identityComponentID,
			step: componentGates{Produces: []string{"identity-done"}},
			op: func() (componentResult, error) {
				return progressingResult(identityComponentID, "ServiceNotReady", "starting", 0)
			},
			wantState: componentStateProgressing, wantReason: "ServiceNotReady", wantRan: true, wantPrepared: true,
		},
		{
			name: "failed preparation degrades without reconciling", id: identityComponentID,
			step:       componentGates{Produces: []string{"identity-done"}},
			prepareErr: errors.New("boom"),
			wantState:  componentStateDegraded, wantReason: procedurePrepareFailedReason, wantPrepared: true,
		},
		{
			name: "disabled component skips its gates", id: otelCollectorComponentID,
			step:      componentGates{Requires: []string{"collector-config"}, Produces: []string{"collector-done"}},
			wantState: componentStateReady, wantReason: "Available", wantRan: true,
		},
		{
			name: "gate that already exists is not recreated", id: edotGatewayComponentID, elastic: true,
			step:      componentGates{Produces: []string{"gateway-done"}},
			present:   []string{"gateway-done"},
			wantState: componentStateReady, wantReason: "Available", wantRan: true, wantPrepared: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testScheme(t)
			c := fake.NewClientBuilder().WithScheme(s).Build()
			r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}
			ne := gatedTestNetEye(tt.elastic)
			var present map[string]struct{}
			if !tt.ignoreGates {
				present = map[string]struct{}{}
				for _, gate := range tt.present {
					present[gate] = struct{}{}
				}
			}
			prepared := false
			step := tt.step
			step.Prepare = func(context.Context, *NetEyeReconciler, *neteye.NetEye) error {
				prepared = true
				return tt.prepareErr
			}
			ran := false
			op := tt.op
			if op == nil {
				op = ready(tt.id)
			}
			wrapped := r.gatedOperation(context.Background(), ne, tt.id, "image", step, !tt.ignoreGates, present, func() (componentResult, error) {
				ran = true
				return op()
			})

			result, err := wrapped()
			if err != nil {
				t.Fatal(err)
			}
			if err := validateOperationResult(tt.id, result); err != nil {
				t.Fatal(err)
			}
			if result.State != tt.wantState || result.Reason != tt.wantReason {
				t.Errorf("result = %s/%s, want %s/%s", result.State, result.Reason, tt.wantState, tt.wantReason)
			}
			if ran != tt.wantRan {
				t.Errorf("component reconciled = %t, want %t", ran, tt.wantRan)
			}
			if prepared != tt.wantPrepared {
				t.Errorf("preparation ran = %t, want %t", prepared, tt.wantPrepared)
			}
			if tt.wantReason == waitingForProcedureGatesReason {
				if status := ne.Status.ServicesStatus.Identity; status.Status != neteye.ServiceStateNotReady || !strings.Contains(status.Message, "db-schema") || strings.Contains(status.Message, "realm-export") {
					t.Errorf("identity status = %+v, want NotReady naming only the missing gate", status)
				}
			}

			list := &neteye.NetEyeProcedureGateList{}
			if err := c.List(context.Background(), list); err != nil {
				t.Fatal(err)
			}
			var created []string
			for _, gate := range list.Items {
				created = append(created, gate.Spec.Gate)
				if gate.Name != procedureGateName(neteye.CurrentNetEyeVersion, gate.Spec.Gate) || gate.Namespace != ne.Namespace {
					t.Errorf("gate stored as %s/%s", gate.Namespace, gate.Name)
				}
				if gate.Spec.Producer != neteye.ProcedureGateProducerOperator || gate.Spec.Version != neteye.CurrentNetEyeVersion {
					t.Errorf("gate spec = %+v, want an operator gate for %s", gate.Spec, neteye.CurrentNetEyeVersion)
				}
				if owner := metav1.GetControllerOf(&gate); owner == nil || owner.UID != ne.UID {
					t.Errorf("gate owner = %+v, want the NetEye", owner)
				}
			}
			if !reflect.DeepEqual(created, tt.wantGates) {
				t.Errorf("created gates = %v, want %v", created, tt.wantGates)
			}
		})
	}
}

func TestLifecyclePhase(t *testing.T) {
	waiting := map[componentID]componentResult{
		otelCollectorComponentID: {Reason: waitingForProcedureGatesReason},
		identityComponentID:      {Reason: waitingForProcedureGatesReason},
		edotGatewayComponentID:   {Reason: "Available"},
	}
	tests := []struct {
		name        string
		procedure   lifecycle
		phase       neteye.NetEyePhase
		results     map[componentID]componentResult
		wantPhase   neteye.NetEyePhase
		wantMessage string
		wantVersion string
	}{
		{name: "steady state is unchanged", procedure: lifecycleSteady, phase: neteye.PhaseNotReady, wantPhase: neteye.PhaseNotReady, wantMessage: "aggregate"},
		{name: "ready completes the procedure", procedure: lifecycleUpgrade, phase: neteye.PhaseReady, wantPhase: neteye.PhaseReady, wantMessage: "aggregate", wantVersion: neteye.CurrentNetEyeVersion},
		{name: "failure is preserved", procedure: lifecycleUpgrade, phase: neteye.PhaseFailed, wantPhase: neteye.PhaseFailed, wantMessage: "aggregate"},
		{
			name: "unfinished install names the waiting components", procedure: lifecycleInstall, phase: neteye.PhaseNotReady, results: waiting,
			wantPhase: neteye.PhaseInstalling, wantMessage: "Installing NetEye " + neteye.CurrentNetEyeVersion + "; waiting for procedure gates of: identity, otel-collector",
		},
		{
			name: "unfinished upgrade points to services status", procedure: lifecycleUpgrade, phase: neteye.PhaseNotReady,
			wantPhase: neteye.PhaseUpgrading, wantMessage: "Upgrading to NetEye " + neteye.CurrentNetEyeVersion + "; check services status for details",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ne := newNetEye(neteye.CurrentNetEyeVersion)
			phase, message := lifecyclePhase(ne, tt.procedure, tt.phase, "aggregate", tt.results)
			if phase != tt.wantPhase || message != tt.wantMessage {
				t.Errorf("phase = %q %q, want %q %q", phase, message, tt.wantPhase, tt.wantMessage)
			}
			if ne.Status.CurrentVersion != tt.wantVersion {
				t.Errorf("currentVersion = %q, want %q", ne.Status.CurrentVersion, tt.wantVersion)
			}
		})
	}
}

func TestNetEyesForProcedureGate(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	other := newNetEye(neteye.CurrentNetEyeVersion)
	other.Namespace = "elsewhere"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ne, other).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}

	requests := r.netEyesForProcedureGate(context.Background(), &neteye.NetEyeProcedureGate{ObjectMeta: metav1.ObjectMeta{Namespace: ne.Namespace, Name: "gate"}})
	if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(ne) {
		t.Errorf("requests = %v, want only %s", requests, client.ObjectKeyFromObject(ne))
	}
}

func createProcedureGate(ctx context.Context, t *testing.T, c client.Client, namespace, gate string) {
	t.Helper()
	if err := c.Create(ctx, &neteye.NetEyeProcedureGate{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: gate},
		Spec:       neteye.NetEyeProcedureGateSpec{Version: neteye.CurrentNetEyeVersion, Gate: gate, Producer: neteye.ProcedureGateProducerAnsible},
	}); err != nil {
		t.Fatal(err)
	}
}

func getNetEyeObject(ctx context.Context, t *testing.T, c client.Client, key client.ObjectKey) *neteye.NetEye {
	t.Helper()
	got := &neteye.NetEye{}
	if err := c.Get(ctx, key, got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The identity service cannot become fully ready under envtest (disabling the
// bootstrap admin needs a live Keycloak), so completion is covered by
// TestLifecyclePhase.
func TestReconcileInstallWaitsForGatesAgainstAPIServer(t *testing.T) {
	c, s := startEnvtest(t)
	ctx := context.Background()
	namespace := keycloak.WorkloadNamespace
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, namespace, "internal-issuer")); err != nil {
		t.Fatal(err)
	}
	ne := &neteye.NetEye{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: namespace},
		Spec: neteye.NetEyeSpec{
			Version:                      neteye.CurrentNetEyeVersion,
			InternalCertificateIssuerRef: "internal-issuer",
			Gateway:                      neteye.NetEyeGatewaySpec{Name: "neteye", ClassName: "cilium"},
			Identity: neteye.NetEyeIdentitySpec{
				Hostname:     "keycloak.example.com",
				DBConnection: neteye.NetEyeDBConnectionSpec{Host: "mariadb.example.com"},
			},
		},
	}
	if err := c.Create(ctx, ne); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(ne)
	if got := getNetEyeObject(ctx, t, c, key); got.Spec.UseProcedureGates == nil || !*got.Spec.UseProcedureGates {
		t.Fatalf("useProcedureGates = %v, want the API server to default it to true", got.Spec.UseProcedureGates)
	}
	r := &NetEyeReconciler{
		Client: c, Log: logr.Discard(), Scheme: s,
		KeycloakComponent: keycloak.NewComponent(c, logr.Discard()), OTelCollectorComponent: elasticstack.NewOTelCollectorComponent(c), EDOTGatewayComponent: elasticstack.NewEDOTGatewayComponent(c),
		releasePlans: map[string]releasePlan{neteye.CurrentNetEyeVersion: {Install: gatePlan{
			identityComponentID: {Requires: []string{"keycloak-db-configured"}},
		}}},
	}
	request := ctrl.Request{NamespacedName: key}

	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	got := getNetEyeObject(ctx, t, c, key)
	if got.Status.Phase != neteye.PhaseInstalling || got.Status.CurrentVersion != "" {
		t.Fatalf("status = %q/%q (%s), want Installing with no current version", got.Status.Phase, got.Status.CurrentVersion, got.Status.Message)
	}
	if identity := got.Status.ServicesStatus.Identity; identity.Status != neteye.ServiceStateNotReady || !strings.Contains(identity.Message, "keycloak-db-configured") {
		t.Errorf("identity status = %+v, want NotReady waiting for keycloak-db-configured", identity)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: keycloak.TLSCertificateName}, newUnstructured(certificateGVK, "", "")); !apierrors.IsNotFound(err) {
		t.Fatalf("identity certificate lookup = %v, want identity untouched before its gate", err)
	}

	createProcedureGate(ctx, t, c, namespace, "keycloak-db-configured")
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	got = getNetEyeObject(ctx, t, c, key)
	if identity := got.Status.ServicesStatus.Identity; strings.Contains(identity.Message, "procedure gates") {
		t.Errorf("identity status = %+v, want it reconciled once the gate exists", identity)
	}
	if got.Status.Phase != neteye.PhaseInstalling {
		t.Errorf("phase = %q, want Installing until every component is ready", got.Status.Phase)
	}
	requireExists(ctx, t, c, certificateGVK, namespace, keycloak.TLSCertificateName)
}

func TestReconcileUpgradeHonorsUseProcedureGatesAgainstAPIServer(t *testing.T) {
	for _, useGates := range []bool{true, false} {
		t.Run(map[bool]string{true: "gates honored", false: "gates ignored"}[useGates], func(t *testing.T) {
			c, _, ctx, ne, r := readyElasticStackTestPlatform(t, nil)
			key := client.ObjectKeyFromObject(ne)
			request := ctrl.Request{NamespacedName: key}

			// Simulate an installation coming from the previous release.
			upgrading := getNetEyeObject(ctx, t, c, key)
			upgrading.Spec.UseProcedureGates = ptr.To(useGates)
			if err := c.Update(ctx, upgrading); err != nil {
				t.Fatal(err)
			}
			upgrading.Status.CurrentVersion = neteye.PreviousNetEyeVersion
			if err := c.Status().Update(ctx, upgrading); err != nil {
				t.Fatal(err)
			}
			r.releasePlans = map[string]releasePlan{neteye.CurrentNetEyeVersion: {Upgrade: gatePlan{
				identityComponentID: {Requires: []string{"keycloak-db-schema"}},
			}}}
			drifted := requireExists(ctx, t, c, keycloakGVK, keycloak.WorkloadNamespace, keycloak.InstanceName)
			if err := unstructured.SetNestedField(drifted.Object, int64(3), "spec", "instances"); err != nil {
				t.Fatal(err)
			}
			if err := c.Update(ctx, drifted); err != nil {
				t.Fatal(err)
			}

			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatal(err)
			}
			got := getNetEyeObject(ctx, t, c, key)
			if got.Status.Phase != neteye.PhaseUpgrading || got.Status.CurrentVersion != neteye.PreviousNetEyeVersion {
				t.Fatalf("status = %q/%q, want Upgrading from %s", got.Status.Phase, got.Status.CurrentVersion, neteye.PreviousNetEyeVersion)
			}
			held := requireExists(ctx, t, c, keycloakGVK, keycloak.WorkloadNamespace, keycloak.InstanceName)
			instances, _, _ := unstructured.NestedInt64(held.Object, "spec", "instances")
			if useGates && instances != 3 {
				t.Errorf("Keycloak instances = %d, want the held workload left untouched", instances)
			}
			if !useGates && instances == 3 {
				t.Error("Keycloak was held although procedure gates are disabled")
			}
			if !useGates {
				return
			}

			createProcedureGate(ctx, t, c, ne.Namespace, "keycloak-db-schema")
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatal(err)
			}
			reconciled := requireExists(ctx, t, c, keycloakGVK, keycloak.WorkloadNamespace, keycloak.InstanceName)
			if instances, _, _ := unstructured.NestedInt64(reconciled.Object, "spec", "instances"); instances == 3 {
				t.Error("Keycloak was not reconciled once its gate existed")
			}
		})
	}
}

func TestProcedureGateSpecIsImmutableAgainstAPIServer(t *testing.T) {
	c, _ := startEnvtest(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "neteye-system"}}); err != nil {
		t.Fatal(err)
	}
	gate := &neteye.NetEyeProcedureGate{
		ObjectMeta: metav1.ObjectMeta{Namespace: "neteye-system", Name: "keycloak-db-schema"},
		Spec:       neteye.NetEyeProcedureGateSpec{Version: "4.52", Gate: "keycloak-db-schema", Producer: neteye.ProcedureGateProducerAnsible},
	}
	if err := c.Create(ctx, gate); err != nil {
		t.Fatal(err)
	}
	gate.Spec.Gate = "something-else"
	if err := c.Update(ctx, gate); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "spec is immutable") {
		t.Errorf("update error = %v, want spec is immutable", err)
	}
	invalid := &neteye.NetEyeProcedureGate{
		ObjectMeta: metav1.ObjectMeta{Namespace: "neteye-system", Name: "invalid"},
		Spec:       neteye.NetEyeProcedureGateSpec{Version: "4.52.1", Gate: "Not_Valid", Producer: "Someone"},
	}
	if err := c.Create(ctx, invalid); !apierrors.IsInvalid(err) {
		t.Errorf("create error = %v, want invalid", err)
	}
}
