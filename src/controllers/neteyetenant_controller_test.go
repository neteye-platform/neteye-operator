// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/permissionsync"
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

// tenantNamespacePolicyObjects returns the admission policy and its binding as
// TenantNamespacePolicy writes them: controlled by the NetEyeTenant CRD, with a
// binding that denies.
func tenantNamespacePolicyObjects() []client.Object {
	owner := metav1.OwnerReference{APIVersion: customResourceDefinitionGVK.GroupVersion().String(), Kind: customResourceDefinitionGVK.Kind, Name: netEyeTenantCRDName, UID: "crd-uid", Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(false)}
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(validatingAdmissionPolicyGVK)
	policy.SetName(TenantNamespacePolicyName)
	policy.SetOwnerReferences([]metav1.OwnerReference{owner})
	policy.Object["spec"] = tenantNamespacePolicySpec(testOperatorUsername)
	binding := &unstructured.Unstructured{}
	binding.SetGroupVersionKind(validatingAdmissionPolicyBindingGVK)
	binding.SetName(TenantNamespacePolicyName)
	binding.SetOwnerReferences([]metav1.OwnerReference{owner})
	binding.Object["spec"] = tenantNamespacePolicyBindingSpec()
	return []client.Object{tenantCRD(), policy, binding}
}

// tenantCRD is the NetEyeTenant CRD that controls the policy objects.
func tenantCRD() *unstructured.Unstructured {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(customResourceDefinitionGVK)
	crd.SetName(netEyeTenantCRDName)
	crd.SetUID("crd-uid")
	return crd
}

// newTenantReconciler builds a reconciler on a fake cluster where the tenant
// namespace admission policy is in place and the NetEye resource and the root
// CA that PermissionSync needs exist.
func newTenantReconciler(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client) {
	t.Helper()
	base := append(tenantNamespacePolicyObjects(), permissionSyncPrerequisites()...)
	return newTenantReconcilerWithoutPolicy(t, append(base, objects...)...)
}

const testIdentityHostname = "neteye.example.com"

// permissionSyncPrerequisites are the shared objects that the PermissionSync
// of a tenant reads: the NetEye resource and the root CA.
func permissionSyncPrerequisites() []client.Object {
	return []client.Object{
		&neteye.NetEye{
			ObjectMeta: metav1.ObjectMeta{Name: "neteye"},
			Spec: neteye.NetEyeSpec{
				Version:  neteye.CurrentNetEyeVersion,
				Identity: neteye.NetEyeIdentitySpec{Hostname: testIdentityHostname},
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: permissionsync.RootCASecretName},
			Data:       map[string][]byte{corev1.TLSCertKey: []byte(testRootCAPEM)},
		},
	}
}

const testRootCAPEM = "-----BEGIN CERTIFICATE-----\nTEST\n-----END CERTIFICATE-----\n"

// markPermissionSyncRolledOut reports every desired Pod of the PermissionSync
// Deployment of the tenant as updated and ready, as the Deployment controller
// does after a rollout.
func markPermissionSyncRolledOut(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: permissionsync.DeploymentName}, deployment); err != nil {
		t.Fatalf("PermissionSync Deployment: %v", err)
	}
	replicas := *deployment.Spec.Replicas
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: replicas, ReadyReplicas: replicas, UpdatedReplicas: replicas, AvailableReplicas: replicas}
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}

// reconcileTenantUntilReady reconciles, reports the KeycloakRealm of the
// tenant as reconciled and the PermissionSync rollout as complete, and
// reconciles again after each.
func reconcileTenantUntilReady(t *testing.T, r *NetEyeTenantReconciler, c client.Client) {
	t.Helper()
	reconcileTenant(t, r)
	markTenantRealmReady(t, c, testTenant)
	reconcileTenant(t, r)
	markPermissionSyncRolledOut(t, c, testTenant)
	reconcileTenant(t, r)
}

// newTenantReconcilerWithoutPolicy builds a reconciler on a fake cluster that
// contains only the given objects and the Keycloak credentials of the
// platform, against a fake Keycloak.
func newTenantReconcilerWithoutPolicy(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client) {
	t.Helper()
	r, c, _ := newTenantReconcilerOnFakeKeycloak(t, objects...)
	return r, c
}

// newTenantReconcilerWithKeycloak is newTenantReconciler that also returns
// the fake Keycloak.
func newTenantReconcilerWithKeycloak(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client, *fakeKeycloak) {
	t.Helper()
	base := append(tenantNamespacePolicyObjects(), permissionSyncPrerequisites()...)
	return newTenantReconcilerOnFakeKeycloak(t, append(base, objects...)...)
}

func newTenantReconcilerOnFakeKeycloak(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client, *fakeKeycloak) {
	t.Helper()
	scheme := tenantScheme(t)
	objects = append(objects, adminSecret(keycloak.WorkloadNamespace))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&neteye.NetEyeTenant{}, &neteye.KeycloakRealm{}).Build()
	kc := newFakeKeycloak()
	return &NetEyeTenantReconciler{Client: c, Log: logr.Discard(), Scheme: scheme, AdminProviders: adminProvidersFor(t, c, kc), OperatorUsername: testOperatorUsername}, c, kc
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
	policy.SetGroupVersionKind(resources.NetworkPolicyGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: resources.TenantDefaultDenyPolicyName}, policy); err != nil {
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
	reconcileTenantUntilReady(t, r, c)

	namespace := getNamespace(t, c, testTenant)
	if namespace.Labels[neteye.TenantLabel] != testTenant || namespace.Annotations[neteye.TenantDisplayNameAnnotation] != "Default" {
		t.Errorf("namespace labels = %v, annotations = %v", namespace.Labels, namespace.Annotations)
	}

	policy := defaultDenyPolicy(t, c, testTenant)
	spec, _, _ := unstructured.NestedMap(policy.Object, "spec")
	if want := map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress", "Egress"}}; !reflect.DeepEqual(spec, want) {
		t.Errorf("default-deny spec = %v, want every Pod selected with ingress and egress denied", spec)
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
	foreign.SetGroupVersionKind(resources.NetworkPolicyGVK())
	foreign.SetNamespace(testTenant)
	foreign.SetName(resources.TenantDefaultDenyPolicyName)
	foreign.Object["spec"] = map[string]any{"podSelector": map[string]any{}}
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

func TestNetEyeTenantWaitsForTheNamespaceAdmissionPolicy(t *testing.T) {
	policyOnly := tenantNamespacePolicyObjects()[:2]
	denyless := tenantNamespacePolicyObjects()
	denyless[2].(*unstructured.Unstructured).Object["spec"] = map[string]any{"policyName": TenantNamespacePolicyName, "validationActions": []any{"Audit"}}
	permissive := tenantNamespacePolicyObjects()
	permissiveSpec := tenantNamespacePolicySpec(testOperatorUsername)
	permissiveSpec["validations"] = []any{map[string]any{"expression": "true"}}
	permissive[1].(*unstructured.Unstructured).Object["spec"] = permissiveSpec
	otherExemption := tenantNamespacePolicyObjects()
	otherExemption[1].(*unstructured.Unstructured).Object["spec"] = tenantNamespacePolicySpec("system:serviceaccount:default:default")
	for name, objects := range map[string][]client.Object{
		"no policy":                 {tenantCRD()},
		"no binding":                policyOnly,
		"binding does not deny":     denyless,
		"policy validates nothing":  permissive,
		"policy exempts other user": otherExemption,
	} {
		t.Run(name, func(t *testing.T) {
			r, c := newTenantReconcilerWithoutPolicy(t, append(objects, plainNamespace(testTenant), tenantResource())...)
			reconcileTenant(t, r)

			tenant := getTenant(t, c, testTenant, testTenant)
			if component := tenant.Status.Components[string(tenantNamespaceComponentID)]; component.Status != neteye.ComponentStateProgressing || component.Reason != "WaitingForNamespacePolicy" {
				t.Errorf("namespace component = %#v", component)
			}
			if ready := conditionOf(t, tenant, neteye.ConditionReady); ready.Status != metav1.ConditionFalse {
				t.Errorf("Ready = %#v, want false while the tenant metadata is not protected", ready)
			}
		})
	}
}

func TestNetEyeTenantWithoutTheOperatorUsernameIsNotReady(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	r.OperatorUsername = ""
	reconcileTenant(t, r)

	tenant := getTenant(t, c, testTenant, testTenant)
	if component := tenant.Status.Components[string(tenantNamespaceComponentID)]; component.Status != neteye.ComponentStateProgressing || component.Reason != "WaitingForNamespacePolicy" {
		t.Errorf("namespace component = %#v", component)
	}
}

func TestNetEyeTenantReportsAForeignNamespaceAdmissionPolicy(t *testing.T) {
	for _, gvk := range []schema.GroupVersionKind{validatingAdmissionPolicyGVK, validatingAdmissionPolicyBindingGVK} {
		t.Run(gvk.Kind, func(t *testing.T) {
			objects := tenantNamespacePolicyObjects()
			for _, object := range objects {
				if object.GetObjectKind().GroupVersionKind() == gvk {
					object.SetOwnerReferences(nil)
				}
			}
			r, c := newTenantReconcilerWithoutPolicy(t, append(objects, plainNamespace(testTenant), tenantResource())...)
			reconcileTenant(t, r)

			tenant := getTenant(t, c, testTenant, testTenant)
			if component := tenant.Status.Components[string(tenantNamespaceComponentID)]; component.Status != neteye.ComponentStateDegraded || component.Reason != "NamespacePolicyConflict" {
				t.Errorf("namespace component = %#v", component)
			}
			if degraded := conditionOf(t, tenant, neteye.ConditionDegraded); degraded.Status != metav1.ConditionTrue {
				t.Errorf("Degraded = %#v", degraded)
			}
		})
	}
}

func TestNetEyeTenantAcceptsDefaultsThatTheAPIServerAdds(t *testing.T) {
	objects := tenantNamespacePolicyObjects()
	policy := objects[1].(*unstructured.Unstructured)
	spec := tenantNamespacePolicySpec(testOperatorUsername)
	spec["futureDefault"] = "value"
	spec["matchConstraints"].(map[string]any)["futureDefault"] = "value"
	policy.Object["spec"] = spec
	r, c := newTenantReconcilerWithoutPolicy(t, append(append(objects, permissionSyncPrerequisites()...), plainNamespace(testTenant), tenantResource())...)
	reconcileTenantUntilReady(t, r, c)
	if ready := conditionOf(t, getTenant(t, c, testTenant, testTenant), neteye.ConditionReady); ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %#v, want true when the API server only adds fields", ready)
	}
}

func TestNetEyeTenantReportsAPolicyOfAnEarlierCRD(t *testing.T) {
	// After the CRD is created again, the old policy objects still name the
	// old CRD and garbage collection removes them.
	objects := tenantNamespacePolicyObjects()
	objects[0].SetUID("new-crd-uid")
	r, c := newTenantReconcilerWithoutPolicy(t, append(objects, plainNamespace(testTenant), tenantResource())...)
	reconcileTenant(t, r)
	if component := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantNamespaceComponentID)]; component.Reason != "NamespacePolicyConflict" {
		t.Errorf("namespace component = %#v", component)
	}
}

func TestNetEyeTenantWaitsForTheNetEyeResource(t *testing.T) {
	objects := append(tenantNamespacePolicyObjects(), plainNamespace(testTenant), tenantResource())
	r, c := newTenantReconcilerWithoutPolicy(t, objects...)
	reconcileTenant(t, r)
	markTenantRealmReady(t, c, testTenant)
	reconcileTenant(t, r)
	tenant := getTenant(t, c, testTenant, testTenant)
	if component := tenant.Status.Components[string(tenantPermissionSyncComponentID)]; component.Status != neteye.ComponentStateProgressing || component.Reason != "WaitingForNetEye" {
		t.Errorf("permission-sync component = %#v", component)
	}
	if degraded := conditionOf(t, tenant, neteye.ConditionDegraded); degraded.Status != metav1.ConditionFalse {
		t.Errorf("Degraded = %#v, want false while a prerequisite is missing", degraded)
	}
}

func TestNetEyeTenantDeploysPermissionSyncAfterItsNamespace(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenantUntilReady(t, r, c)
	component := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantPermissionSyncComponentID)]
	if component.Status != neteye.ComponentStateReady {
		t.Fatalf("permission-sync component = %#v", component)
	}
	if want := []neteye.NetEyeResolvedImage{{Name: permissionsync.DeploymentName, Image: mustRelease(t).PermissionSyncImage}}; !reflect.DeepEqual(component.ResolvedImages, want) {
		t.Errorf("resolved images = %#v, want %#v", component.ResolvedImages, want)
	}
}

func TestNetEyeTenantBlocksPermissionSyncWhileTheNamespaceIsNotReady(t *testing.T) {
	r, c := newTenantReconciler(t, tenantResource())
	reconcileTenant(t, r)
	if component := getTenant(t, c, testTenant, testTenant).Status.Components[string(tenantPermissionSyncComponentID)]; component.Status != neteye.ComponentStateBlocked {
		t.Errorf("permission-sync component = %#v, want blocked by the namespace", component)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: permissionsync.DeploymentName}, &appsv1.Deployment{}); err == nil {
		t.Error("PermissionSync was deployed before its namespace was ready")
	}
}

func TestTenantsForSecretMapsTheSecretsThatPermissionSyncReads(t *testing.T) {
	r, _ := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	adminCredentials := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: keycloak.AdminSecretName}}
	if requests := r.tenantsForSecret(context.Background(), adminCredentials); len(requests) != 1 || requests[0].Name != testTenant || requests[0].Namespace != testTenant {
		t.Errorf("requests for the realm administrator Secret = %v", requests)
	}
	unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "application"}}
	if requests := r.tenantsForSecret(context.Background(), unrelated); len(requests) != 0 {
		t.Errorf("requests for an unrelated Secret of the tenant = %v", requests)
	}
	withGLPI := tenantResource()
	withGLPI.Spec.PermissionSync.GLPI = &neteye.NetEyeTenantPermissionSyncGLPISpec{Endpoint: "https://node.example.com/glpi/apirest.php", CredentialsSecretRef: neteye.NetEyeTenantPermissionSyncGLPICredentials{Name: "permissionsync-glpi-credentials"}}
	r, _ = newTenantReconciler(t, plainNamespace(testTenant), withGLPI)
	glpiCredentials := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "permissionsync-glpi-credentials"}}
	if requests := r.tenantsForSecret(context.Background(), glpiCredentials); len(requests) != 1 {
		t.Errorf("requests for the GLPI credentials = %v, want the tenant", requests)
	}
	rootCA := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: permissionsync.RootCASecretName}}
	if requests := r.tenantsForSecret(context.Background(), rootCA); len(requests) != 1 {
		t.Errorf("requests for the root CA = %v, want every tenant", requests)
	}
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: "other"}}
	if requests := r.tenantsForSecret(context.Background(), other); len(requests) != 0 {
		t.Errorf("requests for an unrelated Secret = %v", requests)
	}
}

func TestTenantsForCABundleMapsTheCABundleOfTheNetEyeResource(t *testing.T) {
	r, _ := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: neteye.DefaultCABundleConfigMapName}}
	if requests := r.tenantsForCABundle(context.Background(), bundle); len(requests) != 1 {
		t.Errorf("requests for the CA bundle = %v, want every tenant", requests)
	}
	other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: "other"}}
	if requests := r.tenantsForCABundle(context.Background(), other); len(requests) != 0 {
		t.Errorf("requests for an unrelated ConfigMap = %v", requests)
	}
	inTenant := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: neteye.DefaultCABundleConfigMapName}}
	if requests := r.tenantsForCABundle(context.Background(), inTenant); len(requests) != 0 {
		t.Errorf("requests for a ConfigMap of the tenant namespace = %v", requests)
	}
}

func mustRelease(t *testing.T) neteye.NetEyeComponents {
	t.Helper()
	release, ok := neteye.ComponentsForVersion(neteye.CurrentNetEyeVersion)
	if !ok {
		t.Fatal("no release data for the current version")
	}
	return release
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

func TestNetEyeTenantDeletionLeavesTheMetadataOfAnotherTenant(t *testing.T) {
	namespace := plainNamespace(testTenant)
	namespace.Labels = map[string]string{neteye.TenantLabel: "neteye-tenant-aaaaaaaa"}
	namespace.Annotations = map[string]string{neteye.TenantDisplayNameAnnotation: "Other"}
	tenant := tenantResource()
	tenant.Finalizers = []string{NetEyeTenantFinalizer}
	r, c := newTenantReconciler(t, namespace, tenant)
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)

	if got := getNamespace(t, c, testTenant); got.Labels[neteye.TenantLabel] != "neteye-tenant-aaaaaaaa" || got.Annotations[neteye.TenantDisplayNameAnnotation] != "Other" {
		t.Errorf("metadata of another tenant changed: labels %v, annotations %v", got.Labels, got.Annotations)
	}
}

func TestNetEyeTenantDeletionOfAnInvalidTenantChangesNoNamespace(t *testing.T) {
	// A tenant in the wrong namespace that got a finalizer by hand must not
	// remove the metadata of the tenant that owns that namespace.
	namespace := plainNamespace(testTenant)
	namespace.Labels = map[string]string{neteye.TenantLabel: testTenant}
	namespace.Annotations = map[string]string{neteye.TenantDisplayNameAnnotation: "Default"}
	invalid := &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "neteye-tenant-aaaaaaaa", Finalizers: []string{NetEyeTenantFinalizer}}, Spec: neteye.NetEyeTenantSpec{DisplayName: "Other"}}
	r, c := newTenantReconciler(t, namespace, invalid)
	if err := c.Delete(context.Background(), invalid); err != nil {
		t.Fatal(err)
	}
	reconcileTenantRequest(t, r, testTenant, invalid.Name)

	if got := getNamespace(t, c, testTenant); got.Labels[neteye.TenantLabel] != testTenant || got.Annotations[neteye.TenantDisplayNameAnnotation] != "Default" {
		t.Errorf("deleting an invalid tenant changed the namespace: labels %v, annotations %v", got.Labels, got.Annotations)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: invalid.Name}, &neteye.NetEyeTenant{}); err == nil {
		t.Error("finalizer of the invalid tenant was not removed")
	}
}

func TestNetEyeTenantDeletionReadsTheNamespaceFromTheAPIServer(t *testing.T) {
	// The cache can still show the namespace without the metadata that the
	// first reconciliation wrote. The deletion must not trust it.
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)
	r.Client = staleNamespaceClient{Client: c}
	r.APIReader = c
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if _, labeled := getNamespace(t, c, testTenant).Labels[neteye.TenantLabel]; labeled {
		t.Error("tenant label left on the namespace after a stale cache read")
	}
}

// staleNamespaceClient returns every namespace without labels and
// annotations, like a cache that has not seen the last change yet.
type staleNamespaceClient struct {
	client.Client
}

func (c staleNamespaceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if namespace, ok := obj.(*corev1.Namespace); ok {
		namespace.Labels, namespace.Annotations = nil, nil
	}
	return nil
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
	for _, name := range []string{"kube-system", neteye.NetEyeNamespace} {
		if requests := tenantForNamespace(context.Background(), plainNamespace(name)); len(requests) != 0 {
			t.Errorf("requests for namespace %s = %v", name, requests)
		}
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
