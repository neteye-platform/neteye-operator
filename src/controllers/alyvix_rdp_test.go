// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/alyvix"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// rdpNetEye is the NetEye resource that selects the images and the Gateway of
// the RDP access.
func rdpNetEye() *neteye.NetEye {
	return &neteye.NetEye{
		ObjectMeta: metav1.ObjectMeta{Name: "neteye", Namespace: neteye.NetEyeNamespace},
		Spec: neteye.NetEyeSpec{
			Version:                      neteye.CurrentNetEyeVersion,
			InternalCertificateIssuerRef: "internal-issuer",
			Gateway:                      neteye.NetEyeGatewaySpec{Name: "neteye", ClassName: "cilium"},
			Identity: neteye.NetEyeIdentitySpec{
				Hostname: "keycloak.example.com",
				DBConnection: neteye.NetEyeDBConnectionSpec{
					Host:           "mariadb.example.com",
					DBName:         "keycloak",
					UsernameSecret: &neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "username"},
					PasswordSecret: &neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "password"},
				},
			},
		},
	}
}

// alyvixTenant is a tenant that enables or disables the Alyvix feature module.
func alyvixTenant(name string, enabled bool) *neteye.NetEyeTenant {
	tenant := tenantResource()
	tenant.Name, tenant.Namespace, tenant.UID = name, name, types.UID(name+"-uid")
	tenant.Spec.Alyvix = &neteye.NetEyeAlyvixSpec{Enabled: enabled}
	return tenant
}

func newTenantReconcilerWithRDP(t *testing.T, objects ...client.Object) (*NetEyeTenantReconciler, client.Client) {
	t.Helper()
	r, c := newTenantReconciler(t, objects...)
	r.RDPComponent = alyvix.NewRDPComponent(c)
	return r, c
}

// rdpResult runs the RDP component of the tenant once more, as a
// reconciliation does after the namespace, and returns its result: the status
// of the tenant names only the components that are not ready.
func rdpResult(t *testing.T, r *NetEyeTenantReconciler, c client.Client, tenant string) componentResult {
	t.Helper()
	result, err := r.reconcileAlyvixRDP(context.Background(), getTenant(t, c, tenant, tenant))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNetEyeTenantDeploysTheRDPAccessWhenTheTenantEnablesAlyvix(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), alyvixTenant(testTenant, true), rdpNetEye())
	reconcileTenant(t, r)

	// The install of the tenant waits for the RDP access.
	tenant := getTenant(t, c, testTenant, testTenant)
	assertTenantPhase(t, tenant, neteye.PhaseInstalling, "alyvix-rdp: ")
	if result := rdpResult(t, r, c, testTenant); result.State != componentStateProgressing || result.Reason != alyvix.ReasonDeploymentNotAvailable {
		t.Fatalf("alyvix-rdp component = %#v, want progressing until the Deployments are ready", result)
	}
	images, _ := neteye.ComponentsForVersion(neteye.CurrentNetEyeVersion)

	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacamoleName}, deployment); err != nil {
		t.Fatalf("guacamole Deployment: %v", err)
	}
	if owners := deployment.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "NetEyeTenant" || owners[0].UID != tenant.UID {
		t.Errorf("guacamole Deployment owners = %#v, want the tenant", owners)
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Image; got != images.GuacamoleImage {
		t.Errorf("guacamole image = %q, want %q", got, images.GuacamoleImage)
	}
	guacd := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacdName}, guacd); err != nil || guacd.Spec.Template.Spec.Containers[0].Image != images.GuacdImage {
		t.Errorf("guacd Deployment = %v, err = %v, want the image %q", guacd.Spec.Template.Spec.Containers, err, images.GuacdImage)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.HTTPRouteName}, route); err != nil {
		t.Fatalf("route: %v", err)
	}
	if parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs"); len(parents) != 1 || parents[0].(map[string]any)["namespace"] != neteye.NetEyeNamespace || parents[0].(map[string]any)["name"] != "neteye" {
		t.Errorf("route parents = %v, want the Gateway of the NetEye resource", parents)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.JSONAuthSecretName}, secret); err != nil || len(secret.Data[alyvix.JSONAuthSecretKey]) != 32 {
		t.Errorf("JSON authentication Secret = %v, err = %v", secret.Data, err)
	}
}

func TestNetEyeTenantReportsTheRDPAccessDisabledWithoutAlyvix(t *testing.T) {
	for name, tenant := range map[string]*neteye.NetEyeTenant{
		"no alyvix section": tenantResource(),
		"alyvix disabled":   alyvixTenant(testTenant, false),
	} {
		t.Run(name, func(t *testing.T) {
			r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), tenant, rdpNetEye())
			reconcileTenant(t, r)

			if result := rdpResult(t, r, c, testTenant); result.State != componentStateReady || result.Reason != "Disabled" {
				t.Errorf("alyvix-rdp component = %#v, want disabled", result)
			}
			// A disabled component does not keep the tenant from being ready.
			assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseReady, "All components are ready")
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacamoleName}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
				t.Errorf("guacamole Deployment err = %v, want not found", err)
			}
		})
	}
}

func TestNetEyeTenantsEnableAlyvixIndependently(t *testing.T) {
	const other = "neteye-tenant-3cj57cgw"
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), plainNamespace(other), alyvixTenant(testTenant, true), alyvixTenant(other, false), rdpNetEye())
	reconcileTenantRequest(t, r, testTenant, testTenant)
	reconcileTenantRequest(t, r, other, other)

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacamoleName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("guacamole Deployment of the tenant with Alyvix: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: other, Name: alyvix.GuacamoleName}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("guacamole Deployment of the tenant without Alyvix: err = %v, want not found", err)
	}
	if result := rdpResult(t, r, c, other); result.Reason != "Disabled" {
		t.Errorf("alyvix-rdp component of the tenant without Alyvix = %#v, want disabled", result)
	}
}

func TestNetEyeTenantRemovesTheRDPAccessAndKeepsItsKeyWhenDisabled(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), alyvixTenant(testTenant, true), rdpNetEye())
	reconcileTenant(t, r)
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.JSONAuthSecretName}, secret); err != nil {
		t.Fatal(err)
	}
	key := string(secret.Data[alyvix.JSONAuthSecretKey])

	tenant := getTenant(t, c, testTenant, testTenant)
	tenant.Spec.Alyvix.Enabled = false
	if err := c.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)

	for _, object := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		for _, name := range []string{alyvix.GuacamoleName, alyvix.GuacdName} {
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, object); !apierrors.IsNotFound(err) {
				t.Errorf("%T %s err = %v, want deleted", object, name, err)
			}
		}
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.JSONAuthSecretName}, secret); err != nil || string(secret.Data[alyvix.JSONAuthSecretKey]) != key {
		t.Errorf("JSON authentication key after disable: err = %v, changed = %v", err, string(secret.Data[alyvix.JSONAuthSecretKey]) != key)
	}
	if result := rdpResult(t, r, c, testTenant); result.Reason != "Disabled" {
		t.Errorf("alyvix-rdp component = %#v, want disabled", result)
	}
}

// The removal of a disabled RDP access does not wait for the namespace
// baseline: here the admission policy is missing.
func TestNetEyeTenantRemovesADisabledRDPAccessWithoutTheNamespacePolicy(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), alyvixTenant(testTenant, true), rdpNetEye())
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacamoleName}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("guacamole Deployment before disable: %v", err)
	}

	for _, object := range tenantNamespacePolicyObjects() {
		if err := c.Delete(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	tenant := getTenant(t, c, testTenant, testTenant)
	tenant.Spec.Alyvix.Enabled = false
	if err := c.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)

	// The namespace is not ready without the policy, and the disabled RDP
	// access is removed all the same: it is not among the components that the
	// tenant waits for.
	tenant = getTenant(t, c, testTenant, testTenant)
	assertTenantPhase(t, tenant, neteye.PhaseFailed, "namespace: ")
	if strings.Contains(tenant.Status.Message, string(tenantAlyvixRDPComponentID)) {
		t.Errorf("message = %q, want the disabled RDP access not reported", tenant.Status.Message)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacamoleName}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("guacamole Deployment err = %v, want deleted", err)
	}
}

func TestNetEyeTenantRDPAccessUsesThePortsOfTheTenant(t *testing.T) {
	tenant := alyvixTenant(testTenant, true)
	tenant.Spec.Alyvix.RDP = &neteye.NetEyeAlyvixRDPSpec{Ports: []int32{3390}}
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), tenant, rdpNetEye())
	reconcileTenant(t, r)

	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(networkPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacdEgressPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	egress, _, _ := unstructured.NestedSlice(policy.Object, "spec", "egress")
	if got := egress[len(egress)-1].(map[string]any)["ports"]; !reflect.DeepEqual(got, []any{map[string]any{"protocol": "TCP", "port": int64(3390)}}) {
		t.Errorf("guacd egress ports = %v, want only 3390", got)
	}
}

func TestNetEyeTenantRDPAccessWaitsForTheNetEyeResource(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), alyvixTenant(testTenant, true))
	reconcileTenant(t, r)

	assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseInstalling, "alyvix-rdp: waiting for the NetEye resource")
	if result := rdpResult(t, r, c, testTenant); result.State != componentStateProgressing || result.Reason != "WaitingForNetEye" {
		t.Errorf("alyvix-rdp component = %#v, want waiting for the NetEye resource", result)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.JSONAuthSecretName}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("Secret err = %v, want nothing created without the NetEye resource", err)
	}
}

func TestNetEyeTenantRDPAccessWaitsForTheNamespace(t *testing.T) {
	// Without the admission policy the namespace component is not ready.
	r, c := newTenantReconcilerWithoutPolicy(t, plainNamespace(testTenant), alyvixTenant(testTenant, true), rdpNetEye())
	r.RDPComponent = alyvix.NewRDPComponent(c)
	reconcileTenant(t, r)

	// The namespace fails without the policy, and the RDP access waits for it.
	assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseFailed, "namespace: ")
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.JSONAuthSecretName}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("Secret err = %v, want nothing created before the namespace is ready", err)
	}
}

func TestReconcileAlyvixRDPAddsTheTenantListenerAndCertificate(t *testing.T) {
	c, s := startEnvtest(t)
	ctx := context.Background()
	ns := keycloak.WorkloadNamespace
	for _, name := range []string{ns, testTenant} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatalf("create namespace %s: %v", name, err)
		}
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, ns, "internal-issuer")); err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	ne := rdpNetEye()
	if err := c.Create(ctx, ne); err != nil {
		t.Fatalf("create neteye: %v", err)
	}
	tenant := &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Name: testTenant, Namespace: testTenant}, Spec: neteye.NetEyeTenantSpec{Version: neteye.CurrentNetEyeVersion, DisplayName: "Default"}}
	if err := c.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	r := &NetEyeReconciler{
		Client:                 c,
		Log:                    logr.Discard(),
		Scheme:                 s,
		KeycloakComponent:      keycloak.NewComponent(c, logr.Discard()),
		OTelCollectorComponent: elasticstack.NewOTelCollectorComponent(c),
		EDOTGatewayComponent:   elasticstack.NewEDOTGatewayComponent(c),
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}
	listener := func() map[string]any {
		t.Helper()
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		gateway := requireExists(ctx, t, c, gatewayGVK, ns, "neteye")
		listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
		for _, raw := range listeners {
			if raw.(map[string]any)["name"] == alyvix.GatewayListenerName {
				return raw.(map[string]any)
			}
		}
		return nil
	}
	setAlyvix := func(enabled bool) {
		t.Helper()
		current := &neteye.NetEyeTenant{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(tenant), current); err != nil {
			t.Fatal(err)
		}
		current.Spec.Alyvix = &neteye.NetEyeAlyvixSpec{Enabled: enabled}
		if err := c.Update(ctx, current); err != nil {
			t.Fatal(err)
		}
	}

	if got := listener(); got != nil {
		t.Errorf("RDP listener = %v without a tenant that enables Alyvix, want none", got)
	}

	setAlyvix(true)
	got := listener()
	if got == nil || got["hostname"] != alyvix.GatewayListenerHostname || got["port"] != int64(443) || got["protocol"] != "HTTPS" {
		t.Fatalf("RDP listener = %v", got)
	}
	namespaces, _, _ := unstructured.NestedMap(got, "allowedRoutes", "namespaces")
	wantNamespaces := map[string]any{"from": "Selector", "selector": map[string]any{"matchExpressions": []any{map[string]any{"key": neteye.TenantLabel, "operator": "Exists"}}}}
	if !reflect.DeepEqual(namespaces, wantNamespaces) {
		t.Errorf("RDP listener namespaces = %v, want only tenant namespaces", namespaces)
	}
	certificate := requireExists(ctx, t, c, certificateGVK, ns, alyvix.TLSCertificateName)
	if owners := certificate.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "NetEye" || owners[0].Name != ne.Name || owners[0].Controller == nil || !*owners[0].Controller {
		t.Errorf("certificate owners = %#v, want the NetEye resource", owners)
	}
	dnsNames, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	secretName, _, _ := unstructured.NestedString(certificate.Object, "spec", "secretName")
	if !reflect.DeepEqual(dnsNames, []string{alyvix.GatewayListenerHostname}) || secretName != alyvix.TLSSecretName {
		t.Errorf("certificate dnsNames = %v, secretName = %q", dnsNames, secretName)
	}

	setAlyvix(false)
	if got := listener(); got != nil {
		t.Errorf("RDP listener still present after disable: %v", got)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: alyvix.TLSCertificateName}, newUnstructured(certificateGVK, ns, alyvix.TLSCertificateName)); !apierrors.IsNotFound(err) {
		t.Errorf("certificate err = %v, want deleted after disable", err)
	}
}

// A Certificate of another controller with the name of the RDP certificate
// must not stop the Gateway and the other components.
func TestReconcileAlyvixRDPCertificateFailureDoesNotStopTheOtherResources(t *testing.T) {
	c, s := startEnvtest(t)
	ctx := context.Background()
	ns := keycloak.WorkloadNamespace
	for _, name := range []string{ns, testTenant} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatalf("create namespace %s: %v", name, err)
		}
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, ns, "internal-issuer")); err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	foreign := newUnstructured(certificateGVK, ns, alyvix.TLSCertificateName)
	foreign.Object["spec"] = map[string]any{"secretName": "other", "dnsNames": []any{"other.example.com"}, "issuerRef": map[string]any{"name": "internal-issuer"}}
	foreign.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other-uid", Controller: ptr.To(true)}})
	if err := c.Create(ctx, foreign); err != nil {
		t.Fatalf("create foreign certificate: %v", err)
	}
	ne := rdpNetEye()
	if err := c.Create(ctx, ne); err != nil {
		t.Fatalf("create neteye: %v", err)
	}
	tenant := &neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Name: testTenant, Namespace: testTenant}, Spec: neteye.NetEyeTenantSpec{Version: neteye.CurrentNetEyeVersion, DisplayName: "Default", Alyvix: &neteye.NetEyeAlyvixSpec{Enabled: true}}}
	if err := c.Create(ctx, tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	r := &NetEyeReconciler{
		Client:                 c,
		Log:                    logr.Discard(),
		Scheme:                 s,
		KeycloakComponent:      keycloak.NewComponent(c, logr.Discard()),
		OTelCollectorComponent: elasticstack.NewOTelCollectorComponent(c),
		EDOTGatewayComponent:   elasticstack.NewEDOTGatewayComponent(c),
	}
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > DefaultFailureRequeueAfter {
		t.Errorf("requeueAfter = %v, want a retry within %v", result.RequeueAfter, DefaultFailureRequeueAfter)
	}
	gateway := requireExists(ctx, t, c, gatewayGVK, ns, "neteye")
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	gatewayListenerByName(t, listeners, keycloak.GatewayListenerName)
	gatewayListenerByName(t, listeners, alyvix.GatewayListenerName)
	requireExists(ctx, t, c, httpRouteGVK, ns, resources.HTTPToHTTPSRedirectRouteName)

	current := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase == neteye.PhaseReady || !strings.Contains(current.Status.Message, "Alyvix RDP") {
		t.Errorf("phase = %q, message = %q, want the Alyvix certificate failure reported", current.Status.Phase, current.Status.Message)
	}
	if owners := requireExists(ctx, t, c, certificateGVK, ns, alyvix.TLSCertificateName).GetOwnerReferences(); len(owners) != 1 || owners[0].UID != "other-uid" {
		t.Errorf("certificate owners = %#v, want the other controller kept", owners)
	}
}

func TestDeleteGatewayRunsEveryDeletion(t *testing.T) {
	scheme := tenantScheme(t)
	owner := metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "neteye", UID: "neteye-uid", Controller: ptr.To(true)}
	owned := func(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
		object := newUnstructured(gvk, keycloak.WorkloadNamespace, name)
		object.SetOwnerReferences([]metav1.OwnerReference{owner})
		return object
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(owned(httpRouteGVK, resources.HTTPToHTTPSRedirectRouteName), owned(certificateGVK, alyvix.TLSCertificateName), owned(gatewayGVK, "neteye")).
		WithInterceptorFuncs(interceptor.Funcs{Delete: func(ctx context.Context, inner client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			if object.GetObjectKind().GroupVersionKind().Kind == "Certificate" {
				return errors.New("certificate deletion failed")
			}
			return inner.Delete(ctx, object, opts...)
		}}).Build()

	err := deleteGateway(context.Background(), c, owner)
	if err == nil || !strings.Contains(err.Error(), "certificate deletion failed") {
		t.Errorf("err = %v, want the certificate failure", err)
	}
	for _, object := range []*unstructured.Unstructured{newUnstructured(httpRouteGVK, keycloak.WorkloadNamespace, resources.HTTPToHTTPSRedirectRouteName), newUnstructured(gatewayGVK, keycloak.WorkloadNamespace, "neteye")} {
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s err = %v, want deleted although the certificate deletion failed", object.GetKind(), object.GetName(), err)
		}
	}
}

func TestNetEyeChangesOfTenantsReachTheNetEyeResource(t *testing.T) {
	scheme := tenantScheme(t)
	ne := rdpNetEye()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ne).Build()
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: scheme}
	requests := r.netEyesForTenant(context.Background(), alyvixTenant(testTenant, true))
	if want := []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(ne)}}; !reflect.DeepEqual(requests, want) {
		t.Errorf("requests = %v, want %v", requests, want)
	}
}

func TestAPIServerValidatesTheAlyvixRDPPorts(t *testing.T) {
	c, _ := startEnvtest(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testTenant}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	newTenant := func(ports []int32) *neteye.NetEyeTenant {
		return &neteye.NetEyeTenant{
			ObjectMeta: metav1.ObjectMeta{Name: testTenant, Namespace: testTenant},
			Spec:       neteye.NetEyeTenantSpec{Version: neteye.CurrentNetEyeVersion, DisplayName: "Default", Alyvix: &neteye.NetEyeAlyvixSpec{Enabled: true, RDP: &neteye.NetEyeAlyvixRDPSpec{Ports: ports}}},
		}
	}
	for name, ports := range map[string][]int32{"port 0": {0}, "port 65536": {65536}, "duplicate": {3389, 3389}} {
		if err := c.Create(ctx, newTenant(ports)); !apierrors.IsInvalid(err) {
			t.Errorf("%s: create err = %v, want invalid", name, err)
		}
	}
	tenant := newTenant(nil)
	tenant.Spec.Alyvix.RDP = nil
	if err := c.Create(ctx, tenant); err != nil {
		t.Fatalf("create valid tenant: %v", err)
	}
	if got := tenant.Spec.Alyvix.EffectiveRDPPorts(); len(got) != 1 || got[0] != neteye.DefaultAlyvixRDPPort {
		t.Errorf("effective ports = %v, want the default", got)
	}
}
