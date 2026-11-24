// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultDenyPolicyName = "neteye-default-deny"

// TenantDefaultDenyPolicyName names the default-deny NetworkPolicy of a tenant
// namespace.
const TenantDefaultDenyPolicyName = "neteye-tenant-default-deny"

// EnsureDefaultDenyNetworkPolicy reconciles the namespace-wide default deny that
// the workload-specific allow policies open up.
func EnsureDefaultDenyNetworkPolicy(ctx context.Context, c client.Client, namespace string, owner metav1.OwnerReference) error {
	return EnsureNetworkPolicy(ctx, c, namespace, DefaultDenyPolicyName, defaultDenyNetworkPolicySpec(), owner)
}

// EnsureNetworkPolicy reconciles a NetworkPolicy, then deletes the
// CiliumNetworkPolicy of the same name that earlier releases created. Applying
// first keeps the old allows in place until their replacement exists.
func EnsureNetworkPolicy(ctx context.Context, c client.Client, namespace, name string, spec map[string]any, owner metav1.OwnerReference) error {
	if _, err := Apply(ctx, c, ObjectDefinition{GVK: NetworkPolicyGVK(), Name: name, Namespace: namespace, Spec: spec, Owner: &owner}); err != nil {
		return err
	}
	return deleteLegacyCiliumNetworkPolicy(ctx, c, namespace, name, owner)
}

// deleteLegacyCiliumNetworkPolicy deletes the named CiliumNetworkPolicy when
// owner controls it. A cluster without the Cilium CRD has nothing to delete.
func deleteLegacyCiliumNetworkPolicy(ctx context.Context, c client.Client, namespace, name string, owner metav1.OwnerReference) error {
	return deleteOwned(ctx, c, LegacyCiliumNetworkPolicyGVK(), namespace, name, owner)
}

// ApplyTenantDefaultDenyNetworkPolicy reconciles the namespace-wide
// default-deny baseline of a tenant namespace. It selects every Pod and allows
// no ingress and no egress. Workloads that the tenant adds later open only the
// traffic they need with their own policies. Tenant namespaces never held a
// CiliumNetworkPolicy, so no legacy cleanup runs.
func ApplyTenantDefaultDenyNetworkPolicy(ctx context.Context, c client.Client, namespace string, owner metav1.OwnerReference) error {
	_, err := Apply(ctx, c, ObjectDefinition{
		GVK:       NetworkPolicyGVK(),
		Name:      TenantDefaultDenyPolicyName,
		Namespace: namespace,
		Spec:      defaultDenyNetworkPolicySpec(),
		Owner:     &owner,
	})
	return err
}

func defaultDenyNetworkPolicySpec() map[string]any {
	return map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Ingress", "Egress"},
	}
}

// NetworkPolicyPorts builds the ports of a NetworkPolicy rule. The protocol is
// always set so the spec matches what the API server returns after defaulting.
func NetworkPolicyPorts(protocol string, ports ...int64) []any {
	entries := make([]any, 0, len(ports))
	for _, port := range ports {
		entries = append(entries, map[string]any{"protocol": protocol, "port": port})
	}
	return entries
}

// DNSEgressRule allows DNS to any destination: the resolver's namespace,
// labels and port (53, or 5353 on OpenShift) differ between distributions.
func DNSEgressRule() map[string]any {
	return map[string]any{"ports": append(NetworkPolicyPorts("UDP", 53, 5353), NetworkPolicyPorts("TCP", 53, 5353)...)}
}

// NetworkPolicyGVK identifies the native Kubernetes network policies the
// operator manages.
func NetworkPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
}

// LegacyCiliumNetworkPolicyGVK identifies the policies earlier releases created
// in place of the NetworkPolicy objects of the same name.
func LegacyCiliumNetworkPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}
}
