// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package tenantgateway

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	testTenant          = "neteye-tenant-2bh46bfv"
	testSharedNamespace = "neteye-tenant-shared"
	testIssuer          = "neteye-internal-issuer"
	testDomain          = "tenant-2bh46bfv.neteyelocal"
)

var (
	certificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	issuerGVK      = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}
	gatewayGVK     = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}
)

func newClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func owner() metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEyeTenant", Name: testTenant, UID: "tenant-uid", Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}
}

func inputs(services map[string]bool) Inputs {
	return Inputs{Namespace: testTenant, ClassName: "cilium", IssuerName: testIssuer, Shared: resources.GatewayRef{Namespace: testSharedNamespace, Name: "neteye"}, Services: services}
}

func get(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		t.Fatalf("get %s %s/%s: %v", gvk.Kind, namespace, name, err)
	}
	return object
}

func absent(t *testing.T, c client.Client, object client.Object, namespace, name string) bool {
	t.Helper()
	return apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, object))
}

func unstructuredOf(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	return object
}

// readyCA is the CA of the tenant in the shared namespace, as cert-manager
// leaves it when the Certificate is ready, and the shared Gateway with its
// address.
func readyCA() []client.Object {
	certificate := unstructuredOf(certificateGVK)
	certificate.SetNamespace(testSharedNamespace)
	certificate.SetName(SharedCAName(testTenant))
	certificate.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "neteye-operator"})
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	secret := &corev1.Secret{
		// cert-manager names the Certificate that wrote the Secret.
		ObjectMeta: metav1.ObjectMeta{Namespace: testSharedNamespace, Name: SharedCAName(testTenant), Annotations: map[string]string{"cert-manager.io/certificate-name": SharedCAName(testTenant)}},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("ca-chain"), corev1.TLSPrivateKeyKey: []byte("ca-key"), "ca.crt": []byte("root")},
	}
	shared := unstructuredOf(gatewayGVK)
	shared.SetNamespace(testSharedNamespace)
	shared.SetName("neteye")
	shared.Object["status"] = map[string]any{"addresses": []any{map[string]any{"type": "IPAddress", "value": "192.0.2.128"}}}
	return []client.Object{certificate, secret, shared}
}

func markCertificateReady(t *testing.T, c client.Client, name string) {
	t.Helper()
	certificate := get(t, c, certificateGVK, testTenant, name)
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	if err := c.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
}

// markListenersProgrammed writes a programmed status for the listeners of the
// current generation of the Gateway.
func markListenersProgrammed(t *testing.T, c client.Client, names ...string) {
	t.Helper()
	gateway := get(t, c, gatewayGVK, testTenant, GatewayName)
	listeners := make([]any, 0, len(names))
	for _, name := range names {
		listeners = append(listeners, map[string]any{"name": name, "conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": gateway.GetGeneration()}}})
	}
	gateway.Object["status"] = map[string]any{"listeners": listeners}
	if err := c.Update(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
}

func listenersOf(t *testing.T, c client.Client) map[string]map[string]any {
	t.Helper()
	listeners, _, _ := unstructured.NestedSlice(get(t, c, gatewayGVK, testTenant, GatewayName).Object, "spec", "listeners")
	result := map[string]map[string]any{}
	for _, raw := range listeners {
		listener := raw.(map[string]any)
		result[listener["name"].(string)] = listener
	}
	return result
}

func TestHostnamesAreOnTheDomainOfTheTenant(t *testing.T) {
	if got := Domain(testTenant); got != testDomain {
		t.Errorf("domain = %q, want %q", got, testDomain)
	}
	if got := ServiceHostname(testTenant, "guacamole"); got != "guacamole."+testDomain {
		t.Errorf("hostname = %q", got)
	}
	if got := SharedCAName(testTenant); got != "neteye-tenant-ca-2bh46bfv" {
		t.Errorf("CA name = %q, want the id of the tenant", got)
	}
}

func TestTheCAOfTheTenantCanSignOnlyForTheDomainOfTheTenant(t *testing.T) {
	c := newClient(t)
	outcome := NewComponent(c).Ensure(context.Background(), inputs(nil), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome = %+v, want waiting for the CA of the tenant", outcome)
	}
	ca := get(t, c, certificateGVK, testSharedNamespace, SharedCAName(testTenant))
	isCA, _, _ := unstructured.NestedBool(ca.Object, "spec", "isCA")
	issuer, _, _ := unstructured.NestedString(ca.Object, "spec", "issuerRef", "name")
	critical, _, _ := unstructured.NestedBool(ca.Object, "spec", "nameConstraints", "critical")
	domains, _, _ := unstructured.NestedStringSlice(ca.Object, "spec", "nameConstraints", "permitted", "dnsDomains")
	if !isCA || issuer != testIssuer || !critical || !reflect.DeepEqual(domains, []string{testDomain}) {
		t.Errorf("CA spec = %v, want a CA of the internal Issuer, with a critical constraint to %s", ca.Object["spec"], testDomain)
	}
	// Nothing of the Gateway exists before the CA: it could not serve.
	if !absent(t, c, unstructuredOf(gatewayGVK), testTenant, GatewayName) || !absent(t, c, unstructuredOf(issuerGVK), testTenant, CAName) {
		t.Error("the Issuer or the Gateway exists before the CA of the tenant is ready")
	}
}

func TestTheGatewayHasTheListenerOfTheTenantAndOneForEachEnabledService(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewComponent(c)
	component.Ensure(context.Background(), inputs(map[string]bool{"guacamole": true, "other": false}), owner())

	// The tenant namespace has the key of the CA of the tenant, and only it.
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: CAName}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[corev1.TLSPrivateKeyKey]) != "ca-key" || len(secret.GetOwnerReferences()) != 1 {
		t.Errorf("CA Secret of the tenant = %v %v", secret.Data, secret.GetOwnerReferences())
	}
	issuer := get(t, c, issuerGVK, testTenant, CAName)
	if name, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName"); name != CAName {
		t.Errorf("Issuer spec = %v", issuer.Object["spec"])
	}

	listeners := listenersOf(t, c)
	want := map[string]string{TenantListenerName: testDomain, "guacamole": "guacamole." + testDomain}
	if len(listeners) != len(want) {
		t.Fatalf("listeners = %v, want %v", listeners, want)
	}
	for name, hostname := range want {
		listener := listeners[name]
		from, _, _ := unstructured.NestedString(listener, "allowedRoutes", "namespaces", "from")
		secretName, _, _ := unstructured.NestedString(listener["tls"].(map[string]any)["certificateRefs"].([]any)[0].(map[string]any), "name")
		if listener["hostname"] != hostname || listener["protocol"] != "HTTPS" || from != "Same" || secretName != ListenerSecretName(name) {
			t.Errorf("listener %s = %v, want HTTPS on %s for the routes of the namespace, with its certificate", name, listener, hostname)
		}
		certificate := get(t, c, certificateGVK, testTenant, ListenerCertificateName(name))
		dnsNames, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
		issuerName, _, _ := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
		if !reflect.DeepEqual(dnsNames, []string{hostname}) || issuerName != CAName {
			t.Errorf("Certificate of %s = %v, want %s signed by the CA of the tenant", name, certificate.Object["spec"], hostname)
		}
	}
	gateway := get(t, c, gatewayGVK, testTenant, GatewayName)
	if class, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName"); class != "cilium" {
		t.Errorf("gatewayClassName = %q", class)
	}
	if _, found, _ := unstructured.NestedMap(gateway.Object, "spec", "infrastructure"); found {
		t.Error("the Gateway of the tenant has annotations, which can ask for an address")
	}
}

func TestDisablingAServiceRemovesItsListenerAndCertificate(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewComponent(c)
	component.Ensure(context.Background(), inputs(map[string]bool{"guacamole": true}), owner())
	get(t, c, certificateGVK, testTenant, ListenerCertificateName("guacamole"))

	component.Ensure(context.Background(), inputs(map[string]bool{"guacamole": false}), owner())
	if _, found := listenersOf(t, c)["guacamole"]; found {
		t.Error("the listener of a disabled service stays")
	}
	if !absent(t, c, unstructuredOf(certificateGVK), testTenant, ListenerCertificateName("guacamole")) {
		t.Error("the certificate of a disabled service stays")
	}
	// The Gateway stays, with the listener of the tenant.
	if _, found := listenersOf(t, c)[TenantListenerName]; !found {
		t.Error("the Gateway lost the listener of the tenant")
	}
}

func TestTheGatewayIsReadyWhenEveryListenerIsProgrammed(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewComponent(c)
	in := inputs(map[string]bool{"guacamole": true})
	if outcome := component.Ensure(context.Background(), in, owner()); outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome = %+v, want waiting for the certificates of the listeners", outcome)
	}
	markCertificateReady(t, c, ListenerCertificateName(TenantListenerName))
	markCertificateReady(t, c, ListenerCertificateName("guacamole"))
	if outcome := component.Ensure(context.Background(), in, owner()); outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for the listeners", outcome)
	}
	markListenersProgrammed(t, c, TenantListenerName)
	if outcome := component.Ensure(context.Background(), in, owner()); outcome.Phase != PhaseProgressing || !strings.Contains(outcome.Message, "guacamole") {
		t.Fatalf("outcome = %+v, want waiting for the listener of the service", outcome)
	}
	markListenersProgrammed(t, c, TenantListenerName, "guacamole")
	if outcome := component.Ensure(context.Background(), in, owner()); outcome.Phase != PhaseReady || !strings.Contains(outcome.Message, testDomain) {
		t.Fatalf("outcome = %+v, want ready", outcome)
	}
}

// The shared Gateway can ask the load balancer pool for a fixed address. The
// Gateway of a tenant that came first would take that address.
func TestTheGatewayWaitsForTheAddressOfTheSharedGateway(t *testing.T) {
	objects := readyCA()
	delete(objects[len(objects)-1].(*unstructured.Unstructured).Object, "status")
	c := newClient(t, objects...)
	outcome := NewComponent(c).Ensure(context.Background(), inputs(nil), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonSharedGatewayNotReady {
		t.Fatalf("outcome = %+v, want waiting for the address of the shared Gateway", outcome)
	}
	if !absent(t, c, unstructuredOf(gatewayGVK), testTenant, GatewayName) {
		t.Error("the Gateway of the tenant exists before the shared Gateway has its address")
	}
}

func TestTheCopyOfTheCAFollowsItsRenewal(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewComponent(c)
	component.Ensure(context.Background(), inputs(nil), owner())
	renewed := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testSharedNamespace, Name: SharedCAName(testTenant)}, renewed); err != nil {
		t.Fatal(err)
	}
	renewed.Data[corev1.TLSPrivateKeyKey] = []byte("renewed-key")
	if err := c.Update(context.Background(), renewed); err != nil {
		t.Fatal(err)
	}
	component.Ensure(context.Background(), inputs(nil), owner())
	copied := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: CAName}, copied); err != nil || string(copied.Data[corev1.TLSPrivateKeyKey]) != "renewed-key" {
		t.Errorf("copy = %v, err = %v, want the renewed CA", copied.Data, err)
	}
}

func TestTheCAOfTheTenantDoesNotTakeOverACertificateOfSomeoneElse(t *testing.T) {
	foreign := unstructuredOf(certificateGVK)
	foreign.SetNamespace(testSharedNamespace)
	foreign.SetName(SharedCAName(testTenant))
	foreign.Object["spec"] = map[string]any{"secretName": "theirs"}
	c := newClient(t, foreign)
	outcome := NewComponent(c).Ensure(context.Background(), inputs(nil), owner())
	if outcome.Phase != PhaseDegraded || !strings.Contains(outcome.Message, "not managed by the operator") {
		t.Errorf("outcome = %+v, want degraded", outcome)
	}
	if name, _, _ := unstructured.NestedString(get(t, c, certificateGVK, testSharedNamespace, SharedCAName(testTenant)).Object, "spec", "secretName"); name != "theirs" {
		t.Error("the Certificate of someone else was changed")
	}
}

func TestTheGatewayRefusesAnInvalidConfiguration(t *testing.T) {
	for name, change := range map[string]func(*Inputs){
		"no class":            func(in *Inputs) { in.ClassName = "" },
		"no issuer":           func(in *Inputs) { in.IssuerName = "" },
		"no shared Gateway":   func(in *Inputs) { in.Shared.Name = "" },
		"the tenant listener": func(in *Inputs) { in.Services = map[string]bool{TenantListenerName: true} },
	} {
		t.Run(name, func(t *testing.T) {
			in := inputs(nil)
			change(&in)
			if outcome := NewComponent(newClient(t, readyCA()...)).Ensure(context.Background(), in, owner()); outcome.Phase != PhaseDegraded || outcome.Reason != ReasonInvalidConfiguration {
				t.Errorf("outcome = %+v, want an invalid configuration", outcome)
			}
		})
	}
}

func TestDeleteRemovesWhatTheGarbageCollectorLeaves(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewComponent(c)
	services := map[string]bool{"guacamole": true}
	component.Ensure(context.Background(), inputs(services), owner())
	// cert-manager writes the Secrets of the listener certificates without an owner.
	for _, name := range []string{TenantListenerName, "guacamole"} {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: ListenerSecretName(name), Annotations: map[string]string{"cert-manager.io/certificate-name": ListenerCertificateName(name)}}}
		if err := c.Create(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
	}

	if err := component.Delete(context.Background(), testTenant, services, owner()); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		object          client.Object
		namespace, name string
	}{
		{unstructuredOf(gatewayGVK), testTenant, GatewayName},
		{unstructuredOf(issuerGVK), testTenant, CAName},
		{&corev1.Secret{}, testTenant, CAName},
		{unstructuredOf(certificateGVK), testTenant, ListenerCertificateName(TenantListenerName)},
		{&corev1.Secret{}, testTenant, ListenerSecretName(TenantListenerName)},
		{unstructuredOf(certificateGVK), testTenant, ListenerCertificateName("guacamole")},
		{&corev1.Secret{}, testTenant, ListenerSecretName("guacamole")},
		{unstructuredOf(certificateGVK), testSharedNamespace, SharedCAName(testTenant)},
		{&corev1.Secret{}, testSharedNamespace, SharedCAName(testTenant)},
	} {
		if !absent(t, c, check.object, check.namespace, check.name) {
			t.Errorf("%T %s/%s stays", check.object, check.namespace, check.name)
		}
	}
}
