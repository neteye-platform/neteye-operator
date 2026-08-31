// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func keycloakClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	gvk := keycloakGVK()
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func keycloakInstance(t *testing.T, namespace string) *unstructured.Unstructured {
	t.Helper()
	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(keycloakGVK())
	kc.SetName(InstanceName)
	kc.SetNamespace(namespace)
	return kc
}

func TestIsReadyNotFound(t *testing.T) {
	c := NewComponent(keycloakClient(t), logr.Discard())
	ready, msg, err := c.IsReady(context.Background(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("expected not ready")
	}
	if msg != "waiting for Keycloak CR to be created" {
		t.Errorf("msg = %q", msg)
	}
}

func TestIsReadyWaitsForObservedGeneration(t *testing.T) {
	kc := keycloakInstance(t, "ns")
	c := NewComponent(keycloakClient(t, kc), logr.Discard())
	ready, msg, err := c.IsReady(context.Background(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("expected not ready")
	}
	if msg != "waiting for Keycloak status to observe the latest generation" {
		t.Errorf("msg = %q", msg)
	}
}

func TestIsReadyTrue(t *testing.T) {
	kc := keycloakInstance(t, "ns")
	if err := unstructured.SetNestedField(kc.Object, int64(1), "status", "observedGeneration"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(kc.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	c := NewComponent(keycloakClient(t, kc), logr.Discard())
	ready, _, err := c.IsReady(context.Background(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("expected ready")
	}
}
