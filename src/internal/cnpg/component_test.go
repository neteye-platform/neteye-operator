// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package cnpg

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newComponent returns a Component on a cluster where neteye-setup already
// created the namespace of the operator.
func newComponent(t *testing.T) (*Component, *unstructured.Unstructured, func() error) {
	t.Helper()
	return newComponentOn(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: OperatorNamespace}})
}

func newComponentOn(t *testing.T, objects ...client.Object) (*Component, *unstructured.Unstructured, func() error) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(clusterExtensionGVK())
	get := func() error {
		return c.Get(context.Background(), types.NamespacedName{Name: extensionName}, extension)
	}
	return NewComponent(c, logr.Discard()), extension, get
}

func TestClusterExtensionSpec(t *testing.T) {
	spec := clusterExtensionSpec()
	if spec["namespace"] != "cnpg-system" {
		t.Errorf("namespace = %v", spec["namespace"])
	}
	catalog := spec["source"].(map[string]any)["catalog"].(map[string]any)
	if catalog["packageName"] != "cloudnative-pg" {
		t.Errorf("packageName = %v", catalog["packageName"])
	}
	if channels := catalog["channels"].([]any); len(channels) != 1 || channels[0] != "stable-v1" {
		t.Errorf("channels = %v", channels)
	}
	selector := catalog["selector"].(map[string]any)["matchLabels"].(map[string]any)
	if selector["olm.operatorframework.io/metadata.name"] != "operatorhubio" {
		t.Errorf("selector = %v", selector)
	}
}

func TestEnsureCreatesTheExtensionOnceWhenSetupCreatedTheNamespace(t *testing.T) {
	component, extension, get := newComponent(t)
	ctx := context.Background()
	for range 2 {
		if err := component.EnsureOperatorExtension(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := get(); err != nil {
		t.Fatalf("ClusterExtension: %v", err)
	}
	if len(extension.GetOwnerReferences()) != 0 {
		t.Errorf("owners = %v, want none", extension.GetOwnerReferences())
	}
}

// neteye-setup creates the namespace, as for Keycloak: the operator reports it
// missing and creates neither the namespace nor the ClusterExtension.
func TestEnsureCreatesNothingWhileTheNamespaceIsMissing(t *testing.T) {
	component, _, get := newComponentOn(t)
	ctx := context.Background()
	if err := component.EnsureOperatorExtension(ctx); !errors.Is(err, ErrOperatorNamespaceMissing) {
		t.Fatalf("err = %v, want ErrOperatorNamespaceMissing", err)
	}
	if err := get(); !apierrors.IsNotFound(err) {
		t.Errorf("the ClusterExtension was created without its namespace: %v", err)
	}
	if err := component.client.Get(ctx, types.NamespacedName{Name: OperatorNamespace}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Errorf("the operator created the namespace: %v", err)
	}
}

func TestIsReadyFollowsTheInstalledCondition(t *testing.T) {
	component, extension, get := newComponent(t)
	ctx := context.Background()
	if ready, message, err := component.IsReady(ctx); err != nil || ready || message == "" {
		t.Fatalf("before the extension exists: ready=%v message=%q err=%v", ready, message, err)
	}
	if err := component.EnsureOperatorExtension(ctx); err != nil {
		t.Fatal(err)
	}
	if ready, _, err := component.IsReady(ctx); err != nil || ready {
		t.Fatalf("without a status: ready=%v err=%v", ready, err)
	}
	if err := get(); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(extension.Object, []any{map[string]any{"type": "Installed", "status": "True"}}, "status", "conditions")
	if err := component.client.Update(ctx, extension); err != nil {
		t.Fatal(err)
	}
	if ready, _, err := component.IsReady(ctx); err != nil || !ready {
		t.Fatalf("installed: ready=%v err=%v", ready, err)
	}
}
