// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/alyvix"
)

// TestTenantRoutePolicyAgainstAPIServer checks the route policy on a real API
// server, which compiles and evaluates the CEL expressions.
func TestTenantRoutePolicyAgainstAPIServer(t *testing.T) {
	admin, scheme, cfg := startEnvtestWithConfig(t)
	operatorConfig := rest.CopyConfig(cfg)
	operatorConfig.Impersonate = rest.ImpersonationConfig{UserName: testOperatorUsername, Groups: []string{"system:masters", "system:authenticated"}}
	operator, err := client.New(operatorConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := &TenantNamespacePolicy{Client: operator, Log: logr.Discard(), OperatorUsername: testOperatorUsername}
	if err := p.Ensure(ctx); err != nil {
		t.Fatalf("ensure policies: %v", err)
	}
	if state, message, err := checkTenantAdmissionPolicy(ctx, admin, tenantRoutePolicy()); err != nil || state != tenantPolicyEnforced {
		t.Fatalf("route policy state = %v %q %v, want enforced", state, message, err)
	}

	const other = "neteye-tenant-3cj57cgw"
	for _, name := range []string{testTenant, other} {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{neteye.TenantLabel: name}, Annotations: map[string]string{neteye.TenantDisplayNameAnnotation: name}}}
		if err := operator.Create(ctx, namespace); err != nil {
			t.Fatalf("create tenant namespace %s: %v", name, err)
		}
	}
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}); err != nil {
		t.Fatal(err)
	}
	own, others := alyvix.Hostname(testTenant), alyvix.Hostname(other)
	shared := func(section string) map[string]any {
		parent := map[string]any{"group": gatewayAPIGroup, "kind": "Gateway", "namespace": neteye.NetEyeNamespace, "name": "neteye"}
		if section != "" {
			parent["sectionName"] = section
		}
		return parent
	}
	count := 0
	route := func(namespace string, hostnames []any, parents ...map[string]any) *unstructured.Unstructured {
		count++
		object := newUnstructured(httpRouteGVK, namespace, fmt.Sprintf("route-%d", count))
		spec := map[string]any{"rules": []any{map[string]any{}}}
		if hostnames != nil {
			spec["hostnames"] = hostnames
		}
		if len(parents) > 0 {
			refs := make([]any, 0, len(parents))
			for _, parent := range parents {
				refs = append(refs, parent)
			}
			spec["parentRefs"] = refs
		}
		object.Object["spec"] = spec
		return object
	}

	// The API server loads a new policy asynchronously, so the first denial is
	// awaited with a bounded poll.
	var denial error
	pollErr := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		denial = admin.Create(ctx, route(testTenant, []any{others}, shared(alyvix.GatewayListenerName)))
		return denial != nil && strings.Contains(denial.Error(), TenantRoutePolicyName), nil
	})
	if pollErr != nil {
		t.Fatalf("a route of a tenant took the hostname of another tenant: last error %v", denial)
	}
	if !strings.Contains(denial.Error(), own) {
		t.Errorf("denial %q does not name the hostname of the tenant %s", denial, own)
	}

	withoutDefaults := map[string]any{"namespace": neteye.NetEyeNamespace, "name": "neteye", "sectionName": alyvix.GatewayListenerName}
	for name, object := range map[string]*unstructured.Unstructured{
		"no hostname":                           route(testTenant, nil, shared(alyvix.GatewayListenerName)),
		"an empty list of hostnames":            route(testTenant, []any{}, shared(alyvix.GatewayListenerName)),
		"the wildcard of the listener":          route(testTenant, []any{alyvix.GatewayListenerHostname}, shared(alyvix.GatewayListenerName)),
		"its own and another hostname":          route(testTenant, []any{own, others}, shared(alyvix.GatewayListenerName)),
		"a subdomain of its own hostname":       route(testTenant, []any{"x." + own}, shared(alyvix.GatewayListenerName)),
		"no sectionName":                        route(testTenant, []any{others}, shared("")),
		"no group and kind":                     route(testTenant, []any{others}, withoutDefaults),
		"a second parent on the listener":       route(testTenant, []any{others}, map[string]any{"name": "local"}, shared(alyvix.GatewayListenerName)),
		"no hostname and no sectionName either": route(testTenant, nil, shared("")),
	} {
		if err := admin.Create(ctx, object); err == nil || !strings.Contains(err.Error(), TenantRoutePolicyName) {
			t.Errorf("a route of a tenant namespace with %s was not refused by the policy: err = %v", name, err)
		}
	}
	if err := operator.Create(ctx, route(testTenant, []any{others}, shared(alyvix.GatewayListenerName))); err == nil {
		t.Error("the operator wrote a route with the hostname of another tenant: the policy has no exemption")
	}

	for name, object := range map[string]*unstructured.Unstructured{
		"its own hostname on the listener":    route(testTenant, []any{own}, shared(alyvix.GatewayListenerName)),
		"its own hostname, no sectionName":    route(testTenant, []any{own}, shared("")),
		"another listener of the Gateway":     route(testTenant, []any{others}, shared("keycloak")),
		"a Gateway of its own namespace":      route(testTenant, []any{others}, map[string]any{"name": "local", "sectionName": alyvix.GatewayListenerName}),
		"a Gateway of another namespace":      route(testTenant, []any{others}, map[string]any{"namespace": "elsewhere", "name": "neteye"}),
		"no parent":                           route(testTenant, []any{others}),
		"a namespace that is not of a tenant": route("plain", []any{others}, shared(alyvix.GatewayListenerName)),
	} {
		if err := admin.Create(ctx, object); err != nil {
			t.Errorf("a route with %s was refused: %v", name, err)
		}
	}

	valid := route(testTenant, []any{own}, shared(alyvix.GatewayListenerName))
	if err := operator.Create(ctx, valid); err != nil {
		t.Fatalf("the operator could not write the route of the tenant: %v", err)
	}
	if err := unstructured.SetNestedSlice(valid.Object, []any{others}, "spec", "hostnames"); err != nil {
		t.Fatal(err)
	}
	if err := admin.Update(ctx, valid); err == nil || !strings.Contains(err.Error(), TenantRoutePolicyName) {
		t.Errorf("an update moved the route of a tenant to the hostname of another tenant: err = %v", err)
	}
}
