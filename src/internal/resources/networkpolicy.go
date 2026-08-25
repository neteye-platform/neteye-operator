// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultDenyPolicyName = "neteye-default-deny"

// EnsureDefaultDenyNetworkPolicy reconciles the shared namespace-wide Cilium
// default-deny baseline. Workload-specific Cilium allow policies compose with
// this baseline without requiring native NetworkPolicy egress exceptions.
func EnsureDefaultDenyNetworkPolicy(ctx context.Context, c client.Client, namespace string) error {
	if err := deleteLegacyDefaultDenyNetworkPolicy(ctx, c, namespace); err != nil {
		return err
	}
	_, err := Apply(ctx, c, ObjectDefinition{
		GVK:       ciliumNetworkPolicyGVK(),
		Name:      DefaultDenyPolicyName,
		Namespace: namespace,
		Spec:      defaultDenyNetworkPolicySpec(),
	})
	return err
}

func deleteLegacyDefaultDenyNetworkPolicy(ctx context.Context, c client.Client, namespace string) error {
	legacy := &unstructured.Unstructured{}
	legacy.SetGroupVersionKind(nativeNetworkPolicyGVK())
	key := types.NamespacedName{Name: DefaultDenyPolicyName, Namespace: namespace}
	if err := c.Get(ctx, key, legacy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := c.Delete(ctx, legacy); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func defaultDenyNetworkPolicySpec() map[string]any {
	return map[string]any{
		"endpointSelector": map[string]any{},
		"enableDefaultDeny": map[string]any{
			"ingress": true,
			"egress":  true,
		},
		"ingress": []any{map[string]any{"fromEntities": []any{"none"}}},
		"egress":  []any{map[string]any{"toEntities": []any{"none"}}},
	}
}

func nativeNetworkPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
}

func ciliumNetworkPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}
}
