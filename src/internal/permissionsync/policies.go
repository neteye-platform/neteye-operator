// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"slices"

	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// NetworkPolicyName is the native NetworkPolicy of the PermissionSync Pods. It
// adds to the default-deny baseline of the tenant namespace only the traffic
// that PermissionSync needs.
const NetworkPolicyName = "neteye-permissionsync"

// networkPolicySpec allows no ingress and only this egress:
//
//   - DNS, to the cluster DNS Pods;
//   - Keycloak, to read the keys of the realm. PermissionSync connects to the
//     shared Gateway, and the Gateway forwards the request to the Keycloak
//     Pods only if the policy of the client allows those Pods, so the rule
//     selects the Keycloak Pods on their HTTP port;
//   - the provider and GLPI, on the ports of their endpoints. They run on the
//     NetEye nodes, outside Kubernetes. A native NetworkPolicy cannot select
//     the address of a node with Cilium, because Cilium gives it the host
//     identity and not a CIDR identity, so these rules open only the ports and
//     no destination.
//
// The rule for ingress stays empty until Keycloak calls PermissionSync.
func networkPolicySpec(endpointPorts []int32) map[string]any {
	egress := []any{
		map[string]any{
			"to": []any{map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
				"podSelector":       map[string]any{"matchLabels": map[string]any{"k8s-app": "kube-dns"}},
			}},
			"ports": []any{
				map[string]any{"protocol": "UDP", "port": int64(53)},
				map[string]any{"protocol": "TCP", "port": int64(53)},
			},
		},
		map[string]any{
			"to": []any{map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": keycloak.WorkloadNamespace}},
				"podSelector": map[string]any{"matchLabels": map[string]any{
					"app":                          "keycloak",
					"app.kubernetes.io/instance":   keycloak.InstanceName,
					"app.kubernetes.io/managed-by": "keycloak-operator",
				}},
			}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": keycloak.HTTPPort}},
		},
	}
	if len(endpointPorts) > 0 {
		ports := slices.Clone(endpointPorts)
		slices.Sort(ports)
		ports = slices.Compact(ports)
		rule := make([]any, 0, len(ports))
		for _, port := range ports {
			rule = append(rule, map[string]any{"protocol": "TCP", "port": int64(port)})
		}
		egress = append(egress, map[string]any{"ports": rule})
	}
	return map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"app": AppLabel}},
		"policyTypes": []any{"Ingress", "Egress"},
		"egress":      egress,
	}
}
