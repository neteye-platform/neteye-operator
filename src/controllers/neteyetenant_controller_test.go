// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const testTenant = "neteye-tenant-2bh46bfv"

func tenantScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func tenantResource() *neteye.NetEyeTenant {
	return &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant, UID: "tenant-uid", Generation: 1}, Spec: neteye.NetEyeTenantSpec{DisplayName: "Default"}}
}

// plainNamespace is a namespace as the installer creates it: without the tenant
// metadata, which only the operator sets.
func plainNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func newTenantReconciler(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client) {
	t.Helper()
	scheme := tenantScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&neteye.NetEyeTenant{}).Build()
	return &NetEyeTenantReconciler{Client: c, Log: logr.Discard(), Scheme: scheme}, c
}

func reconcileTenantRequest(t *testing.T, r *NetEyeTenantReconciler, namespace, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), requestFor(&neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}})); err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
}

func reconcileTenant(t *testing.T, r *NetEyeTenantReconciler) {
	t.Helper()
	reconcileTenantRequest(t, r, testTenant, testTenant)
}

func getTenant(t *testing.T, c client.Client, namespace, name string) *neteye.NetEyeTenant {
	t.Helper()
	tenant := &neteye.NetEyeTenant{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

func getNamespace(t *testing.T, c client.Client, name string) *corev1.Namespace {
	t.Helper()
	namespace := &corev1.Namespace{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, namespace); err != nil {
		t.Fatal(err)
	}
	return namespace
}

func defaultDenyPolicy(t *testing.T, c client.Client, namespace string) *unstructured.Unstructured {
	t.Helper()
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(resources.CiliumNetworkPolicyGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: resources.DefaultDenyPolicyName}, policy); err != nil {
		t.Fatalf("default-deny policy of namespace %q: %v", namespace, err)
	}
	return policy
}

func conditionOf(t *testing.T, tenant *neteye.NetEyeTenant, conditionType string) *metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(tenant.Status.Conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s missing: %#v", conditionType, tenant.Status.Conditions)
	}
	return condition
}

func TestNetEyeTenantPreparesItsNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)

	namespace := getNamespace(t, c, testTenant)
	if namespace.Labels[neteye.TenantLabel] != testTenant || namespace.Annotations[neteye.TenantDisplayNameAnnotation] != "Default" {
		t.Errorf("namespace labels = %v, annotations = %v", namespace.Labels, namespace.Annotations)
	}

	policy := defaultDenyPolicy(t, c, testTenant)
	deny, _, _ := unstructured.NestedMap(policy.Object, "spec", "enableDefaultDeny")
	if !reflect.DeepEqual(deny, map[string]any{"ingress": true, "egress": true}) {
		t.Errorf("enableDefaultDeny = %v, want ingress and egress", deny)
	}
	if owners := policy.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "NetEyeTenant" || owners[0].UID != "tenant-uid" {
		t.Errorf("default-deny policy owners = %#v, want the tenant", owners)
	}

	tenant := getTenant(t, c, testTenant, testTenant)
	if !slices.Contains(tenant.Finalizers, NetEyeTenantFinalizer) {
		t.Error("tenant finalizer missing")
	}
	if ready := conditionOf(t, tenant, neteye.ConditionReady); ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != tenant.Generation {
		t.Errorf("Ready = %#v", ready)
	}
	if progressing := conditionOf(t, tenant, neteye.ConditionProgressing); progressing.Status != metav1.ConditionFalse {
		t.Errorf("Progressing = %#v", progressing)
	}
	if degraded := conditionOf(t, tenant, neteye.ConditionDegraded); degraded.Status != metav1.ConditionFalse {
		t.Errorf("Degraded = %#v", degraded)
	}
	component := tenant.Status.Components[string(tenantNamespaceComponentID)]
	if component.Status != neteye.ComponentStateReady || component.ObservedGeneration != tenant.Generation {
		t.Errorf("namespace component = %#v", component)
	}
	// ADR-0002: a component with no images reports an empty list, so the field
	// must be present in the serialized status.
	serialized, err := json.Marshal(component)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serialized), `"resolvedImages":[]`) {
		t.Errorf("serialized component = %s, want an empty resolvedImages list", serialized)
	}
}

func TestNetEyeTenantDisplayNameChangeReachesTheNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)

	tenant := getTenant(t, c, testTenant, testTenant)
	tenant.Spec.DisplayName = "Würth Italy"
	if err := c.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if got := getNamespace(t, c, testTenant).Annotations[neteye.TenantDisplayNameAnnotation]; got != "Würth Italy" {
		t.Errorf("namespace display name annotation = %q", got)
	}
}

func TestNetEyeTenantRefusesToAdoptAForeignNetworkPolicy(t *testing.T) {
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(resources.CiliumNetworkPolicyGVK())
	foreign.SetNamespace(testTenant)
	foreign.SetName(resources.DefaultDenyPolicyName)
	foreign.Object["spec"] = map[string]any{"endpointSelector": map[string]any{}}
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource(), foreign)
	reconcileTenant(t, r)

	tenant := getTenant(t, c, testTenant, testTenant)
	if component := tenant.Status.Components[string(tenantNamespaceComponentID)]; component.Status != neteye.ComponentStateDegraded || component.Reason != "NetworkPolicyFailed" {
		t.Errorf("namespace component = %#v", component)
	}
	if ready := conditionOf(t, tenant, neteye.ConditionReady); ready.Status != metav1.ConditionFalse || ready.Reason != neteye.ReasonComponentsDegraded {
		t.Errorf("Ready = %#v", ready)
	}
	if degraded := conditionOf(t, tenant, neteye.ConditionDegraded); degraded.Status != metav1.ConditionTrue {
		t.Errorf("Degraded = %#v", degraded)
	}
	if spec, _, _ := unstructured.NestedMap(defaultDenyPolicy(t, c, testTenant).Object, "spec"); !reflect.DeepEqual(spec, foreign.Object["spec"]) {
		t.Errorf("a network policy the tenant does not own was changed: %v", spec)
	}
}

func TestNetEyeTenantLeavesAForeignNativeNetworkPolicyAlone(t *testing.T) {
	// The shared namespace cleans up a legacy native policy with the same name;
	// a tenant namespace never had one, so such an object belongs to someone
	// else and must stay.
	native := &unstructured.Unstructured{}
	native.SetGroupVersionKind(networkPolicyGVK)
	native.SetNamespace(testTenant)
	native.SetName(resources.DefaultDenyPolicyName)
	native.Object["spec"] = map[string]any{"podSelector": map[string]any{}}
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource(), native)
	reconcileTenant(t, r)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: resources.DefaultDenyPolicyName}, native); err != nil {
		t.Errorf("native network policy was removed: %v", err)
	}
}

func TestNetEyeTenantReportsAMissingNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, tenantResource())
	reconcileTenant(t, r)
	if component := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantNamespaceComponentID)]; component.Status != neteye.ComponentStateDegraded || component.Reason != "NamespaceMetadataFailed" {
		t.Errorf("namespace component = %#v", component)
	}
}

func TestNetEyeTenantThatBypassedAdmissionChangesNothing(t *testing.T) {
	for name, tenant := range map[string]*neteye.NetEyeTenant{
		"other namespace": {ObjectMeta: metav1.ObjectMeta{Namespace: neteye.NetEyeNamespace, Name: testTenant}, Spec: neteye.NetEyeTenantSpec{DisplayName: "Default"}},
		"invalid name":    {ObjectMeta: metav1.ObjectMeta{Namespace: "acme", Name: "acme"}, Spec: neteye.NetEyeTenantSpec{DisplayName: "Default"}},
	} {
		t.Run(name, func(t *testing.T) {
			r, c := newTenantReconciler(t, plainNamespace(tenant.Namespace), tenant)
			reconcileTenantRequest(t, r, tenant.Namespace, tenant.Name)

			if labels := getNamespace(t, c, tenant.Namespace).Labels; labels[neteye.TenantLabel] != "" {
				t.Errorf("namespace %q was marked as a tenant namespace", tenant.Namespace)
			}
			stored := getTenant(t, c, tenant.Namespace, tenant.Name)
			if len(stored.Finalizers) != 0 {
				t.Errorf("finalizers = %v", stored.Finalizers)
			}
			if component := stored.Status.Components[string(tenantNamespaceComponentID)]; component.Status != neteye.ComponentStateDegraded || component.Reason != "InvalidTenant" {
				t.Errorf("namespace component = %#v", component)
			}
		})
	}
}

func TestNetEyeTenantDeletionRemovesTheTenantMetadataAndKeepsTheNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)

	namespace := getNamespace(t, c, testTenant)
	if _, labeled := namespace.Labels[neteye.TenantLabel]; labeled {
		t.Error("tenant label left on the namespace")
	}
	if _, annotated := namespace.Annotations[neteye.TenantDisplayNameAnnotation]; annotated {
		t.Error("display name annotation left on the namespace")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, &neteye.NetEyeTenant{}); err == nil {
		t.Error("tenant finalizer was not removed")
	}
}

func TestNetEyeTenantDeletionWithTheNamespaceAlreadyGone(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)
	if err := c.Delete(context.Background(), getNamespace(t, c, testTenant)); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: testTenant}, &neteye.NetEyeTenant{}); err == nil {
		t.Error("tenant finalizer was not removed")
	}
}

func TestTenantForNamespaceMapsOnlyTenantNamespaces(t *testing.T) {
	if requests := tenantForNamespace(context.Background(), plainNamespace(testTenant)); len(requests) != 1 || requests[0].Namespace != testTenant || requests[0].Name != testTenant {
		t.Errorf("requests for a tenant namespace = %v", requests)
	}
	if requests := tenantForNamespace(context.Background(), plainNamespace("kube-system")); len(requests) != 0 {
		t.Errorf("requests for another namespace = %v", requests)
	}
}

func TestSetTenantConditionsIsStableAcrossComponentOrder(t *testing.T) {
	status := neteye.NetEyeTenantStatus{Components: map[string]neteye.NetEyeComponentStatus{
		"zeta":  {Status: neteye.ComponentStateProgressing},
		"alpha": {Status: neteye.ComponentStateBlocked},
		"beta":  {Status: neteye.ComponentStateDegraded},
		"gamma": {Status: neteye.ComponentStateReady},
	}}
	for range 20 {
		setTenantConditions(&status, 3)
		ready := meta.FindStatusCondition(status.Conditions, neteye.ConditionReady)
		if ready.Status != metav1.ConditionFalse || ready.Reason != neteye.ReasonComponentsDegraded || ready.Message != "Degraded components: beta" {
			t.Fatalf("Ready = %#v", ready)
		}
		progressing := meta.FindStatusCondition(status.Conditions, neteye.ConditionProgressing)
		if progressing.Status != metav1.ConditionTrue || progressing.Message != "Converging components: alpha, zeta" {
			t.Fatalf("Progressing = %#v", progressing)
		}
	}
}
