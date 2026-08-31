// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var (
	testGVK        = schema.GroupVersionKind{Group: "test.neteye.cloud", Version: "v1", Kind: "Widget"}
	testClusterGVK = schema.GroupVersionKind{Group: "test.neteye.cloud", Version: "v1", Kind: "ClusterWidget"}
)

func applyClient(t *testing.T, objs ...client.Object) client.Client {
	return applyClientWithInterceptors(t, interceptor.Funcs{}, objs...)
}

func applyClientWithInterceptors(t *testing.T, interceptors interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{testGVK, testClusterGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{testGVK.GroupVersion()})
	mapper.Add(testGVK, meta.RESTScopeNamespace)
	mapper.Add(testClusterGVK, meta.RESTScopeRoot)
	return fake.NewClientBuilder().WithScheme(s).WithRESTMapper(mapper).WithObjects(objs...).WithInterceptorFuncs(interceptors).Build()
}

func getObject(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name, namespace string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, u); err != nil {
		t.Fatalf("get object: %v", err)
	}
	return u
}

func TestApplyCreatesWithManagedLabel(t *testing.T) {
	c := applyClient(t)
	outcome, err := Apply(context.Background(), c, ObjectDefinition{
		GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "bar"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if outcome != Created {
		t.Errorf("outcome = %v, want Created", outcome)
	}

	u := getObject(t, c, testGVK, "w", "ns")
	if spec, _, _ := unstructured.NestedMap(u.Object, "spec"); spec["foo"] != "bar" {
		t.Errorf("spec = %v", spec)
	}
	if u.GetLabels()[managedByLabel] != managedByValue {
		t.Errorf("managed-by label = %v", u.GetLabels())
	}
}

func TestApplyUnchangedWhenNoDrift(t *testing.T) {
	c := applyClient(t)
	def := ObjectDefinition{GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "bar"}}
	if _, err := Apply(context.Background(), c, def); err != nil {
		t.Fatal(err)
	}
	outcome, err := Apply(context.Background(), c, def)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Unchanged {
		t.Errorf("outcome = %v, want Unchanged", outcome)
	}
}

func TestApplyUpdatesOnDrift(t *testing.T) {
	c := applyClient(t)
	if _, err := Apply(context.Background(), c, ObjectDefinition{GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "bar"}}); err != nil {
		t.Fatal(err)
	}
	outcome, err := Apply(context.Background(), c, ObjectDefinition{GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "baz"}})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != Updated {
		t.Errorf("outcome = %v, want Updated", outcome)
	}
	u := getObject(t, c, testGVK, "w", "ns")
	if spec, _, _ := unstructured.NestedMap(u.Object, "spec"); spec["foo"] != "baz" {
		t.Errorf("spec not updated: %v", spec)
	}
}

func TestApplyRetriesConflictedUpdate(t *testing.T) {
	updateAttempts := 0
	c := applyClientWithInterceptors(t, interceptor.Funcs{
		Update: func(ctx context.Context, underlying client.WithWatch, object client.Object, options ...client.UpdateOption) error {
			updateAttempts++
			if updateAttempts == 1 {
				return apierrors.NewConflict(schema.GroupResource{Group: testGVK.Group, Resource: "widgets"}, object.GetName(), errors.New("simulated conflict"))
			}
			return underlying.Update(ctx, object, options...)
		},
	})
	if _, err := Apply(context.Background(), c, ObjectDefinition{GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "bar"}}); err != nil {
		t.Fatal(err)
	}

	outcome, err := Apply(context.Background(), c, ObjectDefinition{GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "baz"}})
	if err != nil {
		t.Fatalf("apply after conflict: %v", err)
	}
	if outcome != Updated {
		t.Errorf("outcome = %v, want Updated", outcome)
	}
	if updateAttempts != 2 {
		t.Errorf("update attempts = %d, want 2", updateAttempts)
	}
}

func TestApplyReconcilesAfterCreateRace(t *testing.T) {
	c := applyClientWithInterceptors(t, interceptor.Funcs{
		Create: func(ctx context.Context, underlying client.WithWatch, object client.Object, options ...client.CreateOption) error {
			if err := underlying.Create(ctx, object, options...); err != nil {
				return err
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: testGVK.Group, Resource: "widgets"}, object.GetName())
		},
	})

	outcome, err := Apply(context.Background(), c, ObjectDefinition{GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "bar"}})
	if err != nil {
		t.Fatalf("apply after create race: %v", err)
	}
	if outcome != Unchanged {
		t.Errorf("outcome = %v, want Unchanged", outcome)
	}
	if spec, _, _ := unstructured.NestedMap(getObject(t, c, testGVK, "w", "ns").Object, "spec"); spec["foo"] != "bar" {
		t.Errorf("spec = %v", spec)
	}
}

func TestApplyClusterScoped(t *testing.T) {
	c := applyClient(t)
	outcome, err := Apply(context.Background(), c, ObjectDefinition{
		GVK: testClusterGVK, Name: "cw", Spec: map[string]any{"foo": "bar"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if outcome != Created {
		t.Errorf("outcome = %v, want Created", outcome)
	}
	if u := getObject(t, c, testClusterGVK, "cw", ""); u.GetNamespace() != "" {
		t.Errorf("expected cluster-scoped object, got namespace %q", u.GetNamespace())
	}
}

func TestApplySetsOwnerReference(t *testing.T) {
	c := applyClient(t)
	controller := true
	owner := metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", UID: "uid-1", Controller: &controller}
	if _, err := Apply(context.Background(), c, ObjectDefinition{
		GVK: testGVK, Name: "w", Namespace: "ns", Spec: map[string]any{"foo": "bar"}, Owner: &owner,
	}); err != nil {
		t.Fatal(err)
	}
	refs := getObject(t, c, testGVK, "w", "ns").GetOwnerReferences()
	if len(refs) != 1 || refs[0].Name != "platform" {
		t.Errorf("owner references = %v", refs)
	}
}
