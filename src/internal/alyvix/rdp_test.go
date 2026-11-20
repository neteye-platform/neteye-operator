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

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	testTenant          = "neteye-tenant-2bh46bfv"
	testSharedNamespace = "neteye-tenant-shared"
)

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
		in.Gateway = &resources.GatewayRef{Namespace: testSharedNamespace, Name: "neteye"}
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
	deployment.Status.ReadyReplicas = *deployment.Spec.Replicas
	deployment.Status.UpdatedReplicas = *deployment.Spec.Replicas
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}

func readyCertificate() *unstructured.Unstructured {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK)
	certificate.SetNamespace(testSharedNamespace)
	certificate.SetName(TLSCertificateName)
	certificate.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	return certificate
}

// sharedGateway is the shared Gateway with the status of the RDP listener.
func sharedGateway(generation, observed int64, programmed string) *unstructured.Unstructured {
	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"})
	gateway.SetNamespace(testSharedNamespace)
	gateway.SetName("neteye")
	gateway.SetGeneration(generation)
	gateway.Object["status"] = map[string]any{"listeners": []any{
		map[string]any{"name": "keycloak", "conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": generation}}},
		map[string]any{"name": GatewayListenerName, "conditions": []any{map[string]any{"type": "Programmed", "status": programmed, "observedGeneration": observed}}},
	}}
	return gateway
}

// setListenerProgrammed sets the Gateway generation to 2 and the Programmed
// condition of the RDP listener.
func setListenerProgrammed(t *testing.T, c client.Client, observed int64, status string) {
	t.Helper()
	gateway := unstructuredObject(t, c, schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}, testSharedNamespace, "neteye")
	updated := sharedGateway(2, observed, status)
	gateway.SetGeneration(2)
	gateway.Object["status"] = updated.Object["status"]
	if err := c.Update(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
}

func markRouteAccepted(t *testing.T, c client.Client) {
	t.Helper()
	route := unstructuredObject(t, c, httpRouteGVK, testTenant, HTTPRouteName)
	parent := map[string]any{
		"parentRef": map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": testSharedNamespace, "name": "neteye", "sectionName": GatewayListenerName},
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
	c := newClient(t)
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
	if want := []string{testTenant + ".guacamole.neteyelocal"}; !reflect.DeepEqual(hostnames, want) {
		t.Errorf("route hostnames = %v, want %v", hostnames, want)
	}
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if want := []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": testSharedNamespace, "name": "neteye", "sectionName": GatewayListenerName}}; !reflect.DeepEqual(parents, want) {
		t.Errorf("route parents = %v, want the RDP listener of the shared Gateway", parents)
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
	c := newClient(t)
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
	c := newClient(t)
	component := NewRDPComponent(c)
	component.Ensure(context.Background(), inputs(true), owner())
	markDeploymentReady(t, c, GuacdName)
	markDeploymentReady(t, c, GuacamoleName)

	outcome := component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome = %+v, want waiting for the certificate of the listener", outcome)
	}
	if err := c.Create(context.Background(), readyCertificate()); err != nil {
		t.Fatal(err)
	}
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for the Gateway to program the listener", outcome)
	}
	// The route can be accepted before the listener is programmed.
	markRouteAccepted(t, c)
	gateway := sharedGateway(1, 0, "False")
	if err := c.Create(context.Background(), gateway); err != nil {
		t.Fatal(err)
	}
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for a listener that is not programmed", outcome)
	}
	setListenerProgrammed(t, c, 1, "True")
	outcome = component.Ensure(context.Background(), inputs(true), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonListenerNotProgrammed {
		t.Fatalf("outcome = %+v, want waiting for a status of the current Gateway generation", outcome)
	}
	setListenerProgrammed(t, c, 2, "True")
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
	if outcome.Phase != PhaseReady || !strings.Contains(outcome.Message, testTenant+".guacamole.neteyelocal") {
		t.Fatalf("outcome = %+v, want ready on the hostname of the tenant", outcome)
	}
}

func TestRDPAccessWithAnUnmanagedGatewayHasNoRoute(t *testing.T) {
	c := newClient(t)
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
	for _, resource := range resourceInventory() {
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
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
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
	for _, resource := range resourceInventory() {
		uid := unstructuredObject(t, c, resource.gvk, testTenant, resource.name).GetUID()
		if uid == "" || seen[uid] {
			t.Fatalf("%s %s has the UID %q, want a distinct, nonempty one", resource.gvk.Kind, resource.name, uid)
		}
		seen[uid] = true
		uids[resource.gvk.Kind+"/"+resource.name] = uid
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
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
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
	for _, resource := range resourceInventory() {
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

func TestRDPGatewayListenerAcceptsRoutesOfTenantNamespacesOnly(t *testing.T) {
	listener := GatewayListener()
	if listener.Hostname != "*.guacamole.neteyelocal" || listener.TLSSecretName != TLSSecretName || listener.RouteKind != resources.RouteKindHTTP {
		t.Errorf("listener = %+v", listener)
	}
	want := map[string]any{"matchExpressions": []any{map[string]any{"key": neteye.TenantLabel, "operator": "Exists"}}}
	if !reflect.DeepEqual(listener.RouteNamespaceSelector, want) {
		t.Errorf("route namespace selector = %v, want %v", listener.RouteNamespaceSelector, want)
	}
}
