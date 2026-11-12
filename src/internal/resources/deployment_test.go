// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIsDeploymentReadyRequiresUpdatedReplicas(t *testing.T) {
	desired := int32(2)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "collector", Namespace: "shared", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: desired, UpdatedReplicas: desired - 1},
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appsv1.Deployment{}).WithObjects(deployment).Build()

	ready, _, err := IsDeploymentReady(context.Background(), c, "shared", "collector")
	if err != nil || ready {
		t.Fatalf("ready=%t err=%v, want not ready without all updated replicas", ready, err)
	}

	deployment.Status.UpdatedReplicas = desired
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	ready, _, err = IsDeploymentReady(context.Background(), c, "shared", "collector")
	if err != nil || !ready {
		t.Fatalf("ready=%t err=%v, want ready", ready, err)
	}
}

func TestIsDeploymentReadyReportsStalledRollout(t *testing.T) {
	stalled := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: `ReplicaSet "collector-1" has timed out progressing.`}
	for _, test := range []struct {
		name        string
		generation  int64
		ready       int32
		wantStalled bool
	}{
		{"current generation without ready replicas", 1, 0, true},
		{"condition from an earlier generation", 2, 0, false},
		{"ready replicas win over a leftover condition", 1, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "collector", Namespace: "shared", Generation: test.generation},
				Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: test.ready, UpdatedReplicas: 1, Conditions: []appsv1.DeploymentCondition{stalled}},
			}
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appsv1.Deployment{}).WithObjects(deployment).Build()

			_, message, err := IsDeploymentReady(context.Background(), c, "shared", "collector")
			if got := errors.Is(err, ErrDeploymentStalled); got != test.wantStalled {
				t.Fatalf("stalled=%t err=%v, want stalled=%t", got, err, test.wantStalled)
			}
			if test.wantStalled && !strings.Contains(message, "timed out progressing") {
				t.Fatalf("message=%q does not carry the Deployment condition", message)
			}
		})
	}
}
