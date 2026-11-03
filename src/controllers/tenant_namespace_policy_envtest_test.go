// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"maps"
	"os"
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
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const testOperatorUsername = "system:serviceaccount:neteye-system:neteye-operator"

// TestTenantNamespacePolicyAgainstAPIServer checks the admission policy on a
// real API server: the API server compiles the CEL expressions, so a typo
// fails here rather than in production.
func TestTenantNamespacePolicyAgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("envtest binaries not available (run 'make envtest' / set KUBEBUILDER_ASSETS)")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	t.Cleanup(func() { _ = env.Stop() })
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	scheme := keycloakClientScheme(t)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	operatorConfig := rest.CopyConfig(cfg)
	operatorConfig.Impersonate = rest.ImpersonationConfig{UserName: testOperatorUsername, Groups: []string{"system:masters", "system:authenticated"}}
	operator, err := client.New(operatorConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	r := &NetEyeTenantReconciler{Client: operator, Log: logr.Discard(), Scheme: scheme, OperatorUsername: testOperatorUsername}
	if err := r.ensureNamespacePolicy(ctx); err != nil {
		t.Fatalf("ensure policy: %v", err)
	}
	generations := func() map[string]int64 {
		result := map[string]int64{}
		for _, gvk := range []schema.GroupVersionKind{validatingAdmissionPolicyGVK, validatingAdmissionPolicyBindingGVK} {
			object := &unstructured.Unstructured{}
			object.SetGroupVersionKind(gvk)
			if err := admin.Get(ctx, types.NamespacedName{Name: TenantNamespacePolicyName}, object); err != nil {
				t.Fatal(err)
			}
			result[gvk.Kind] = object.GetGeneration()
		}
		return result
	}
	before := generations()
	if err := r.ensureNamespacePolicy(ctx); err != nil {
		t.Fatalf("ensure policy again: %v", err)
	}
	if after := generations(); !maps.Equal(before, after) {
		t.Errorf("an unchanged policy was rewritten: generations %v -> %v", before, after)
	}

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        testTenant,
		Labels:      map[string]string{neteye.TenantLabel: testTenant},
		Annotations: map[string]string{neteye.TenantDisplayNameAnnotation: "Default"},
	}}
	if err := admin.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	other := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}
	if err := admin.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	// The API server loads a new policy asynchronously, so the first denial is
	// awaited with a bounded poll.
	setAnnotation := func(c client.Client, name, value string) error {
		ns := &corev1.Namespace{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, ns); err != nil {
			return err
		}
		patch := client.MergeFrom(ns.DeepCopy())
		if ns.Annotations == nil {
			ns.Annotations = map[string]string{}
		}
		ns.Annotations[neteye.TenantDisplayNameAnnotation] = value
		return c.Patch(ctx, ns, patch)
	}
	// Every attempt writes a new value: an update that changes nothing is
	// allowed by the policy and would prove nothing.
	var denial error
	attempt := 0
	pollErr := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) {
		attempt++
		denial = setAnnotation(admin, testTenant, fmt.Sprintf("Changed by hand %d", attempt))
		return denial != nil && strings.Contains(denial.Error(), TenantNamespacePolicyName), nil
	})
	if pollErr != nil {
		t.Fatalf("a user changed the display name annotation of a tenant namespace: last error %v", denial)
	}

	ns := &corev1.Namespace{}
	if err := admin.Get(ctx, types.NamespacedName{Name: testTenant}, ns); err != nil {
		t.Fatal(err)
	}
	patch := client.MergeFrom(ns.DeepCopy())
	delete(ns.Labels, neteye.TenantLabel)
	if err := admin.Patch(ctx, ns, patch); err == nil {
		t.Error("a user removed the tenant label of a tenant namespace")
	}

	ns = &corev1.Namespace{}
	if err := admin.Get(ctx, types.NamespacedName{Name: testTenant}, ns); err != nil {
		t.Fatal(err)
	}
	patch = client.MergeFrom(ns.DeepCopy())
	ns.Labels["team"] = "monitoring"
	if err := admin.Patch(ctx, ns, patch); err != nil {
		t.Errorf("a user could not change other metadata of a tenant namespace: %v", err)
	}
	if err := setAnnotation(admin, "unrelated", "anything"); err != nil {
		t.Errorf("the policy blocked a namespace that is not a tenant namespace: %v", err)
	}
	if err := setAnnotation(operator, testTenant, "Renamed"); err != nil {
		t.Errorf("the operator could not update the display name annotation: %v", err)
	}
}

func TestAPIServerValidatesNetEyeTenantSchema(t *testing.T) {
	c, _ := startEnvtest(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testTenant}}); err != nil {
		t.Fatal(err)
	}
	missing := &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant}}
	if err := c.Create(ctx, missing); err == nil || !strings.Contains(err.Error(), "spec.name") {
		t.Fatalf("tenant without spec.name: error = %v, want a spec.name validation error", err)
	}
	tooLong := &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant}, Spec: neteye.NetEyeTenantSpec{Name: strings.Repeat("x", 64)}}
	if err := c.Create(ctx, tooLong); err == nil {
		t.Fatal("tenant with a 64 character spec.name was accepted")
	}
	tenant := &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant}, Spec: neteye.NetEyeTenantSpec{Name: "Default"}}
	if err := c.Create(ctx, tenant); err != nil {
		t.Fatalf("valid tenant rejected: %v", err)
	}
	tenant.Status = neteye.NetEyeTenantStatus{ObservedGeneration: tenant.Generation, Conditions: []metav1.Condition{{Type: neteye.TenantConditionReady, Status: metav1.ConditionTrue, Reason: neteye.TenantReasonAllComponentsReady, Message: "ready", LastTransitionTime: metav1.Now()}}}
	if err := c.Status().Update(ctx, tenant); err != nil {
		t.Fatalf("status subresource update: %v", err)
	}
}
