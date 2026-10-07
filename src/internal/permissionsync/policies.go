// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"net"
	"strconv"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var ciliumPolicyGVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

// ingressPolicy admits the only two ways in: the shared Gateway, through which
// the identity service's login-sync authenticator posts over TLS, and the
// node, which runs the liveness and readiness probes. Everything else stays
// refused by the namespace-wide default-deny baseline.
func ingressPolicy() map[string]any {
	port := strconv.Itoa(int(ListenerPort))
	return map[string]any{
		"endpointSelector": labelsFor(appLabel),
		"ingress": []any{
			map[string]any{"fromEntities": []any{"ingress"}, "toPorts": []any{tcpPorts(port)}},
			map[string]any{"fromEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts(port)}},
		},
	}
}

// egressPolicy allows DNS, the identity service's public hostname for token
// verification material, and each configured Provider and GLPI endpoint. The
// identity hostname resolves to the shared Gateway, which can answer from a
// node address, so the host entities are allowed on 443 as well.
func egressPolicy(identityHostname string, targets []endpointTarget) map[string]any {
	rules := []any{
		dnsEgress(),
		fqdnEgress(identityHostname, "443"),
		map[string]any{"toEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts("443")}},
	}
	for _, target := range targets {
		rules = append(rules, targetEgress(target))
	}
	return map[string]any{"endpointSelector": labelsFor(appLabel), "egress": rules}
}

func targetEgress(target endpointTarget) map[string]any {
	if ip := net.ParseIP(target.host); ip != nil {
		return map[string]any{"toCIDR": []any{hostCIDR(ip)}, "toPorts": []any{tcpPorts(target.port)}}
	}
	return fqdnEgress(target.host, target.port)
}

func hostCIDR(ip net.IP) string {
	if ip.To4() != nil {
		return ip.String() + "/32"
	}
	return ip.String() + "/128"
}

func labelsFor(app string) map[string]any {
	return map[string]any{"matchLabels": map[string]any{"k8s:app": app}}
}

func tcpPorts(ports ...string) map[string]any {
	values := make([]any, 0, len(ports))
	for _, port := range ports {
		values = append(values, map[string]any{"port": port, "protocol": "TCP"})
	}
	return map[string]any{"ports": values}
}

func dnsEgress() map[string]any {
	return map[string]any{
		"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "kube-system", "k8s:k8s-app": "kube-dns"}}},
		"toPorts":     []any{map[string]any{"ports": []any{map[string]any{"port": "53", "protocol": "TCP"}, map[string]any{"port": "53", "protocol": "UDP"}}, "rules": map[string]any{"dns": []any{map[string]any{"matchPattern": "*"}}}}},
	}
}

func fqdnEgress(host, port string) map[string]any {
	return map[string]any{"toFQDNs": []any{map[string]any{"matchName": host}}, "toPorts": []any{tcpPorts(port)}}
}
