// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureGRPCRoute ensures a Gateway API GRPCRoute with a same-namespace Service backend.
func EnsureGRPCRoute(ctx context.Context, c client.Client, namespace, name, gatewayNamespace, gatewayName, hostname, serviceName string, port int64, owner *metav1.OwnerReference) error {
	_, err := Apply(ctx, c, ObjectDefinition{GVK: schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"}, Name: name, Namespace: namespace, Owner: owner, Spec: map[string]any{
		"parentRefs": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": gatewayNamespace, "name": gatewayName, "sectionName": gatewayHTTPSListenerName}},
		"hostnames":  []any{hostname},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": serviceName, "port": port, "weight": int64(100)}}}},
	}})
	return err
}
