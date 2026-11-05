// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestDefaultDenyNetworkPolicySpec(t *testing.T) {
	want := map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Ingress", "Egress"},
	}
	if got := defaultDenyNetworkPolicySpec(); !reflect.DeepEqual(got, want) {
		t.Errorf("default deny spec = %#v, want %#v", got, want)
	}
}

func TestNetworkPolicyPortsSetsProtocol(t *testing.T) {
	want := []any{
		map[string]any{"protocol": "UDP", "port": int64(53)},
		map[string]any{"protocol": "UDP", "port": int64(5353)},
	}
	if got := NetworkPolicyPorts("UDP", 53, 5353); !reflect.DeepEqual(got, want) {
		t.Errorf("ports = %#v, want %#v", got, want)
	}
}

func TestEnsureNetworkPolicyReplacesLegacyCiliumPolicy(t *testing.T) {
	const namespace, name = "ns", "policy"
	owner := policyTestOwner("owner-uid")
	noCiliumCRD := interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if obj.GetObjectKind().GroupVersionKind() == LegacyCiliumNetworkPolicyGVK() {
			return &meta.NoKindMatchError{GroupKind: LegacyCiliumNetworkPolicyGVK().GroupKind(), SearchedVersions: []string{"v2"}}
		}
		return c.Get(ctx, key, obj, opts...)
	}}
	for _, test := range []struct {
		name         string
		legacyOwner  *metav1.OwnerReference
		interceptors interceptor.Funcs
		wantLegacy   bool
	}{
		{name: "owned legacy policy is deleted", legacyOwner: &owner},
		{name: "foreign legacy policy is kept", legacyOwner: ptr.To(policyTestOwner("other-uid")), wantLegacy: true},
		{name: "no legacy policy"},
		{name: "cluster without the Cilium CRD", interceptors: noCiliumCRD},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithInterceptorFuncs(test.interceptors)
			if test.legacyOwner != nil {
				legacy := &unstructured.Unstructured{}
				legacy.SetGroupVersionKind(LegacyCiliumNetworkPolicyGVK())
				legacy.SetNamespace(namespace)
				legacy.SetName(name)
				legacy.SetOwnerReferences([]metav1.OwnerReference{*test.legacyOwner})
				builder = builder.WithObjects(legacy)
			}
			c := builder.Build()

			if err := EnsureNetworkPolicy(context.Background(), c, namespace, name, defaultDenyNetworkPolicySpec(), owner); err != nil {
				t.Fatalf("ensure network policy: %v", err)
			}
			policy := getObject(t, c, NetworkPolicyGVK(), name, namespace)
			if spec, _, _ := unstructured.NestedMap(policy.Object, "spec"); !reflect.DeepEqual(spec, defaultDenyNetworkPolicySpec()) {
				t.Errorf("network policy spec = %#v", spec)
			}
			if test.interceptors.Get != nil {
				return
			}
			legacy := &unstructured.Unstructured{}
			legacy.SetGroupVersionKind(LegacyCiliumNetworkPolicyGVK())
			err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, legacy)
			if test.wantLegacy && err != nil {
				t.Errorf("foreign legacy policy was deleted: %v", err)
			}
			if !test.wantLegacy && !apierrors.IsNotFound(err) {
				t.Errorf("legacy policy lookup = %v, want not found", err)
			}
		})
	}
}

func policyTestOwner(uid types.UID) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: string(uid), UID: uid, Controller: &controller}
}
