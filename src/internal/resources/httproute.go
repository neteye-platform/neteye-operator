// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// EnsureHTTPRoute ensures a Gateway API HTTPRoute exists for a hostname and
// routes traffic to a Service in the same namespace as the route.
func EnsureHTTPRoute(ctx context.Context, client client.Client, namespace string, name, gatewayNamespace, gatewayRef string, hostnames []string, backendServiceName string, backendServicePort int64, owner *metav1.OwnerReference) error {
	desiredSpec := map[string]any{
		"parentRefs": []any{
			map[string]any{
				"group":       "gateway.networking.k8s.io",
				"kind":        "Gateway",
				"namespace":   gatewayNamespace,
				"name":        gatewayRef,
				"sectionName": gatewayHTTPSListenerName,
			},
		},
		"hostnames": stringSliceToInterfaces(hostnames),
		"rules": []any{
			map[string]any{
				"backendRefs": []any{
					map[string]any{
						"group": "",
						"kind":  "Service",
						"name":  backendServiceName,
						"port":  backendServicePort,
					},
				},
			},
		},
	}
	return ensureHTTPRouteSpec(ctx, client, namespace, name, desiredSpec, owner)
}

func ensureHTTPRouteSpec(ctx context.Context, client client.Client, namespace string, name string, desiredSpec map[string]any, owner *metav1.OwnerReference) error {
	outcome, err := Apply(ctx, client, ObjectDefinition{
		GVK:       httpRouteGVK(),
		Name:      name,
		Namespace: namespace,
		Spec:      desiredSpec,
		Owner:     owner,
	})
	if err != nil {
		return err
	}
	log := logf.FromContext(ctx)
	switch outcome {
	case Unchanged:
		log.V(1).Info("HttpRoute had no drift", "namespace", namespace, "name", name)
	case Updated:
		log.V(1).Info("HTTPRoute reconciled", "namespace", namespace, "name", name)
	case Created:
		log.Info("HTTPRoute created", "namespace", namespace, "name", name)
	}
	return nil
}

func httpRouteGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "HTTPRoute",
	}
}
