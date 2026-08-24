// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
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
