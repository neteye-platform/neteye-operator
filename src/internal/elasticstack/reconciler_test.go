// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func TestReconcilerDisabledDeletesManagedResources(t *testing.T) {
	component := &recordingComponent{}
	outcome := NewReconciler(component).Reconcile(context.Background(), Request{Namespace: "shared", Owner: owner()})
	if component.deletes != 1 || outcome.Err != nil || outcome.Requeue != RequeueNone {
		t.Fatalf("deletes=%d outcome=%+v", component.deletes, outcome)
	}
	if outcome.Service.Status != neteye.ServiceStateUnknown {
		t.Errorf("service state = %q", outcome.Service.Status)
	}
}

func TestReconcilerEnabledWithoutComponentFails(t *testing.T) {
	config := elasticConfig()
	outcome := NewReconciler(nil).Reconcile(context.Background(), Request{Config: &config})
	if outcome.Err == nil || outcome.Requeue != RequeueFailure || outcome.Phase != neteye.PhaseFailed {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestReconcilerReportsMissingPrerequisiteAsProgressing(t *testing.T) {
	config := elasticConfig()
	component := &recordingComponent{ready: false, message: "required user-managed Secret \"api-key\" is missing"}
	outcome := NewReconciler(component).Reconcile(context.Background(), Request{Config: &config})
	if outcome.Err != nil || outcome.Requeue != RequeueProgressing || outcome.Phase != neteye.PhaseNotReady || outcome.Service.Message != component.message {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestReconcilerReady(t *testing.T) {
	config := elasticConfig()
	outcome := NewReconciler(&recordingComponent{ready: true}).Reconcile(context.Background(), Request{Config: &config})
	if outcome.Err != nil || outcome.Requeue != RequeueNone || outcome.Phase != "" || outcome.Service.Status != neteye.ServiceStateReady {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestDeleteResourcesPreservesObjectsOwnedByAnotherNetEye(t *testing.T) {
	otherOwner := owner()
	otherOwner.UID = types.UID("other")
	c := fake.NewClientBuilder().WithScheme(elasticScheme(t)).WithObjects(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shared", Name: ConfigMapName, OwnerReferences: []metav1.OwnerReference{otherOwner}},
	}).Build()
	owner := owner()
	owner.UID = types.UID("current")
	if err := NewComponent(c, logr.Discard()).DeleteResources(context.Background(), "shared", owner); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "shared", Name: ConfigMapName}, &corev1.ConfigMap{}); err != nil {
		t.Fatalf("object owned by another NetEye was deleted: %v", err)
	}
}

type recordingComponent struct {
	ready   bool
	message string
	err     error
	deletes int
}

func (c *recordingComponent) EnsureResources(context.Context, string, neteye.NetEyeElasticStackSpec, string, string, string, metav1.OwnerReference) (bool, string, error) {
	return c.ready, c.message, c.err
}

func (c *recordingComponent) DeleteResources(context.Context, string, metav1.OwnerReference) error {
	c.deletes++
	return c.err
}

var _ ResourceReconciler = (*recordingComponent)(nil)
