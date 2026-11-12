// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"slices"

	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// NetworkPolicyName is the native NetworkPolicy of the PermissionSync Pods. It
// adds to the default-deny baseline of the tenant namespace only the traffic
// that PermissionSync needs.
const NetworkPolicyName = "neteye-permissionsync"

// gatewayHTTPSPort is the port of the HTTPS listeners of the shared Gateway,
// through which PermissionSync reads the keys of the realm.
const gatewayHTTPSPort = 443

// networkPolicySpec allows no ingress and only this egress:
//
//   - DNS, on the DNS ports to any destination, because the namespace, labels
//     and port of the resolver change between distributions, as for the OTel
//     Collector;
//   - Keycloak, to read the keys of the realm. PermissionSync connects to the
//     shared Gateway on its HTTPS port, and Cilium checks the policy of the
//     client twice: towards the Gateway, which a native NetworkPolicy cannot
//     select, so a rule opens the port and no destination, as for the OTel
//     Collector; and towards the Keycloak Pods that the Gateway forwards to,
//     so another rule selects those Pods on their HTTP port;
//   - the provider and GLPI, on the ports of their endpoints. They run on the
//     NetEye nodes, outside Kubernetes. A native NetworkPolicy cannot select
//     the address of a node with Cilium, because Cilium gives it the host
//     identity and not a CIDR identity, so these ports are in the same rule
//     with no destination.
//
// The rule for ingress stays empty until Keycloak calls PermissionSync.
func networkPolicySpec(endpointPorts []int32) map[string]any {
	ports := []int64{gatewayHTTPSPort}
	for _, port := range endpointPorts {
		ports = append(ports, int64(port))
	}
	slices.Sort(ports)
	egress := []any{
		map[string]any{"ports": append(resources.NetworkPolicyPorts("UDP", 53, 5353), resources.NetworkPolicyPorts("TCP", 53, 5353)...)},
		map[string]any{
			"to": []any{map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": keycloak.WorkloadNamespace}},
				"podSelector": map[string]any{"matchLabels": map[string]any{
					"app":                          "keycloak",
					"app.kubernetes.io/instance":   keycloak.InstanceName,
					"app.kubernetes.io/managed-by": "keycloak-operator",
				}},
			}},
			"ports": resources.NetworkPolicyPorts("TCP", keycloak.HTTPPort),
		},
	}
	egress = append(egress, map[string]any{"ports": resources.NetworkPolicyPorts("TCP", slices.Compact(ports)...)})
	return map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"app": AppLabel}},
		"policyTypes": []any{"Ingress", "Egress"},
		"egress":      egress,
	}
}
