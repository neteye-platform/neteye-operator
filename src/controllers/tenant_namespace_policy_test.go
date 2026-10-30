// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const testOperatorUsername = "system:serviceaccount:neteye-system:neteye-operator"

func TestTenantNamespacePolicyRequiresTheOperatorUsername(t *testing.T) {
	p := &TenantNamespacePolicy{Log: logr.Discard()}
	if err := p.Ensure(context.Background()); err == nil {
		t.Fatal("policy applied without knowing which user to exempt")
	}
}

// TestTenantNamespacePolicyAgainstAPIServer checks the admission policy on a
// real API server: the API server compiles the CEL expressions, so a mistake
// fails here rather than in production.
func TestTenantNamespacePolicyAgainstAPIServer(t *testing.T) {
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
		t.Fatalf("ensure policy: %v", err)
	}
	policyObjects := func() map[string]*unstructured.Unstructured {
		objects := map[string]*unstructured.Unstructured{}
		for _, gvk := range []schema.GroupVersionKind{validatingAdmissionPolicyGVK, validatingAdmissionPolicyBindingGVK} {
			object := &unstructured.Unstructured{}
			object.SetGroupVersionKind(gvk)
			if err := admin.Get(ctx, types.NamespacedName{Name: TenantNamespacePolicyName}, object); err != nil {
				t.Fatal(err)
			}
			objects[gvk.Kind] = object
		}
		return objects
	}
	generations := func() map[string]int64 {
		result := map[string]int64{}
		for kind, object := range policyObjects() {
			result[kind] = object.GetGeneration()
		}
		return result
	}
	for kind, object := range policyObjects() {
		owners := object.GetOwnerReferences()
		if len(owners) != 1 || owners[0].Kind != "CustomResourceDefinition" || owners[0].Name != netEyeTenantCRDName {
			t.Errorf("%s owners = %#v, want the NetEyeTenant CRD", kind, owners)
		}
	}
	before := generations()
	if err := p.Ensure(ctx); err != nil {
		t.Fatalf("ensure policy again: %v", err)
	}
	if after := generations(); !maps.Equal(before, after) {
		t.Errorf("an unchanged policy was rewritten: generations %v -> %v", before, after)
	}

	tenantNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        testTenant,
		Labels:      map[string]string{neteye.TenantLabel: testTenant},
		Annotations: map[string]string{neteye.TenantDisplayNameAnnotation: "Default"},
	}}
	if err := operator.Create(ctx, tenantNamespace); err != nil {
		t.Fatalf("the operator could not create a tenant namespace: %v", err)
	}
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}); err != nil {
		t.Fatal(err)
	}

	patchNamespace := func(c client.Client, name string, change func(*corev1.Namespace)) error {
		ns := &corev1.Namespace{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, ns); err != nil {
			return err
		}
		patch := client.MergeFrom(ns.DeepCopy())
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		if ns.Annotations == nil {
			ns.Annotations = map[string]string{}
		}
		change(ns)
		return c.Patch(ctx, ns, patch)
	}

	// The API server loads a new policy asynchronously, so the first denial is
	// awaited with a bounded poll. Every attempt writes a new value: an update
	// that changes nothing is allowed and would prove nothing.
	var denial error
	attempt := 0
	pollErr := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		attempt++
		denial = patchNamespace(admin, testTenant, func(ns *corev1.Namespace) {
			ns.Annotations[neteye.TenantDisplayNameAnnotation] = fmt.Sprintf("Changed by hand %d", attempt)
		})
		return denial != nil && strings.Contains(denial.Error(), TenantNamespacePolicyName), nil
	})
	if pollErr != nil {
		t.Fatalf("a user changed the display name annotation of a tenant namespace: last error %v", denial)
	}

	for name, change := range map[string]func(*corev1.Namespace){
		"remove the tenant label": func(ns *corev1.Namespace) { delete(ns.Labels, neteye.TenantLabel) },
		"change the tenant label": func(ns *corev1.Namespace) { ns.Labels[neteye.TenantLabel] = "neteye-tenant-aaaaaaaa" },
	} {
		if err := patchNamespace(admin, testTenant, change); err == nil {
			t.Errorf("a user could %s of a tenant namespace", name)
		}
	}
	if err := patchNamespace(admin, "unrelated", func(ns *corev1.Namespace) { ns.Labels[neteye.TenantLabel] = "unrelated" }); err == nil {
		t.Error("a user turned an existing namespace into a tenant namespace")
	}
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "spoofed", Labels: map[string]string{neteye.TenantLabel: "spoofed"}}}); err == nil {
		t.Error("a user created a namespace with the tenant label")
	}

	if err := patchNamespace(admin, testTenant, func(ns *corev1.Namespace) { ns.Labels["team"] = "monitoring" }); err != nil {
		t.Errorf("a user could not change other metadata of a tenant namespace: %v", err)
	}
	if err := patchNamespace(admin, "unrelated", func(ns *corev1.Namespace) { ns.Labels["team"] = "monitoring" }); err != nil {
		t.Errorf("the policy blocked a namespace that is not a tenant namespace: %v", err)
	}
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}); err != nil {
		t.Errorf("a user could not create a plain namespace: %v", err)
	}
	if err := patchNamespace(operator, testTenant, func(ns *corev1.Namespace) { ns.Annotations[neteye.TenantDisplayNameAnnotation] = "Renamed" }); err != nil {
		t.Errorf("the operator could not update the display name annotation: %v", err)
	}
	if err := patchNamespace(operator, testTenant, func(ns *corev1.Namespace) {
		delete(ns.Labels, neteye.TenantLabel)
		delete(ns.Annotations, neteye.TenantDisplayNameAnnotation)
	}); err != nil {
		t.Errorf("the operator could not remove the tenant metadata: %v", err)
	}
}

func TestTenantNamespacePolicyDoesNotAdoptAForeignPolicy(t *testing.T) {
	admin, _, _ := startEnvtestWithConfig(t)
	ctx := context.Background()
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(validatingAdmissionPolicyGVK)
	foreign.SetName(TenantNamespacePolicyName)
	foreign.Object["spec"] = map[string]any{
		"validations": []any{map[string]any{"expression": "true"}},
		"matchConstraints": map[string]any{"resourceRules": []any{map[string]any{
			"apiGroups": []any{""}, "apiVersions": []any{"v1"}, "operations": []any{"UPDATE"}, "resources": []any{"configmaps"},
		}}},
	}
	if err := admin.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	p := &TenantNamespacePolicy{Client: admin, Log: logr.Discard(), OperatorUsername: testOperatorUsername}
	if err := p.Ensure(ctx); err == nil || !strings.Contains(err.Error(), "cannot be adopted") {
		t.Fatalf("error = %v, want a conflict with the existing policy", err)
	}
}
