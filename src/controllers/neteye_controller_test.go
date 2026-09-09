// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatalf("add neteye scheme: %v", err)
	}
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("add coordination scheme: %v", err)
	}
	return s
}

func newNetEye(version string) *neteye.NetEye {
	return &neteye.NetEye{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: "neteye-system"},
		Spec:       neteye.NetEyeSpec{Version: version},
	}
}

func reconcileNetEye(t *testing.T, r *NetEyeReconciler, ne *neteye.NetEye) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: ne.Namespace, Name: ne.Name},
	})
}

func TestReconcileVersionGating(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantPhase neteye.NetEyePhase
	}{
		{name: "previous version pends upgrade", version: neteye.PreviousNetEyeVersion, wantPhase: neteye.PhasePendingUpgrades},
		{name: "unknown version fails", version: "1.00", wantPhase: neteye.PhaseFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testScheme(t)
			ne := newNetEye(tt.version)
			c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).Build()
			r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}

			res, err := reconcileNetEye(t, r, ne)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.RequeueAfter != DefaultFailureRequeueAfter {
				t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, DefaultFailureRequeueAfter)
			}

			got := &neteye.NetEye{}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: ne.Namespace, Name: ne.Name}, got); err != nil {
				t.Fatalf("get neteye: %v", err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
		})
	}
}

func TestReconcileMissingKeycloakComponent(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}

	if _, err := reconcileNetEye(t, r, ne); err == nil {
		t.Fatal("expected an error when the keycloak component is nil")
	}

	got := &neteye.NetEye{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ne.Namespace, Name: ne.Name}, got); err != nil {
		t.Fatalf("get neteye: %v", err)
	}
	if got.Status.Phase != neteye.PhaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, neteye.PhaseFailed)
	}
}

func TestReconcileNotFound(t *testing.T) {
	s := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "neteye-system", Name: "missing"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsZero() {
		t.Errorf("expected an empty result, got %+v", res)
	}
}

func TestReconcileClusterAuthorityFailurePreventsManagedResourceMutations(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	ne.UID = "platform-uid"
	authorityFailure := errors.New("authority unavailable")
	createCalls, updateCalls := 0, 0
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, underlying client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, ok := object.(*coordinationv1.Lease); ok {
				return authorityFailure
			}
			return underlying.Get(ctx, key, object, options...)
		},
		Create: func(ctx context.Context, underlying client.WithWatch, object client.Object, options ...client.CreateOption) error {
			createCalls++
			return underlying.Create(ctx, object, options...)
		},
		Update: func(ctx context.Context, underlying client.WithWatch, object client.Object, options ...client.UpdateOption) error {
			updateCalls++
			return underlying.Update(ctx, object, options...)
		},
	}).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s, KeycloakComponent: keycloak.NewComponent(c, logr.Discard())}

	result, err := reconcileNetEye(t, r, ne)
	if !errors.Is(err, authorityFailure) {
		t.Fatalf("error = %v, want authority failure", err)
	}
	if result.RequeueAfter != DefaultFailureRequeueAfter {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, DefaultFailureRequeueAfter)
	}
	if createCalls != 0 || updateCalls != 0 {
		t.Errorf("managed resource mutations = creates:%d updates:%d, want none", createCalls, updateCalls)
	}
	got := &neteye.NetEye{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ne), got); err != nil {
		t.Fatalf("get neteye: %v", err)
	}
	if got.Status.Phase != neteye.PhaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, neteye.PhaseFailed)
	}
	if identity := got.Status.ServicesStatus.Identity; identity == nil || identity.Status != neteye.ServiceStateUnknown {
		t.Errorf("identity status = %+v, want Unknown", identity)
	}
	if elasticStack := got.Status.ServicesStatus.ElasticStack; elasticStack == nil || elasticStack.Status != neteye.ServiceStateUnknown || elasticStack.OTelCollector == nil || elasticStack.OTelCollector.Status != neteye.ServiceStateUnknown {
		t.Errorf("Elastic Stack status = %+v, want module and collector Unknown", elasticStack)
	}
}

func TestReconcileReturnsStatusUpdateFailure(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.PreviousNetEyeVersion)
	statusFailure := errors.New("status unavailable")
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return statusFailure
		},
	}).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}

	result, err := reconcileNetEye(t, r, ne)
	if !errors.Is(err, statusFailure) {
		t.Fatalf("error = %v, want status failure", err)
	}
	if result.RequeueAfter != DefaultFailureRequeueAfter {
		t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, DefaultFailureRequeueAfter)
	}
}

func TestReconcileJoinsSystemicAndStatusUpdateFailures(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	ne.UID = "platform-uid"
	authorityFailure := errors.New("authority unavailable")
	statusFailure := errors.New("status unavailable")
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, underlying client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, ok := object.(*coordinationv1.Lease); ok {
				return authorityFailure
			}
			return underlying.Get(ctx, key, object, options...)
		},
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return statusFailure
		},
	}).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s, KeycloakComponent: keycloak.NewComponent(c, logr.Discard())}

	_, err := reconcileNetEye(t, r, ne)
	if !errors.Is(err, authorityFailure) || !errors.Is(err, statusFailure) {
		t.Fatalf("error = %v, want joined authority and status failures", err)
	}
}

func TestUpdateStatusRetriesConflict(t *testing.T) {
	s := testScheme(t)
	ne := newNetEye(neteye.CurrentNetEyeVersion)
	attempts := 0
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(ne).WithObjects(ne).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, underlying client.Client, subResourceName string, object client.Object, options ...client.SubResourceUpdateOption) error {
			attempts++
			if attempts == 1 {
				return apierrors.NewConflict(neteye.GroupVersion.WithResource("neteyes").GroupResource(), object.GetName(), errors.New("simulated conflict"))
			}
			return underlying.SubResource(subResourceName).Update(ctx, object, options...)
		},
	}).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s}
	status := neteye.NetEyeStatus{Phase: neteye.PhaseReady}

	if err := r.updateStatus(context.Background(), client.ObjectKeyFromObject(ne), status); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if attempts != 2 {
		t.Errorf("status update attempts = %d, want 2", attempts)
	}
	got := &neteye.NetEye{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(ne), got); err != nil {
		t.Fatalf("get neteye: %v", err)
	}
	if got.Status.Phase != neteye.PhaseReady {
		t.Errorf("phase = %q, want %q", got.Status.Phase, neteye.PhaseReady)
	}
}

func TestShouldReturn(t *testing.T) {
	if shouldReturn(ctrl.Result{}, nil) {
		t.Error("empty result with no error should not stop reconciliation")
	}
	if !shouldReturn(ctrl.Result{RequeueAfter: DefaultReconciliationRequeueAfter}, nil) {
		t.Error("a requeue result should stop reconciliation")
	}
	if !shouldReturn(ctrl.Result{}, context.Canceled) {
		t.Error("an error should stop reconciliation")
	}
}

func TestOwnerReferenceFor(t *testing.T) {
	owner := ownerReferenceFor(newNetEye(neteye.CurrentNetEyeVersion))
	if owner.Kind != "NetEye" || owner.Name != "platform" {
		t.Errorf("owner = %+v", owner)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("owner should be a controller reference")
	}
}

func TestRequeueIntervals(t *testing.T) {
	overridden := &NetEyeReconciler{
		WaitForProgressingRequeueAfter: 1 * time.Second,
		FailureRequeueAfter:            2 * time.Second,
		ReconciliationRequeueAfter:     3 * time.Second,
	}
	if got := overridden.waitForProgressingRequeue(); got != 1*time.Second {
		t.Errorf("waitForProgressingRequeue() = %v, want %v", got, 1*time.Second)
	}
	if got := overridden.failureRequeue(); got != 2*time.Second {
		t.Errorf("failureRequeue() = %v, want %v", got, 2*time.Second)
	}
	if got := overridden.reconciliationRequeue(); got != 3*time.Second {
		t.Errorf("reconciliationRequeue() = %v, want %v", got, 3*time.Second)
	}

	def := &NetEyeReconciler{}
	if got := def.waitForProgressingRequeue(); got != DefaultWaitForProgressingRequeueAfter {
		t.Errorf("waitForProgressingRequeue() default = %v, want %v", got, DefaultWaitForProgressingRequeueAfter)
	}
	if got := def.failureRequeue(); got != DefaultFailureRequeueAfter {
		t.Errorf("failureRequeue() default = %v, want %v", got, DefaultFailureRequeueAfter)
	}
	if got := def.reconciliationRequeue(); got != DefaultReconciliationRequeueAfter {
		t.Errorf("reconciliationRequeue() default = %v, want %v", got, DefaultReconciliationRequeueAfter)
	}
}

func TestReconcileElasticStackOutcomeMapping(t *testing.T) {
	tests := []struct {
		name               string
		config             *neteye.NetEyeElasticStackSpec
		component          *elasticStackResources
		wantServiceState   neteye.ServiceState
		wantServiceMessage string
		wantModuleMessage  string
		wantRequeue        time.Duration
		wantErr            bool
		wantDeletes        int
		wantResultState    componentState
		wantResultReason   string
	}{
		{
			name:             "ready",
			config:           &neteye.NetEyeElasticStackSpec{Enabled: true, OTelCollector: &neteye.NetEyeOtelCollectorSpec{}},
			component:        &elasticStackResources{ready: true},
			wantServiceState: neteye.ServiceStateReady, wantServiceMessage: "OpenTelemetry Collector is ready", wantModuleMessage: "Elastic Stack feature module is ready",
			wantResultState: componentStateReady, wantResultReason: "Available",
		},
		{
			name:             "not ready uses progressing override",
			config:           &neteye.NetEyeElasticStackSpec{Enabled: true, OTelCollector: &neteye.NetEyeOtelCollectorSpec{}},
			component:        &elasticStackResources{message: "required user-managed Secret is missing"},
			wantServiceState: neteye.ServiceStateNotReady, wantServiceMessage: "required user-managed Secret is missing", wantModuleMessage: "Elastic Stack feature module is not ready",
			wantRequeue:     7 * time.Second,
			wantResultState: componentStateProgressing, wantResultReason: "Progressing",
		},
		{
			name:             "failed uses failure override",
			config:           &neteye.NetEyeElasticStackSpec{Enabled: true, OTelCollector: &neteye.NetEyeOtelCollectorSpec{}},
			component:        &elasticStackResources{err: errors.New("ensure failed")},
			wantServiceState: neteye.ServiceStateFailed, wantServiceMessage: "ensure failed", wantModuleMessage: "Elastic Stack feature module is unavailable",
			wantRequeue: 11 * time.Second, wantErr: true,
			wantResultState: componentStateDegraded, wantResultReason: "ReconcileFailed",
		},
		{
			name:             "disabled cleans up",
			component:        &elasticStackResources{},
			wantServiceState: neteye.ServiceStateDisabled, wantServiceMessage: "OpenTelemetry Collector is disabled", wantModuleMessage: "Elastic Stack feature module is disabled",
			wantDeletes:     1,
			wantResultState: componentStateReady, wantResultReason: "Disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ne := newNetEye(neteye.CurrentNetEyeVersion)
			ne.Spec.ElasticStack = tt.config
			ne.Status.Phase = neteye.PhaseReady
			ne.Status.Message = "previous phase"
			r := &NetEyeReconciler{
				ElasticStackReconciler:         elasticstack.NewReconciler(tt.component),
				WaitForProgressingRequeueAfter: 7 * time.Second,
				FailureRequeueAfter:            11 * time.Second,
			}

			result, err := r.reconcileElasticStack(context.Background(), ne, "collector-image")
			if err != nil {
				t.Fatalf("systemic error = %v", err)
			}
			if (result.Err != nil) != tt.wantErr {
				t.Errorf("component error = %v, want error=%t", result.Err, tt.wantErr)
			}
			if result.RequeueAfter != tt.wantRequeue {
				t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, tt.wantRequeue)
			}
			if result.State != tt.wantResultState || result.Reason != tt.wantResultReason {
				t.Errorf("component result = %+v, want state=%q reason=%q", result, tt.wantResultState, tt.wantResultReason)
			}
			collector := ne.Status.ServicesStatus.ElasticStack.OTelCollector
			if module := ne.Status.ServicesStatus.ElasticStack; module.Status != tt.wantServiceState || module.Message != tt.wantModuleMessage {
				t.Errorf("ElasticStack module status = %+v, want state=%q message=%q", module, tt.wantServiceState, tt.wantModuleMessage)
			}
			if collector.Status != tt.wantServiceState || collector.Message != tt.wantServiceMessage || collector.ResolvedImage != "collector-image" {
				t.Errorf("ElasticStack collector status = %+v, want state=%q message=%q image=collector-image", collector, tt.wantServiceState, tt.wantServiceMessage)
			}
			if tt.component.deletes != tt.wantDeletes {
				t.Errorf("delete calls = %d, want %d", tt.component.deletes, tt.wantDeletes)
			}
		})
	}
}

func TestEarliestComponentRequeue(t *testing.T) {
	results := map[componentID]componentResult{
		"none": {ID: "none"}, "late": {ID: "late", RequeueAfter: 2 * time.Minute},
		"early": {ID: "early", RequeueAfter: 30 * time.Second}, "negative": {ID: "negative", RequeueAfter: -time.Second},
	}
	if got := earliestComponentRequeue(results); got != 30*time.Second {
		t.Errorf("earliest = %v, want 30s", got)
	}
	if got := earliestComponentRequeue(map[componentID]componentResult{"none": {ID: "none"}}); got != 0 {
		t.Errorf("empty earliest = %v, want 0", got)
	}
}

func TestAggregateComponentPhasePreservesReadyIdentityWithDegradedTelemetry(t *testing.T) {
	identity, err := readyResult(identityComponentID, "Available", "Identity service is ready")
	if err != nil {
		t.Fatalf("identity result: %v", err)
	}
	telemetry, err := degradedResult(otelCollectorComponentID, "ReconcileFailed", "failed", time.Minute, errors.New("failed"))
	if err != nil {
		t.Fatalf("telemetry result: %v", err)
	}
	phase, _ := aggregateComponentPhase(map[componentID]componentResult{identityComponentID: identity, otelCollectorComponentID: telemetry})
	if identity.State != componentStateReady || phase != neteye.PhaseFailed {
		t.Errorf("identity=%+v phase=%q", identity, phase)
	}
}

type elasticStackResources struct {
	ready   bool
	message string
	err     error
	deletes int
}

func (r *elasticStackResources) EnsureResources(context.Context, string, neteye.NetEyeElasticStackSpec, string, string, string, string, resources.CertificateIssuerRef, metav1.OwnerReference) (bool, string, error) {
	return r.ready, r.message, r.err
}

func (r *elasticStackResources) DeleteResources(context.Context, string, metav1.OwnerReference) error {
	r.deletes++
	return r.err
}

var _ elasticstack.ResourceReconciler = (*elasticStackResources)(nil)
