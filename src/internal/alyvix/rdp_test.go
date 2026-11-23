// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package alyvix

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	testTenant          = "neteye-tenant-2bh46bfv"
	testSharedNamespace = "neteye-tenant-shared"
	testIssuer          = "neteye-internal-issuer"
)

var gatewayGVK = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}

var (
	certificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	httpRouteGVK   = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
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

func inputs(gateway bool) RDPInputs {
	in := RDPInputs{Namespace: testTenant, GuacamoleImage: "guacamole-image", GuacdImage: "guacd-image", Ports: []int32{3389}, CABundle: resources.CABundleRef{Name: "neteye-ca-bundle", Key: "ca-bundle.pem"}}
	if gateway {
		in.Gateway = &GatewayInputs{ClassName: "cilium", IssuerName: testIssuer, Shared: resources.GatewayRef{Namespace: testSharedNamespace, Name: "neteye"}}
	}
	return in
}

func getObject(t *testing.T, c client.Client, name string, object client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, object); err != nil {
		t.Fatalf("get %T %s: %v", object, name, err)
	}
}

func unstructuredObject(t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		t.Fatalf("get %s %s/%s: %v", gvk.Kind, namespace, name, err)
	}
	return object
}

func jsonAuthKey(t *testing.T, c client.Client) string {
	t.Helper()
	secret := &corev1.Secret{}
	getObject(t, c, JSONAuthSecretName, secret)
	return string(secret.Data[JSONAuthSecretKey])
}

func findEnv(values []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range values {
		if values[i].Name == name {
			return &values[i]
		}
	}
	return nil
}

func markDeploymentReady(t *testing.T, c client.Client, name string) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	getObject(t, c, name, deployment)
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.Replicas = *deployment.Spec.Replicas
	deployment.Status.ReadyReplicas = *deployment.Spec.Replicas
	deployment.Status.UpdatedReplicas = *deployment.Spec.Replicas
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}

// readyCA is the intermediate CA of the tenant in the shared namespace, as
// cert-manager leaves it when the Certificate is ready: the Certificate and
// its Secret.
func readyCA() []client.Object {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK)
	certificate.SetNamespace(testSharedNamespace)
	certificate.SetName(sharedCAName(testTenant))
	certificate.SetUID("shared-ca-certificate-uid")
	certificate.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "neteye-operator"})
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	secret := &corev1.Secret{
		// cert-manager names the Certificate that wrote the Secret.
		ObjectMeta: metav1.ObjectMeta{Namespace: testSharedNamespace, Name: sharedCAName(testTenant), UID: "shared-ca-secret-uid", Annotations: map[string]string{"cert-manager.io/certificate-name": sharedCAName(testTenant)}},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("ca-chain"), corev1.TLSPrivateKeyKey: []byte("ca-key"), "ca.crt": []byte("root")},
	}
	return []client.Object{certificate, secret, sharedGatewayWithAddress()}
}

// sharedGatewayWithAddress is the shared Gateway of the NetEye resource, with
// the address that the load balancer pool gave it.
func sharedGatewayWithAddress() *unstructured.Unstructured {
	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(gatewayGVK)
	gateway.SetNamespace(testSharedNamespace)
	gateway.SetName("neteye")
	gateway.Object["status"] = map[string]any{"addresses": []any{map[string]any{"type": "IPAddress", "value": "192.0.2.128"}}}
	return gateway
}

// markCertificateReady sets the Ready condition of a Certificate that the
// component created.
func markCertificateReady(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	certificate := unstructuredObject(t, c, certificateGVK, namespace, name)
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	if err := c.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
}

// setListenerStatus sets the generation of the Gateway of the tenant, and the
// Programmed condition of its RDP listener for an observed generation.
func setListenerStatus(t *testing.T, c client.Client, generation, observed int64, programmed string) {
	t.Helper()
	gateway := unstructuredObject(t, c, gatewayGVK, testTenant, GatewayName)
	gateway.SetGeneration(generation)
	gateway.Object["status"] = map[string]any{"listeners": []any{
		map[string]any{"name": "http", "conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": generation}}},
		map[string]any{"name": GatewayListenerName, "conditions": []any{map[string]any{"type": "Programmed", "status": programmed, "observedGeneration": observed}}},
	}}
	if err := c.Update(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
}

func markRouteAccepted(t *testing.T, c client.Client) {
	t.Helper()
	route := unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)
	parent := map[string]any{
		"parentRef": map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": testTenant, "name": GatewayName, "sectionName": GatewayListenerName},
		"conditions": []any{
			map[string]any{"type": "Accepted", "status": "True", "observedGeneration": route.GetGeneration()},
			map[string]any{"type": "ResolvedRefs", "status": "True", "observedGeneration": route.GetGeneration()},
		},
	}
	if err := unstructured.SetNestedSlice(route.Object, []any{parent}, "status", "parents"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
}

func TestRDPAccessDeploysGuacamoleAndGuacdInTheTenantNamespace(t *testing.T) {
	c := newClient(t, readyCA()...)
	outcome := NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonDeploymentNotAvailable {
		t.Fatalf("outcome = %+v, want progressing until the Deployments are ready", outcome)
	}

	guacd := &appsv1.Deployment{}
	getObject(t, c, GuacdName, guacd)
	container := guacd.Spec.Template.Spec.Containers[0]
	if container.Image != "guacd-image" || container.Ports[0].ContainerPort != GuacdPort {
		t.Errorf("guacd container = %+v", container)
	}
	if owners := guacd.GetOwnerReferences(); len(owners) != 1 || owners[0].UID != "tenant-uid" {
		t.Errorf("guacd owners = %#v, want the tenant", owners)
	}

	guacamole := &appsv1.Deployment{}
	getObject(t, c, GuacamoleName, guacamole)
	pod := guacamole.Spec.Template.Spec
	container = pod.Containers[0]
	if container.Image != "guacamole-image" {
		t.Errorf("guacamole image = %q", container.Image)
	}
	if env := findEnv(container.Env, "GUACD_HOSTNAME"); env == nil || env.Value != GuacdName {
		t.Errorf("GUACD_HOSTNAME = %+v, want the guacd Service", env)
	}
	key := findEnv(container.Env, "JSON_SECRET_KEY")
	if key == nil || key.Value != "" || key.ValueFrom == nil || key.ValueFrom.SecretKeyRef == nil || key.ValueFrom.SecretKeyRef.Name != JSONAuthSecretName || key.ValueFrom.SecretKeyRef.Key != JSONAuthSecretKey {
		t.Errorf("JSON_SECRET_KEY = %+v, want a reference to the Secret", key)
	}
	if env := findEnv(container.Env, "BAN_ENABLED"); env == nil || env.Value != "false" {
		t.Errorf("BAN_ENABLED = %+v, want the ban extension disabled", env)
	}
	// The Service links of a Service named guacd would overwrite GUACD_PORT.
	if pod.EnableServiceLinks == nil || *pod.EnableServiceLinks || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Errorf("pod spec enableServiceLinks = %v, automountServiceAccountToken = %v, want both false", pod.EnableServiceLinks, pod.AutomountServiceAccountToken)
	}
	if context := container.SecurityContext; context == nil || context.RunAsNonRoot == nil || !*context.RunAsNonRoot || context.RunAsUser == nil || *context.RunAsUser != guacamoleUserID || context.AllowPrivilegeEscalation == nil || *context.AllowPrivilegeEscalation {
		t.Errorf("guacamole security context = %+v", context)
	}

	for name, port := range map[string]int32{GuacdName: GuacdPort, GuacamoleName: GuacamolePort} {
		service := &corev1.Service{}
		getObject(t, c, name, service)
		if service.Spec.Selector["app"] != name || service.Spec.Ports[0].Port != port {
			t.Errorf("Service %s = %+v", name, service.Spec)
		}
	}

	route := unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)
	hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if want := []string{"guacamole.tenant-2bh46bfv.neteyelocal"}; !reflect.DeepEqual(hostnames, want) {
		t.Errorf("route hostnames = %v, want %v", hostnames, want)
	}
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if want := []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": testTenant, "name": GatewayName, "sectionName": GatewayListenerName}}; !reflect.DeepEqual(parents, want) {
		t.Errorf("route parents = %v, want the RDP listener of the Gateway of the tenant", parents)
	}
}

func TestRDPGuacdTrustsTheCABundleOfTheTenant(t *testing.T) {
	c := newClient(t)
	in := inputs(true)
	in.CABundle = resources.CABundleRef{Name: "custom-ca", Key: "cas.pem"}
	NewRDPComponent(c).Ensure(context.Background(), in, owner())

	guacd := &appsv1.Deployment{}
	getObject(t, c, GuacdName, guacd)
	pod := guacd.Spec.Template.Spec
	container := pod.Containers[0]
	// FreeRDP reads SSL_CERT_FILE in addition to the hashed system CA folder.
	if env := findEnv(container.Env, "SSL_CERT_FILE"); env == nil || env.Value != userCAMountPath+"/"+resources.UserCABundleFile {
		t.Errorf("SSL_CERT_FILE = %+v, want the mounted CA bundle", env)
	}
	if want := []corev1.VolumeMount{{Name: resources.UserCABundleVolumeName, MountPath: userCAMountPath, ReadOnly: true}}; !reflect.DeepEqual(container.VolumeMounts, want) {
		t.Errorf("guacd volume mounts = %+v, want %+v", container.VolumeMounts, want)
	}
	if want := []corev1.Volume{{Name: resources.UserCABundleVolumeName, VolumeSource: resources.UserCABundleVolumeSource(in.CABundle)}}; !reflect.DeepEqual(pod.Volumes, want) {
		t.Errorf("guacd volumes = %+v, want %+v", pod.Volumes, want)
	}
	source := pod.Volumes[0].ConfigMap
	if source.Name != "custom-ca" || source.Optional == nil || !*source.Optional || source.Items[0].Key != "cas.pem" {
		t.Errorf("guacd CA volume = %+v, want the optional ConfigMap key of the tenant", source)
	}
	// The pod is not rolled when the bundle changes: FreeRDP loads it for
	// every connection, and a roll would close the open sessions.
	if _, ok := guacd.Spec.Template.Annotations[resources.UserCABundleVersionAnnotation]; ok {
		t.Errorf("guacd annotations = %v, want no CA bundle version", guacd.Spec.Template.Annotations)
	}

	// The web application opens no TLS connection to the Alyvix nodes.
	guacamole := &appsv1.Deployment{}
	getObject(t, c, GuacamoleName, guacamole)
	if volumes := guacamole.Spec.Template.Spec.Volumes; len(volumes) != 0 {
		t.Errorf("guacamole volumes = %+v, want none", volumes)
	}
}

func TestRDPRouteExposesOnlyThePathsOfTheModuleAndTheBrowser(t *testing.T) {
	c := newClient(t, readyCA()...)
	NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())

	route := unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want one rule", rules)
	}
	rule := rules[0].(map[string]any)
	// Every pair, in order: an extra match for a path must make the test fail.
	got := []string{}
	for _, raw := range rule["matches"].([]any) {
		match := raw.(map[string]any)
		path := match["path"].(map[string]any)
		if path["type"] != "Exact" || len(match) != 2 {
			t.Errorf("match %v, want only an exact path and a method", match)
		}
		got = append(got, match["method"].(string)+" "+path["value"].(string))
	}
	want := []string{
		"POST /guacamole/api/tokens",
		"DELETE /guacamole/api/session",
		"GET /guacamole/websocket-tunnel",
		"GET /guacamole/guacamole-common-js/all.min.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("route matches = %v, want %v", got, want)
	}
	if backends := rule["backendRefs"].([]any); len(backends) != 1 || backends[0].(map[string]any)["name"] != GuacamoleName {
		t.Errorf("route backends = %v, want the guacamole Service", backends)
	}
}

func TestRDPAccessGeneratesTheJSONAuthenticationKeyOnce(t *testing.T) {
	c := newClient(t)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())

	key := jsonAuthKey(t, c)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(key) {
		t.Fatalf("key %q, want 32 hexadecimal characters", key)
	}
	secret := &corev1.Secret{}
	getObject(t, c, JSONAuthSecretName, secret)
	if owners := secret.GetOwnerReferences(); len(owners) != 1 || owners[0].UID != "tenant-uid" {
		t.Errorf("Secret owners = %#v, want the tenant", owners)
	}
	guacamole := &appsv1.Deployment{}
	getObject(t, c, GuacamoleName, guacamole)
	if got := guacamole.Spec.Template.Annotations[jsonAuthKeyVersionAnnotation]; got == "" || got != secret.ResourceVersion {
		t.Errorf("pod template key version = %q, want the Secret resourceVersion %q", got, secret.ResourceVersion)
	}

	component.Ensure(context.Background(), inputs(true), owner())
	if again := jsonAuthKey(t, c); again != key {
		t.Errorf("key changed from %q to %q", key, again)
	}

	other := newClient(t)
	NewRDPComponent(other).Ensure(context.Background(), inputs(true), owner())
	if jsonAuthKey(t, other) == key {
		t.Error("two tenants got the same key")
	}
}

func TestRDPAccessKeepsAKeyOfTheUserAndRefusesAnInvalidOne(t *testing.T) {
	userKey := "0123456789ABCDEF0123456789abcdef"
	c := newClient(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: JSONAuthSecretName}, Data: map[string][]byte{JSONAuthSecretKey: []byte(userKey)}})
	NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())
	if got := jsonAuthKey(t, c); got != userKey {
		t.Errorf("key = %q, want the key of the user", got)
	}

	invalid := newClient(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: JSONAuthSecretName}, Data: map[string][]byte{JSONAuthSecretKey: []byte("short")}})
	outcome := NewRDPComponent(invalid).Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseDegraded || outcome.Reason != ReasonInvalidSecret || outcome.Err == nil {
		t.Fatalf("outcome = %+v, want degraded with an invalid Secret", outcome)
	}
	if strings.Contains(outcome.Message, "short") {
		t.Errorf("message %q shows the value of the Secret", outcome.Message)
	}
	if err := invalid.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: GuacamoleName}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("guacamole Deployment with an invalid key: err = %v, want not found", err)
	}
}

func TestRDPAccessNeverReplacesAKeyThatTheCacheHasNotSeen(t *testing.T) {
	existing := "0123456789abcdef0123456789abcdef"
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: JSONAuthSecretName}, Data: map[string][]byte{JSONAuthSecretKey: []byte(existing)}}).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
			if _, secret := object.(*corev1.Secret); secret {
				return apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
			}
			return c.Get(ctx, key, object, opts...)
		}}).Build()

	outcome := NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonSecretNotCached {
		t.Fatalf("outcome = %+v, want waiting for the cache", outcome)
	}
	list := &corev1.SecretList{}
	if err := c.List(context.Background(), list); err != nil || len(list.Items) != 1 || string(list.Items[0].Data[JSONAuthSecretKey]) != existing {
		t.Errorf("Secrets = %v, err = %v, want the existing key unchanged", list.Items, err)
	}
}

func TestRDPNetworkPoliciesOpenOnlyTheTrafficOfTheAccess(t *testing.T) {
	c := newClient(t)
	in := inputs(true)
	in.Ports = []int32{3390, 3389, 3389}
	NewRDPComponent(c).Ensure(context.Background(), in, owner())

	spec := func(name string) map[string]any {
		policy := unstructuredObject(t, c, resources.NetworkPolicyGVK(), testTenant, name)
		value, _, _ := unstructured.NestedMap(policy.Object, "spec")
		return value
	}
	app := func(name string) map[string]any { return map[string]any{"matchLabels": map[string]any{"app": name}} }
	tcp := func(ports ...int64) []any { return resources.NetworkPolicyPorts("TCP", ports...) }
	want := map[string]map[string]any{
		GuacamoleIngressPolicyName: {"podSelector": app(GuacamoleName), "policyTypes": []any{"Ingress"}, "ingress": []any{map[string]any{"ports": tcp(8080)}}},
		GuacamoleEgressPolicyName: {"podSelector": app(GuacamoleName), "policyTypes": []any{"Egress"}, "egress": []any{
			resources.DNSEgressRule(),
			map[string]any{"to": []any{map[string]any{"podSelector": app(GuacdName)}}, "ports": tcp(4822)},
		}},
		GuacdIngressPolicyName: {"podSelector": app(GuacdName), "policyTypes": []any{"Ingress"}, "ingress": []any{
			map[string]any{"from": []any{map[string]any{"podSelector": app(GuacamoleName)}}, "ports": tcp(4822)},
		}},
		GuacdEgressPolicyName: {"podSelector": app(GuacdName), "policyTypes": []any{"Egress"}, "egress": []any{
			resources.DNSEgressRule(),
			map[string]any{"ports": tcp(3389, 3390)},
		}},
	}
	for name, expected := range want {
		if got := spec(name); !reflect.DeepEqual(got, expected) {
			t.Errorf("policy %s = %v, want %v", name, got, expected)
		}
	}
}

func TestRDPAccessIsReadyWhenTheWorkloadsTheCertificateAndTheRouteAreReady(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	markDeploymentReady(t, c, GuacdName)
	markDeploymentReady(t, c, GuacamoleName)

	outcome := component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome = %+v, want waiting for the certificate of the listener", outcome)
	}
	markCertificateReady(t, c, testTenant, TLSCertificateName)
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for the Gateway to program the listener", outcome)
	}
	// The route can be accepted before the listener is programmed.
	markRouteAccepted(t, c)
	setListenerStatus(t, c, 1, 0, "False")
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for a listener that is not programmed", outcome)
	}
	setListenerStatus(t, c, 2, 1, "True")
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for a status of the current Gateway generation", outcome)
	}
	setListenerStatus(t, c, 2, 2, "True")
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseReady {
		t.Fatalf("outcome = %+v, want ready", outcome)
	}
	// The route is checked too.
	route := unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)
	unstructured.RemoveNestedField(route.Object, "status")
	if err := c.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonRouteNotReady {
		t.Fatalf("outcome = %+v, want waiting for the route", outcome)
	}
	markRouteAccepted(t, c)
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseReady || !strings.Contains(outcome.Message, "guacamole.tenant-2bh46bfv.neteyelocal") {
		t.Fatalf("outcome = %+v, want ready on the hostname of the tenant", outcome)
	}
}

func TestRDPAccessWithAnUnmanagedGatewayHasNoRoute(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)

	component.Ensure(context.Background(), inputs(false), owner())
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: HTTPRouteName}, route); !apierrors.IsNotFound(err) {
		t.Errorf("route err = %v, want not found", err)
	}
	markDeploymentReady(t, c, GuacdName)
	markDeploymentReady(t, c, GuacamoleName)
	outcome := component.Ensure(context.Background(), inputs(false), owner())
	if outcome.Phase != PhaseReady {
		t.Errorf("outcome = %+v, want ready without a route", outcome)
	}
	// The hostname is not served by the operator: the message does not claim it.
	if strings.Contains(outcome.Message, "ready on") || !strings.Contains(outcome.Message, "yourself") {
		t.Errorf("message %q, want the workloads ready and the routing left to the administrator", outcome.Message)
	}
}

func TestRDPAccessRefusesAnInvalidConfiguration(t *testing.T) {
	for name, change := range map[string]func(*RDPInputs){
		"no guacamole image": func(in *RDPInputs) { in.GuacamoleImage = "" },
		"no guacd image":     func(in *RDPInputs) { in.GuacdImage = "" },
		"no port":            func(in *RDPInputs) { in.Ports = nil },
		"no CA bundle name":  func(in *RDPInputs) { in.CABundle.Name = "" },
		"no CA bundle key":   func(in *RDPInputs) { in.CABundle.Key = "" },
		"invalid port":       func(in *RDPInputs) { in.Ports = []int32{0} },
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t)
			in := inputs(true)
			change(&in)
			outcome := NewRDPComponent(c).Ensure(context.Background(), in, owner())
			if outcome.Phase != PhaseDegraded || outcome.Reason != ReasonInvalidConfiguration || outcome.Err == nil {
				t.Errorf("outcome = %+v, want an invalid configuration", outcome)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: JSONAuthSecretName}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Errorf("Secret err = %v, want nothing created", err)
			}
		})
	}
}

func TestRDPDeleteKeepsTheKeyAndObjectsOfOthers(t *testing.T) {
	foreign := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: GuacdName}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 1}}}}
	c := newClient(t, foreign)
	component := NewRDPComponent(c)
	if outcome := component.Ensure(context.Background(), inputs(true), owner()); outcome.Phase != PhaseDegraded {
		t.Fatalf("outcome = %+v, want degraded: the guacd Service belongs to someone else", outcome)
	}
	key := jsonAuthKey(t, c)

	if err := component.Delete(context.Background(), testTenant, owner()); err != nil {
		t.Fatal(err)
	}
	for _, resource := range allInventory() {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(resource.gvk)
		err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: resource.name}, object)
		if resource.gvk.Kind == "Service" && resource.name == GuacdName {
			if err != nil {
				t.Errorf("the Service of someone else was deleted: %v", err)
			}
			continue
		}
		if !apierrors.IsNotFound(err) {
			t.Errorf("%s %s err = %v, want deleted", resource.gvk.Kind, resource.name, err)
		}
	}
	if got := jsonAuthKey(t, c); got != key {
		t.Errorf("key = %q after Delete, want %q kept", got, key)
	}
}

func TestRDPDeleteOnlyDeletesTheObjectThatPassedTheOwnershipCheck(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	preconditions := map[string]types.UID{}
	// The fake client assigns no UID, so every object gets its own here: the
	// test then tells apart an empty, a reused and the right UID.
	created := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyCA()...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, inner client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			created++
			object.SetUID(types.UID(fmt.Sprintf("uid-%d", created)))
			return inner.Create(ctx, object, opts...)
		},
		Delete: func(ctx context.Context, inner client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			options := &client.DeleteOptions{}
			options.ApplyOptions(opts)
			if options.Preconditions != nil && options.Preconditions.UID != nil {
				preconditions[object.GetObjectKind().GroupVersionKind().Kind+"/"+object.GetName()] = *options.Preconditions.UID
			}
			return inner.Delete(ctx, object, opts...)
		},
	}).Build()
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	uids := map[string]types.UID{}
	seen := map[types.UID]bool{}
	for _, resource := range allInventory() {
		uid := unstructuredObject(t, c, resource.gvk, testTenant, resource.name).GetUID()
		if uid == "" || seen[uid] {
			t.Fatalf("%s %s has the UID %q, want a distinct, nonempty one", resource.gvk.Kind, resource.name, uid)
		}
		seen[uid] = true
		uids[resource.gvk.Kind+"/"+resource.name] = uid
	}
	// The Secret with the key of the CA is typed: its kind is empty in the client.
	caSecret := &corev1.Secret{}
	getObject(t, c, CASecretName, caSecret)
	uids["/"+CASecretName] = caSecret.UID
	sharedCASecret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testSharedNamespace, Name: sharedCAName(testTenant)}, sharedCASecret); err != nil {
		t.Fatal(err)
	}
	uids["/"+sharedCAName(testTenant)] = sharedCASecret.UID
	// The two Certificates are deleted after the label check, so with a precondition too.
	uids["Certificate/"+TLSCertificateName] = unstructuredObject(t, c, certificateGVK, testTenant, TLSCertificateName).GetUID()
	uids["Certificate/"+sharedCAName(testTenant)] = unstructuredObject(t, c, certificateGVK, testSharedNamespace, sharedCAName(testTenant)).GetUID()
	for name, uid := range uids {
		if uid == "" {
			t.Fatalf("%s has an empty UID", name)
		}
	}

	if err := component.Delete(context.Background(), testTenant, owner()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preconditions, uids) {
		t.Errorf("delete preconditions = %v, want the UID of each object read = %v", preconditions, uids)
	}
}

// A route that cannot be deleted, for example because an admission policy
// refuses it, must not keep the workloads that serve it.
func TestRDPDeleteGoesOnWhenAnObjectCannotBeDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyCA()...).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, inner client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			if object.GetObjectKind().GroupVersionKind().Kind == "HTTPRoute" {
				return errors.New("denied by an admission policy")
			}
			return inner.Delete(ctx, object, opts...)
		},
	}).Build()
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())

	err := component.Delete(context.Background(), testTenant, owner())
	if err == nil || !strings.Contains(err.Error(), "denied by an admission policy") || !strings.Contains(err.Error(), HTTPRouteName) {
		t.Errorf("err = %v, want the failure of the route", err)
	}
	unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)
	for _, resource := range allInventory() {
		if resource.gvk.Kind == "HTTPRoute" {
			continue
		}
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(resource.gvk)
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: resource.name}, object); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s err = %v, want deleted although the route was not", resource.gvk.Kind, resource.name, err)
		}
	}
}

func allInventory() []managedResource {
	return append(gatewayInventory(), workloadInventory()...)
}

func TestRDPHostnameIsTheServiceOnTheTenant(t *testing.T) {
	if got, want := Hostname("neteye-tenant-2bh46bfv"), "guacamole.tenant-2bh46bfv.neteyelocal"; got != want {
		t.Errorf("hostname = %q, want %q", got, want)
	}
}

func TestRDPAccessWaitsForTheIntermediateCAOfTheTenant(t *testing.T) {
	c := newClient(t)
	outcome := NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonDeploymentNotAvailable {
		t.Fatalf("outcome = %+v, want the workloads first", outcome)
	}
	markDeploymentReady(t, c, GuacdName)
	markDeploymentReady(t, c, GuacamoleName)
	outcome = NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome = %+v, want waiting for the CA of the tenant", outcome)
	}

	// The internal Issuer signs the intermediate CA, in the shared namespace.
	ca := unstructuredObject(t, c, certificateGVK, testSharedNamespace, sharedCAName(testTenant))
	if isCA, _, _ := unstructured.NestedBool(ca.Object, "spec", "isCA"); !isCA {
		t.Errorf("CA Certificate spec = %v, want isCA", ca.Object["spec"])
	}
	if issuer, _, _ := unstructured.NestedString(ca.Object, "spec", "issuerRef", "name"); issuer != testIssuer {
		t.Errorf("CA Certificate issuer = %q, want %q", issuer, testIssuer)
	}
	// Nothing of the Gateway exists before the CA: it could not serve.
	for _, resource := range gatewayInventory() {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(resource.gvk)
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: resource.name}, object); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s err = %v, want not created", resource.gvk.Kind, resource.name, err)
		}
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: CASecretName}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("CA Secret err = %v, want not created", err)
	}
}

func TestRDPGatewayOfTheTenantSignsWithTheIntermediateCAOfTheTenant(t *testing.T) {
	c := newClient(t, append(readyCA(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: testSharedNamespace, Name: "neteye-root-ca"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: []byte("root-cert"), corev1.TLSPrivateKeyKey: []byte("root-key")},
	})...)
	NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())

	// The tenant namespace has the key of the intermediate CA, and only it, with
	// the certificate of the root CA.
	secret := &corev1.Secret{}
	getObject(t, c, CASecretName, secret)
	want := map[string][]byte{corev1.TLSCertKey: []byte("ca-chain"), corev1.TLSPrivateKeyKey: []byte("ca-key"), "ca.crt": []byte("root")}
	if secret.Type != corev1.SecretTypeTLS || !reflect.DeepEqual(secret.Data, want) {
		t.Errorf("CA Secret = %v %v, want only the certificate and the key of the intermediate CA", secret.Type, secret.Data)
	}
	if owners := secret.GetOwnerReferences(); len(owners) != 1 || owners[0].UID != "tenant-uid" {
		t.Errorf("CA Secret owners = %#v, want the tenant", owners)
	}
	secrets := &corev1.SecretList{}
	if err := c.List(context.Background(), secrets, client.InNamespace(testTenant)); err != nil {
		t.Fatal(err)
	}
	for _, other := range secrets.Items {
		if _, found := other.Data[corev1.TLSPrivateKeyKey]; found && other.Name != CASecretName {
			t.Errorf("Secret %s holds a private key", other.Name)
		}
		if string(other.Data[corev1.TLSPrivateKeyKey]) == "root-key" {
			t.Errorf("Secret %s holds the key of the root CA", other.Name)
		}
	}

	issuer := unstructuredObject(t, c, schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}, testTenant, CAIssuerName)
	if name, _, _ := unstructured.NestedString(issuer.Object, "spec", "ca", "secretName"); name != CASecretName {
		t.Errorf("Issuer spec = %v, want the CA Secret of the tenant", issuer.Object["spec"])
	}
	certificate := unstructuredObject(t, c, certificateGVK, testTenant, TLSCertificateName)
	dnsNames, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	issuerName, _, _ := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
	if !reflect.DeepEqual(dnsNames, []string{"guacamole.tenant-2bh46bfv.neteyelocal"}) || issuerName != CAIssuerName {
		t.Errorf("Certificate spec = %v, want the hostname of the tenant, signed by its Issuer", certificate.Object["spec"])
	}

	gateway := unstructuredObject(t, c, gatewayGVK, testTenant, GatewayName)
	if class, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName"); class != "cilium" {
		t.Errorf("gatewayClassName = %q", class)
	}
	if _, found, _ := unstructured.NestedMap(gateway.Object, "spec", "infrastructure"); found {
		t.Error("the Gateway of the tenant has the annotations of the shared Gateway, which can ask for its address")
	}
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	// Only the HTTPS listener: nothing redirects HTTP, so port 80 stays closed.
	if len(listeners) != 1 {
		t.Errorf("listeners = %v, want only the HTTPS listener %s", listeners, GatewayListenerName)
	}
	var rdp map[string]any
	for _, listener := range listeners {
		if listener.(map[string]any)["name"] == GatewayListenerName {
			rdp = listener.(map[string]any)
		}
	}
	if rdp == nil {
		t.Fatalf("listeners = %v, want the listener %s", listeners, GatewayListenerName)
	}
	from, _, _ := unstructured.NestedString(rdp, "allowedRoutes", "namespaces", "from")
	secretName, _, _ := unstructured.NestedString(rdp["tls"].(map[string]any)["certificateRefs"].([]any)[0].(map[string]any), "name")
	if rdp["hostname"] != "guacamole.tenant-2bh46bfv.neteyelocal" || from != "Same" || secretName != TLSSecretName {
		t.Errorf("listener = %v, want the hostname of the tenant, routes of its namespace only, and its certificate", rdp)
	}
	if owners := gateway.GetOwnerReferences(); len(owners) != 1 || owners[0].UID != "tenant-uid" {
		t.Errorf("Gateway owners = %#v, want the tenant", owners)
	}
}

func TestRDPAccessFollowsTheRenewalOfTheIntermediateCA(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())

	renewed := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testSharedNamespace, Name: sharedCAName(testTenant)}, renewed); err != nil {
		t.Fatal(err)
	}
	renewed.Data[corev1.TLSCertKey] = []byte("renewed-chain")
	renewed.Data[corev1.TLSPrivateKeyKey] = []byte("renewed-key")
	if err := c.Update(context.Background(), renewed); err != nil {
		t.Fatal(err)
	}
	component.Ensure(context.Background(), inputs(true), owner())

	copied := &corev1.Secret{}
	getObject(t, c, CASecretName, copied)
	if string(copied.Data[corev1.TLSCertKey]) != "renewed-chain" || string(copied.Data[corev1.TLSPrivateKeyKey]) != "renewed-key" {
		t.Errorf("CA Secret of the tenant = %v, want the renewed CA", copied.Data)
	}
}

func TestRDPAccessRefusesAGatewayWithoutAClassOrAnIssuer(t *testing.T) {
	for name, change := range map[string]func(*GatewayInputs){
		"no class":          func(gateway *GatewayInputs) { gateway.ClassName = "" },
		"no issuer":         func(gateway *GatewayInputs) { gateway.IssuerName = "" },
		"no shared Gateway": func(gateway *GatewayInputs) { gateway.Shared.Name = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, readyCA()...)
			in := inputs(true)
			change(in.Gateway)
			outcome := NewRDPComponent(c).Ensure(context.Background(), in, owner())
			if outcome.Phase != PhaseDegraded || outcome.Reason != ReasonInvalidConfiguration {
				t.Errorf("outcome = %+v, want an invalid configuration", outcome)
			}
		})
	}
}

func TestRDPDeleteRemovesTheIntermediateCAOfTheTenantFromBothNamespaces(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	getObject(t, c, CASecretName, &corev1.Secret{})

	if err := component.Delete(context.Background(), testTenant, owner()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: CASecretName}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("CA Secret of the tenant err = %v, want deleted", err)
	}
	ca := &unstructured.Unstructured{}
	ca.SetGroupVersionKind(certificateGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testSharedNamespace, Name: sharedCAName(testTenant)}, ca); !apierrors.IsNotFound(err) {
		t.Errorf("CA Certificate err = %v, want deleted", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testSharedNamespace, Name: sharedCAName(testTenant)}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("CA Secret of the shared namespace err = %v, want deleted", err)
	}
}

func TestRDPAccessWithAnUnmanagedGatewayRemovesTheGatewayOfTheTenant(t *testing.T) {
	c := newClient(t, readyCA()...)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	unstructuredObject(t, c, gatewayGVK, testTenant, GatewayName)

	component.Ensure(context.Background(), inputs(false), owner())
	for _, resource := range gatewayInventory() {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(resource.gvk)
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: resource.name}, object); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s err = %v, want deleted", resource.gvk.Kind, resource.name, err)
		}
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: CASecretName}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("CA Secret err = %v, want deleted", err)
	}
}

// The shared Gateway can ask the load balancer pool for a fixed address. The
// Gateway of a tenant that came first would take that address.
func TestRDPGatewayOfTheTenantWaitsForTheAddressOfTheSharedGateway(t *testing.T) {
	objects := readyCA()
	shared := objects[len(objects)-1].(*unstructured.Unstructured)
	delete(shared.Object, "status")
	c := newClient(t, objects...)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	markDeploymentReady(t, c, GuacdName)
	markDeploymentReady(t, c, GuacamoleName)

	outcome := component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonSharedGatewayNotReady {
		t.Fatalf("outcome = %+v, want waiting for the address of the shared Gateway", outcome)
	}
	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(gatewayGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: GatewayName}, gateway); !apierrors.IsNotFound(err) {
		t.Errorf("Gateway of the tenant err = %v, want not created before the shared Gateway has its address", err)
	}

	live := unstructuredObject(t, c, gatewayGVK, testSharedNamespace, "neteye")
	live.Object["status"] = sharedGatewayWithAddress().Object["status"]
	if err := c.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	component.Ensure(context.Background(), inputs(true), owner())
	unstructuredObject(t, c, gatewayGVK, testTenant, GatewayName)
}

// The intermediate CA has no owner: a Certificate of someone else with its
// name must not be taken over.
func TestRDPAccessDoesNotTakeOverACertificateOfSomeoneElseAtTheNameOfTheCA(t *testing.T) {
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(certificateGVK)
	foreign.SetNamespace(testSharedNamespace)
	foreign.SetName(sharedCAName(testTenant))
	foreign.Object["spec"] = map[string]any{"secretName": "theirs", "issuerRef": map[string]any{"name": "their-issuer"}}
	c := newClient(t, foreign, sharedGatewayWithAddress())

	outcome := NewRDPComponent(c).Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseDegraded || !strings.Contains(outcome.Message, "not managed by the operator") {
		t.Errorf("outcome = %+v, want degraded: the Certificate belongs to someone else", outcome)
	}
	live := unstructuredObject(t, c, certificateGVK, testSharedNamespace, sharedCAName(testTenant))
	if secretName, _, _ := unstructured.NestedString(live.Object, "spec", "secretName"); secretName != "theirs" {
		t.Errorf("spec = %v, want the Certificate of someone else unchanged", live.Object["spec"])
	}
}

// Guacamole reads the key only when it starts: an administrator who edits the
// key in the Secret, without deleting it, must get a new pod too.
func TestRDPAccessRestartsGuacamoleWhenTheKeyIsEditedInPlace(t *testing.T) {
	c := newClient(t)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	version := func() string {
		guacamole := &appsv1.Deployment{}
		getObject(t, c, GuacamoleName, guacamole)
		return guacamole.Spec.Template.Annotations[jsonAuthKeyVersionAnnotation]
	}
	before := version()

	secret := &corev1.Secret{}
	getObject(t, c, JSONAuthSecretName, secret)
	secret.Data[JSONAuthSecretKey] = []byte("0123456789abcdef0123456789abcdef")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	component.Ensure(context.Background(), inputs(true), owner())
	getObject(t, c, JSONAuthSecretName, secret)
	if got := version(); got == before || got != secret.ResourceVersion {
		t.Errorf("pod template key version = %q after the edit (before %q), want the new resourceVersion %q", got, before, secret.ResourceVersion)
	}
}
