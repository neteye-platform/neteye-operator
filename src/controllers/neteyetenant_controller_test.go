// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const testTenant = "neteye-tenant-2bh46bfv"

func tenantNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testTenant, Labels: map[string]string{neteye.TenantLabel: testTenant}}}
}

func tenantResource() *neteye.NetEyeTenant {
	return &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant, UID: "tenant-uid", Generation: 1}, Spec: neteye.NetEyeTenantSpec{Name: "Default"}}
}

func newTenantReconciler(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client) {
	t.Helper()
	scheme := keycloakClientScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&neteye.NetEyeTenant{}).Build()
	return &NetEyeTenantReconciler{Client: c, Log: logr.Discard(), Scheme: scheme}, c
}

func reconcileTenant(t *testing.T, r *NetEyeTenantReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), requestFor(tenantResource())); err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
}

func getTenant(t *testing.T, c client.Client) *neteye.NetEyeTenant {
	t.Helper()
	tenant := &neteye.NetEyeTenant{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

func getTenantNamespace(t *testing.T, c client.Client) *corev1.Namespace {
	t.Helper()
	namespace := &corev1.Namespace{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: testTenant}, namespace); err != nil {
		t.Fatal(err)
	}
	return namespace
}

func TestNetEyeTenantPreparesItsNamespace(t *testing.T) {
	// The namespace starts without the label, as when someone creates it by
	// hand: the operator adds it.
	r, c := newTenantReconciler(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testTenant}}, tenantResource())
	reconcileTenant(t, r)

	namespace := getTenantNamespace(t, c)
	if namespace.Labels[neteye.TenantLabel] != testTenant || namespace.Annotations[neteye.TenantDisplayNameAnnotation] != "Default" {
		t.Errorf("namespace labels = %v, annotations = %v", namespace.Labels, namespace.Annotations)
	}

	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(ciliumPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: resources.DefaultDenyPolicyName}, policy); err != nil {
		t.Fatalf("default-deny policy of the tenant namespace: %v", err)
	}
	deny, _, _ := unstructured.NestedMap(policy.Object, "spec", "enableDefaultDeny")
	if !reflect.DeepEqual(deny, map[string]any{"ingress": true, "egress": true}) {
		t.Errorf("enableDefaultDeny = %v, want ingress and egress", deny)
	}
	if owners := policy.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "NetEyeTenant" || owners[0].UID != "tenant-uid" {
		t.Errorf("default-deny policy owners = %#v, want the tenant", owners)
	}

	tenant := getTenant(t, c)
	ready := meta.FindStatusCondition(tenant.Status.Conditions, neteye.TenantConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != tenant.Generation {
		t.Errorf("Ready condition = %#v", ready)
	}
	if component := tenant.Status.Components[tenantNamespaceComponent]; component.Status != neteye.TenantComponentStateReady {
		t.Errorf("namespace component = %#v", component)
	}
	if len(tenant.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none: garbage collection removes everything the tenant owns", tenant.Finalizers)
	}
}

func TestNetEyeTenantDisplayNameChangeReachesTheNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, tenantNamespace(), tenantResource())
	reconcileTenant(t, r)

	tenant := getTenant(t, c)
	tenant.Spec.Name = "Würth Italy"
	if err := c.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if got := getTenantNamespace(t, c).Annotations[neteye.TenantDisplayNameAnnotation]; got != "Würth Italy" {
		t.Errorf("namespace display name annotation = %q", got)
	}
}

func TestNetEyeTenantRefusesToAdoptAForeignNetworkPolicy(t *testing.T) {
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(ciliumPolicyGVK)
	foreign.SetNamespace(testTenant)
	foreign.SetName(resources.DefaultDenyPolicyName)
	foreign.Object["spec"] = map[string]any{"endpointSelector": map[string]any{}}
	r, c := newTenantReconciler(t, tenantNamespace(), tenantResource(), foreign)
	reconcileTenant(t, r)

	tenant := getTenant(t, c)
	if component := tenant.Status.Components[tenantNamespaceComponent]; component.Status != neteye.TenantComponentStateDegraded || component.Reason != "NetworkPolicyFailed" {
		t.Errorf("namespace component = %#v", component)
	}
	if ready := meta.FindStatusCondition(tenant.Status.Conditions, neteye.TenantConditionReady); ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != neteye.TenantReasonComponentsDegraded {
		t.Errorf("Ready condition = %#v", ready)
	}
}

func TestNetEyeTenantReportsAMissingNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, tenantResource())
	reconcileTenant(t, r)
	if component := getTenant(t, c).Status.Components[tenantNamespaceComponent]; component.Status != neteye.TenantComponentStateDegraded || component.Reason != "NamespaceMetadataFailed" {
		t.Errorf("namespace component = %#v", component)
	}
}
