// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatalf("add neteye scheme: %v", err)
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
