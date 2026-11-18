// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func routeWithParent(namespace string, parentRef map[string]any) *unstructured.Unstructured {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK())
	route.SetNamespace("shared")
	route.SetName("route")
	route.SetGeneration(1)
	if namespace != "" {
		parentRef["namespace"] = namespace
	}
	route.Object["status"] = map[string]any{"parents": []any{map[string]any{
		"parentRef": parentRef,
		"conditions": []any{
			map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(1)},
			map[string]any{"type": "ResolvedRefs", "status": "True", "observedGeneration": int64(1)},
		},
	}}}
	return route
}

func routeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

// Gateway API reads an omitted parent namespace as the namespace of the route.
func TestRouteReadyReadsAnOmittedParentNamespaceAsTheRouteNamespace(t *testing.T) {
	expected := RouteParent{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "shared", Name: "gateway", Section: "listener"}
	for name, test := range map[string]struct {
		namespace string
		ready     bool
	}{
		"omitted namespace": {"", true},
		"same namespace":    {"shared", true},
		"another namespace": {"other", false},
	} {
		t.Run(name, func(t *testing.T) {
			c := routeClient(t, routeWithParent(test.namespace, map[string]any{"name": "gateway", "sectionName": "listener"}))
			ready, _, err := RouteReady(context.Background(), c, "shared", "route", RouteKindHTTP, expected)
			if err != nil || ready != test.ready {
				t.Errorf("ready = %t, err = %v, want %t", ready, err, test.ready)
			}
		})
	}
}
