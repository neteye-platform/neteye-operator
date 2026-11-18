// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/alyvix"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
)

// alyvixNetEye is the NetEye resource that the installer renders when the
// Alyvix feature module is installed.
func alyvixNetEye(enabled bool) *neteye.NetEye {
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
			Alyvix: &neteye.NetEyeAlyvixSpec{RDP: &neteye.NetEyeAlyvixRDPSpec{Enabled: enabled}},
		},
	}
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

func TestNetEyeTenantDeploysTheRDPAccessWhenTheNetEyeResourceEnablesIt(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), tenantResource(), alyvixNetEye(true))
	reconcileTenant(t, r)

	// The install of the tenant waits for the RDP access.
	assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseInstalling, "alyvix-rdp: ")
	if result := rdpResult(t, r, c, testTenant); result.State != componentStateProgressing || result.Reason != alyvix.ReasonDeploymentNotAvailable {
		t.Fatalf("alyvix-rdp component = %#v, want progressing until the Deployments are ready", result)
	}
	images, _ := neteye.ComponentsForVersion(neteye.CurrentNetEyeVersion)

	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacamoleName}, deployment); err != nil {
		t.Fatalf("guacamole Deployment: %v", err)
	}
	if owners := deployment.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "NetEyeTenant" || owners[0].UID != "tenant-uid" {
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

func TestNetEyeTenantReportsTheRDPAccessDisabledWithoutTheNetEyeSwitch(t *testing.T) {
	for name, objects := range map[string][]client.Object{
		"no NetEye resource":     nil,
		"RDP access disabled":    {alyvixNetEye(false)},
		"Alyvix section missing": {func() client.Object { ne := alyvixNetEye(false); ne.Spec.Alyvix = nil; return ne }()},
	} {
		t.Run(name, func(t *testing.T) {
			r, c := newTenantReconcilerWithRDP(t, append([]client.Object{plainNamespace(testTenant), tenantResource()}, objects...)...)
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

func TestNetEyeTenantRemovesTheRDPAccessAndKeepsItsKeyWhenDisabled(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), tenantResource(), alyvixNetEye(true))
	reconcileTenant(t, r)
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.JSONAuthSecretName}, secret); err != nil {
		t.Fatal(err)
	}
	key := string(secret.Data[alyvix.JSONAuthSecretKey])

	ne := &neteye.NetEye{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: neteye.NetEyeNamespace, Name: "neteye"}, ne); err != nil {
		t.Fatal(err)
	}
	ne.Spec.Alyvix = nil
	if err := c.Update(context.Background(), ne); err != nil {
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

func TestNetEyeTenantRDPAccessWaitsForTheNamespace(t *testing.T) {
	// Without the admission policy the namespace component is not ready.
	r, c := newTenantReconcilerWithoutPolicy(t, plainNamespace(testTenant), tenantResource(), alyvixNetEye(true))
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
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, ns, "internal-issuer")); err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	ne := alyvixNetEye(true)
	ne.Spec.Alyvix.RDP.Ports = []int32{3389, 3390}
	if err := c.Create(ctx, ne); err != nil {
		t.Fatalf("create neteye: %v", err)
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
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	gateway := requireExists(ctx, t, c, gatewayGVK, ns, "neteye")
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	listener := gatewayListenerByName(t, listeners, alyvix.GatewayListenerName)
	if listener["hostname"] != alyvix.GatewayListenerHostname || listener["port"] != int64(443) || listener["protocol"] != "HTTPS" {
		t.Errorf("RDP listener = %v", listener)
	}
	namespaces, _, _ := unstructured.NestedMap(listener, "allowedRoutes", "namespaces")
	wantNamespaces := map[string]any{"from": "Selector", "selector": map[string]any{"matchExpressions": []any{map[string]any{"key": neteye.TenantLabel, "operator": "Exists"}}}}
	if !reflect.DeepEqual(namespaces, wantNamespaces) {
		t.Errorf("RDP listener namespaces = %v, want only tenant namespaces", namespaces)
	}
	if keycloakListener := gatewayListenerByName(t, listeners, keycloak.GatewayListenerName); keycloakListener["allowedRoutes"].(map[string]any)["namespaces"].(map[string]any)["from"] != "Same" {
		t.Errorf("keycloak listener = %v, want routes of the shared namespace only", keycloakListener)
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

	current := &neteye.NetEye{}
	if err := c.Get(ctx, req.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Alyvix = nil
	if err := c.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile after disable: %v", err)
	}
	gateway = requireExists(ctx, t, c, gatewayGVK, ns, "neteye")
	listeners, _, _ = unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	for _, raw := range listeners {
		if raw.(map[string]any)["name"] == alyvix.GatewayListenerName {
			t.Errorf("RDP listener still present after disable: %v", raw)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: alyvix.TLSCertificateName}, newUnstructured(certificateGVK, ns, alyvix.TLSCertificateName)); !apierrors.IsNotFound(err) {
		t.Errorf("certificate err = %v, want deleted after disable", err)
	}
}

func TestAPIServerValidatesTheAlyvixRDPPorts(t *testing.T) {
	c, _ := startEnvtest(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: neteye.NetEyeNamespace}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	for name, ports := range map[string][]int32{"port 0": {0}, "port 65536": {65536}, "duplicate": {3389, 3389}} {
		ne := alyvixNetEye(true)
		ne.Name = "invalid"
		ne.Spec.Alyvix.RDP.Ports = ports
		if err := c.Create(ctx, ne); !apierrors.IsInvalid(err) {
			t.Errorf("%s: create err = %v, want invalid", name, err)
		}
	}
	ne := alyvixNetEye(true)
	if err := c.Create(ctx, ne); err != nil {
		t.Fatalf("create valid neteye: %v", err)
	}
	if ne.Spec.Alyvix.RDP.EffectivePorts()[0] != neteye.DefaultAlyvixRDPPort {
		t.Errorf("effective ports = %v, want the default", ne.Spec.Alyvix.RDP.EffectivePorts())
	}
}
