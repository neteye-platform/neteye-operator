// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/alyvix"
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

// readyRDPCA is the intermediate CA of a tenant in the shared namespace, as
// cert-manager leaves it when the Certificate is ready.
func readyRDPCA(tenant string) []client.Object {
	certificate := newUnstructured(certificateGVK, neteye.NetEyeNamespace, alyvix.CANamePrefix+tenant)
	certificate.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "neteye-operator"})
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	secret := &corev1.Secret{
		// cert-manager names the Certificate that wrote the Secret.
		ObjectMeta: metav1.ObjectMeta{Namespace: neteye.NetEyeNamespace, Name: alyvix.CANamePrefix + tenant, Annotations: map[string]string{"cert-manager.io/certificate-name": alyvix.CANamePrefix + tenant}},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("ca-chain"), corev1.TLSPrivateKeyKey: []byte("ca-key")},
	}
	// The shared Gateway has its address: the Gateway of the tenant waits for it.
	shared := newUnstructured(gatewayGVK, neteye.NetEyeNamespace, "neteye")
	shared.Object["status"] = map[string]any{"addresses": []any{map[string]any{"type": "IPAddress", "value": "192.0.2.128"}}}
	return []client.Object{certificate, secret, shared}
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

func TestNetEyeTenantGivesGuacdTheCABundleOfTheTenant(t *testing.T) {
	for name, test := range map[string]struct {
		caBundle *neteye.NetEyeConfigMapKeySelector
		wantName string
		wantKey  string
	}{
		"default": {wantName: neteye.DefaultCABundleConfigMapName, wantKey: neteye.DefaultCABundleKey},
		"custom":  {caBundle: &neteye.NetEyeConfigMapKeySelector{Name: "tenant-cas", Key: "cas.pem"}, wantName: "tenant-cas", wantKey: "cas.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			tenant := alyvixTenant(testTenant, true)
			tenant.Spec.CABundle = test.caBundle
			r, c := newTenantReconcilerWithRDP(t, plainNamespace(testTenant), tenant, rdpNetEye())
			reconcileTenant(t, r)

			guacd := &appsv1.Deployment{}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: alyvix.GuacdName}, guacd); err != nil {
				t.Fatalf("guacd Deployment: %v", err)
			}
			volumes := guacd.Spec.Template.Spec.Volumes
			if len(volumes) != 1 || volumes[0].ConfigMap == nil || volumes[0].ConfigMap.Name != test.wantName || volumes[0].ConfigMap.Items[0].Key != test.wantKey {
				t.Errorf("guacd volumes = %+v, want the ConfigMap key %s/%s of the tenant namespace", volumes, test.wantName, test.wantKey)
			}
		})
	}
}

func TestNetEyeTenantDeploysTheRDPAccessWhenTheTenantEnablesAlyvix(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, append(readyRDPCA(testTenant), plainNamespace(testTenant), alyvixTenant(testTenant, true), rdpNetEye())...)
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
	if parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs"); len(parents) != 1 || parents[0].(map[string]any)["namespace"] != testTenant || parents[0].(map[string]any)["name"] != alyvix.GatewayName {
		t.Errorf("route parents = %v, want the Gateway of the tenant", parents)
	}
	// The tenant has its own Gateway, with the class of the NetEye resource, and
	// its CA is signed by the internal Issuer of the NetEye resource.
	gateway := requireExists(context.Background(), t, c, gatewayGVK, testTenant, alyvix.GatewayName)
	if class, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName"); class != "cilium" {
		t.Errorf("gatewayClassName = %q, want the class of the NetEye resource", class)
	}
	if hostname := alyvix.Hostname(testTenant); !strings.HasPrefix(hostname, "guacamole.tenant-") || !strings.HasSuffix(hostname, ".neteyelocal") {
		t.Errorf("hostname = %q", hostname)
	}
	requireExists(context.Background(), t, c, certificateGVK, testTenant, alyvix.TLSCertificateName)
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

func TestNetEyeTenantDeletionRemovesTheIntermediateCAFromTheSharedNamespace(t *testing.T) {
	r, c := newTenantReconcilerWithRDP(t, append(readyRDPCA(testTenant), plainNamespace(testTenant), alyvixTenant(testTenant, true), rdpNetEye())...)
	reconcileTenant(t, r)
	requireExists(context.Background(), t, c, certificateGVK, neteye.NetEyeNamespace, alyvix.CANamePrefix+testTenant)

	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)

	certificate := newUnstructured(certificateGVK, neteye.NetEyeNamespace, alyvix.CANamePrefix+testTenant)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(certificate), certificate); !apierrors.IsNotFound(err) {
		t.Errorf("CA Certificate err = %v, want deleted with the tenant", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: neteye.NetEyeNamespace, Name: alyvix.CANamePrefix + testTenant}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("CA Secret err = %v, want deleted with the tenant", err)
	}
}
