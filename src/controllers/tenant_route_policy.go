// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"fmt"
	"strconv"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/alyvix"
)

// TenantRoutePolicyName names the ValidatingAdmissionPolicy and its binding that
// keep the routes of a tenant namespace on the hostname of the tenant on the
// shared Alyvix RDP listener.
const TenantRoutePolicyName = "neteye-tenant-route-hostnames"

func tenantRoutePolicy() tenantAdmissionPolicy {
	return tenantAdmissionPolicy{name: TenantRoutePolicyName, protects: "the hostnames of the tenants on the Alyvix RDP listener", spec: tenantRoutePolicySpec()}
}

// tenantRoutePolicySpec refuses an HTTPRoute of a tenant namespace that
// attaches to the Alyvix RDP listener of a Gateway in the shared namespace,
// unless the route sets hostnames and each of them is the hostname of its own
// tenant. The listener has a wildcard hostname and accepts the routes of every
// tenant namespace: without the policy, a route could take the hostname of
// another tenant, or inherit the wildcard by setting no hostname.
//
// A parent reference without sectionName attaches to every listener that
// accepts the route, so it counts as a reference to the listener. Only the
// operator sets the tenant label of a namespace (see tenantNamespacePolicySpec),
// so the namespace selector cannot be escaped. The policy has no exemption: the
// operator writes the routes with the hostname of the tenant.
func tenantRoutePolicySpec() map[string]any {
	quote := strconv.Quote
	attaches := fmt.Sprintf("(!has(p.group) || p.group == %s) && (!has(p.kind) || p.kind == 'Gateway')"+
		" && has(p.namespace) && p.namespace == %s && (!has(p.sectionName) || p.sectionName == %s)",
		quote(gatewayAPIGroup), quote(neteye.NetEyeNamespace), quote(alyvix.GatewayListenerName))
	hostname := "request.namespace + " + quote("."+alyvix.HostnameSuffix)
	return map[string]any{
		"failurePolicy": "Fail",
		// The defaults the API server would fill in are spelled out, so the live
		// spec matches the desired one and an unchanged policy is not rewritten.
		"matchConstraints": map[string]any{
			"matchPolicy": "Equivalent",
			"namespaceSelector": map[string]any{
				"matchExpressions": []any{map[string]any{"key": neteye.TenantLabel, "operator": "Exists"}},
			},
			"objectSelector": map[string]any{},
			"resourceRules": []any{map[string]any{
				"apiGroups": []any{gatewayAPIGroup},
				// With matchPolicy Equivalent, a request through another served
				// version of HTTPRoute is converted to v1 and checked too.
				"apiVersions": []any{"v1"},
				"operations":  []any{"CREATE", "UPDATE"},
				"resources":   []any{"httproutes"},
				"scope":       "*",
			}},
		},
		"matchConditions": []any{
			map[string]any{
				"name":       "attaches-to-the-alyvix-rdp-listener",
				"expression": fmt.Sprintf("has(object.spec) && has(object.spec.parentRefs) && object.spec.parentRefs.exists(p, %s)", attaches),
			},
		},
		"validations": []any{
			map[string]any{
				"expression": fmt.Sprintf("has(object.spec.hostnames) && size(object.spec.hostnames) > 0 && object.spec.hostnames.all(h, h == %s)", hostname),
				"messageExpression": fmt.Sprintf("%s + %s + %s",
					quote(fmt.Sprintf("a route of a tenant namespace on the %s listener of the shared Gateway must set hostnames to the hostname of its tenant, ", alyvix.GatewayListenerName)),
					hostname, quote(", and to no other")),
				"reason": "Forbidden",
			},
		},
	}
}

const gatewayAPIGroup = "gateway.networking.k8s.io"
