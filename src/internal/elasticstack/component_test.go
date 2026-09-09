// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

func TestOTelCollectorBuildsIsolatedIngressResources(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(collectorPrerequisites(namespace)...).Build()
	ready, message, err := NewOTelCollectorComponent(c).Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{Replicas: 2}, "identity.example.com", namespace, "gateway", "collector-image", issuerRef(), owner())
	if err != nil || ready || !strings.Contains(message, "TLS Certificate") {
		t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 2 || deployment.Spec.Selector == nil || deployment.Spec.Selector.MatchLabels["app"] != collectorAppLabel {
		t.Fatalf("deployment=%#v", deployment.Spec)
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Image; got != "collector-image" {
		t.Fatalf("image=%q", got)
	}
	if findEnv(deployment.Spec.Template.Spec.Containers[0].Env, "ELASTICSEARCH_API_KEY") != nil {
		t.Fatal("collector must not mount Elasticsearch API key")
	}
	variables := configMap(t, c, namespace, VariablesConfigMapName)
	if got := variables.Data["OIDC_ISSUER"]; got != "https://identity.example.com/auth/realms/master" {
		t.Fatalf("OIDC_ISSUER=%q", got)
	}
	if _, ok := variables.Data["ELASTICSEARCH_ENDPOINTS"]; ok {
		t.Fatal("collector variables include Elasticsearch endpoints")
	}
	assertPipelineReferences(t, configMap(t, c, namespace, ConfigMapName).Data["otel-collector-config.yaml"], false)
	service := &corev1.Service{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: ServiceName}, service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.Selector["app"] != collectorAppLabel {
		t.Fatalf("service selector=%v", service.Spec.Selector)
	}
	assertPolicySelector(t, c, namespace, IngressPolicyName, collectorAppLabel)
	assertPolicySelector(t, c, namespace, EgressPolicyName, collectorAppLabel)
	assertNamespaceScopedPeer(t, c, namespace, EgressPolicyName, "egress", "toEndpoints", edotGatewayAppLabel)
}

func TestOTelCollectorPrerequisitesAndInvalidSpecDoNotCreateWorkloads(t *testing.T) {
	namespace := "telemetry"
	for _, test := range []struct {
		name    string
		spec    *neteye.NetEyeOtelCollectorSpec
		objects []client.Object
	}{
		{"missing basic auth", &neteye.NetEyeOtelCollectorSpec{}, []client.Object{rootCA(namespace)}},
		{"empty htpasswd", &neteye.NetEyeOtelCollectorSpec{}, []client.Object{basicAuth(namespace, nil), rootCA(namespace)}},
		{"negative replicas", &neteye.NetEyeOtelCollectorSpec{Replicas: -1}, collectorPrerequisites(namespace)},
		{"invalid identity hostname", &neteye.NetEyeOtelCollectorSpec{}, collectorPrerequisites(namespace)},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(test.objects...).Build()
			identity := "identity.example.com"
			if test.name == "invalid identity hostname" {
				identity = "https://identity.example.com"
			}
			ready, message, err := NewOTelCollectorComponent(c).Ensure(context.Background(), namespace, test.spec, identity, namespace, "gateway", "image", issuerRef(), owner())
			if err != nil || ready || message == "" {
				t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Fatal("workload created before valid prerequisites")
			}
		})
	}
}

func TestEDOTGatewayBuildsElasticsearchBoundary(t *testing.T) {
	namespace := "telemetry"
	spec := &neteye.NetEyeEDOTGatewaySpec{Replicas: 2, ElasticsearchEndpoints: []string{"https://elastic.example.com:9243"}, APIKeySecret: &neteye.NetEyeSecretKeySelector{Name: "elastic-key", Key: "key"}, RootCASecretName: "elastic-ca"}
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "elastic-key"}, Data: map[string][]byte{"key": []byte("value")}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "elastic-ca"}, Data: map[string][]byte{"tls.crt": []byte("ca")}}).Build()
	ready, _, err := NewEDOTGatewayComponent(c).Ensure(context.Background(), namespace, spec, "gateway-image", owner())
	if err != nil || ready {
		t.Fatalf("ready=%t err=%v", ready, err)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: EDOTGatewayDeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "gateway-image" || deployment.Spec.Selector.MatchLabels["app"] != edotGatewayAppLabel || findEnv(container.Env, "ELASTICSEARCH_API_KEY") == nil {
		t.Fatalf("deployment=%#v", deployment.Spec)
	}
	if len(container.Ports) != 3 || container.Ports[0].ContainerPort != 13133 || container.StartupProbe == nil || container.ReadinessProbe == nil || container.LivenessProbe == nil {
		t.Fatalf("gateway ports/probes=%#v", container)
	}
	if findVolume(deployment.Spec.Template.Spec.Volumes, "root-ca").Secret.SecretName != "elastic-ca" || findVolume(deployment.Spec.Template.Spec.Volumes, "trusted-ca").EmptyDir == nil {
		t.Fatal("gateway root CA was not mounted")
	}
	if got := configMap(t, c, namespace, EDOTGatewayVariablesConfigMapName).Data["ELASTICSEARCH_ENDPOINTS"]; got != `["https://elastic.example.com:9243"]` {
		t.Fatalf("endpoints=%q", got)
	}
	assertPipelineReferences(t, configMap(t, c, namespace, EDOTGatewayConfigMapName).Data["edot-gateway-config.yaml"], true)
	assertEDOTMapping(t, configMap(t, c, namespace, EDOTGatewayConfigMapName).Data["edot-gateway-config.yaml"])
	assertPolicySelector(t, c, namespace, EDOTGatewayIngressPolicyName, edotGatewayAppLabel)
	assertPolicySelector(t, c, namespace, EDOTGatewayEgressPolicyName, edotGatewayAppLabel)
	assertNamespaceScopedPeer(t, c, namespace, EDOTGatewayIngressPolicyName, "ingress", "fromEndpoints", collectorAppLabel)
}

func TestInputResourceVersionsChangeDeploymentTemplate(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(collectorPrerequisites(namespace)...).Build()
	component := NewOTelCollectorComponent(c)
	if _, _, err := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", issuerRef(), owner()); err != nil {
		t.Fatal(err)
	}
	before := deploymentAnnotations(t, c, namespace, DeploymentName)
	variables := configMap(t, c, namespace, VariablesConfigMapName)
	variables.Data["rollout-test"] = "changed"
	if err := c.Update(context.Background(), variables); err != nil {
		t.Fatal(err)
	}
	secret := basicAuth(namespace, map[string][]byte{"htpasswd": []byte("replacement")})
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if _, _, err := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", issuerRef(), owner()); err != nil {
		t.Fatal(err)
	}
	after := deploymentAnnotations(t, c, namespace, DeploymentName)
	if before["neteye.cloud/configmap-"+VariablesConfigMapName+"-resource-version"] == after["neteye.cloud/configmap-"+VariablesConfigMapName+"-resource-version"] || before["neteye.cloud/secret-"+DefaultBasicAuthSecretName+"-resource-version"] == after["neteye.cloud/secret-"+DefaultBasicAuthSecretName+"-resource-version"] {
		t.Fatalf("template annotations did not track input versions: before=%v after=%v", before, after)
	}
}

func TestCollectorWaitsForRouteReadiness(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(collectorPrerequisites(namespace)...).Build()
	component := NewOTelCollectorComponent(c)
	ready, message, err := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", issuerRef(), owner())
	if err != nil || ready || !strings.Contains(message, "TLS Certificate") {
		t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
	}
	markCertificateReady(t, c, namespace, GRPCTLSCertName)
	markCertificateReady(t, c, namespace, CrossTenantTLSCertName)
	ready, message, err = component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", issuerRef(), owner())
	if err != nil || ready || !strings.Contains(message, "GRPCRoute") {
		t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
	}
	markRouteReady(t, c, namespace, GRPCRouteName, "GRPCRoute")
	markRouteReady(t, c, namespace, HTTPRouteName, "HTTPRoute")
	markReadyDeployment(t, c, namespace, DeploymentName)
	ready, _, err = component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", issuerRef(), owner())
	if err != nil || !ready {
		t.Fatalf("ready=%t err=%v", ready, err)
	}
}

func TestEDOTGatewayRejectsInvalidPrerequisitesAndEndpoints(t *testing.T) {
	namespace := "telemetry"
	for _, test := range []struct {
		name    string
		spec    *neteye.NetEyeEDOTGatewaySpec
		objects []client.Object
	}{
		{"empty endpoint", &neteye.NetEyeEDOTGatewaySpec{}, nil},
		{"http endpoint", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"http://elastic.example.com"}}, nil},
		{"invalid endpoint port", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elastic.example.com:65536"}}, nil},
		{"empty secret selector", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elastic.example.com"}, APIKeySecret: &neteye.NetEyeSecretKeySelector{}}, nil},
		{"missing key", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elastic.example.com"}}, []client.Object{rootCA(namespace), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultAPIKeySecretName}}}},
		{"negative replicas", &neteye.NetEyeEDOTGatewaySpec{Replicas: -1, ElasticsearchEndpoints: []string{"https://elastic.example.com"}}, gatewayPrerequisites(namespace)},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(test.objects...).Build()
			ready, message, err := NewEDOTGatewayComponent(c).Ensure(context.Background(), namespace, test.spec, "gateway-image", owner())
			if ready || (message == "" && err == nil) {
				t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: EDOTGatewayDeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Fatal("gateway workload created from invalid configuration")
			}
		})
	}
}

func TestComponentsReportReadinessAndDeleteOnlyOwnedResources(t *testing.T) {
	namespace := "telemetry"
	scheme := componentScheme(t)
	objects := append(collectorPrerequisites(namespace), gatewayPrerequisites(namespace)...)
	unique := objects[:0]
	seen := map[string]bool{}
	for _, object := range objects {
		key := object.GetNamespace() + "/" + object.GetName()
		if !seen[key] {
			seen[key] = true
			unique = append(unique, object)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&appsv1.Deployment{}).WithObjects(unique...).Build()
	collector := NewOTelCollectorComponent(c)
	gateway := NewEDOTGatewayComponent(c)
	if ready, _, err := collector.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "collector-image", issuerRef(), owner()); err != nil || ready {
		t.Fatalf("collector: ready=%t err=%v", ready, err)
	}
	if ready, _, err := gateway.Ensure(context.Background(), namespace, &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elastic.example.com"}}, "gateway-image", owner()); err != nil || ready {
		t.Fatalf("gateway: ready=%t err=%v", ready, err)
	}
	markReadyDeployment(t, c, namespace, DeploymentName)
	markReadyDeployment(t, c, namespace, EDOTGatewayDeploymentName)
	markCertificateReady(t, c, namespace, GRPCTLSCertName)
	markCertificateReady(t, c, namespace, CrossTenantTLSCertName)
	markRouteReady(t, c, namespace, GRPCRouteName, "GRPCRoute")
	markRouteReady(t, c, namespace, HTTPRouteName, "HTTPRoute")
	if ready, _, err := collector.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "collector-image", issuerRef(), owner()); err != nil || !ready {
		t.Fatalf("collector ready=%t err=%v", ready, err)
	}
	if ready, _, err := gateway.Ensure(context.Background(), namespace, &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elastic.example.com"}}, "gateway-image", owner()); err != nil || !ready {
		t.Fatalf("gateway ready=%t err=%v", ready, err)
	}
	if err := collector.Delete(context.Background(), namespace, owner()); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, c, namespace, DeploymentName, &appsv1.Deployment{})
	assertPresent(t, c, namespace, EDOTGatewayDeploymentName, &appsv1.Deployment{})
	if err := gateway.Delete(context.Background(), namespace, owner()); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, c, namespace, EDOTGatewayDeploymentName, &appsv1.Deployment{})
	if err := c.Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: EDOTGatewayConfigMapName, OwnerReferences: []metav1.OwnerReference{{UID: "other", Controller: boolPtr(true)}}}}); err != nil {
		t.Fatal(err)
	}
	if err := gateway.Delete(context.Background(), namespace, owner()); err != nil {
		t.Fatal(err)
	}
	assertPresent(t, c, namespace, EDOTGatewayConfigMapName, &corev1.ConfigMap{})
	assertPresent(t, c, namespace, DefaultAPIKeySecretName, &corev1.Secret{})
}

func assertPipelineReferences(t *testing.T, document string, expectElasticsearch bool) {
	t.Helper()
	var config map[string]any
	if err := yaml.Unmarshal([]byte(document), &config); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"receivers", "processors", "exporters", "extensions"} {
		if _, ok := config[section].(map[string]any); !ok {
			t.Fatalf("missing %s", section)
		}
	}
	exporters := config["exporters"].(map[string]any)
	_, elasticsearch := exporters["elasticsearch/otel"]
	if elasticsearch != expectElasticsearch {
		t.Fatalf("Elasticsearch exporter=%t want=%t", elasticsearch, expectElasticsearch)
	}
	pipelines := config["service"].(map[string]any)["pipelines"].(map[string]any)
	for name, pipeline := range pipelines {
		pipelineMap := pipeline.(map[string]any)
		for _, field := range []string{"receivers", "processors", "connectors", "exporters"} {
			refs, exists := pipelineMap[field]
			if !exists {
				continue
			}
			for _, ref := range refs.([]any) {
				if _, ok := config[field].(map[string]any)[ref.(string)]; !ok {
					t.Fatalf("pipeline %s references undefined %s %q", name, field, ref)
				}
			}
		}
	}
	service := config["service"].(map[string]any)
	for _, ref := range service["extensions"].([]any) {
		if _, ok := config["extensions"].(map[string]any)[ref.(string)]; !ok {
			t.Fatalf("service references undefined extension %q", ref)
		}
	}
}
func assertEDOTMapping(t *testing.T, document string) {
	t.Helper()
	var config map[string]any
	if err := yaml.Unmarshal([]byte(document), &config); err != nil {
		t.Fatal(err)
	}
	mapping := config["exporters"].(map[string]any)["elasticsearch/otel"].(map[string]any)["mapping"].(map[string]any)
	if mapping["mode"] != "otel" {
		t.Fatalf("mapping=%v", mapping)
	}
}
func deploymentAnnotations(t *testing.T, c client.Client, namespace, name string) map[string]string {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		t.Fatal(err)
	}
	return deployment.Spec.Template.Annotations
}
func assertPolicySelector(t *testing.T, c client.Client, namespace, name, app string) {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(ciliumPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		t.Fatal(err)
	}
	selector, _, _ := unstructured.NestedString(object.Object, "spec", "endpointSelector", "matchLabels", "k8s:app")
	if selector != app {
		t.Fatalf("policy %s selects %q", name, selector)
	}
}
func assertNamespaceScopedPeer(t *testing.T, c client.Client, namespace, policyName, direction, peerField, app string) {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(ciliumPolicyGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: policyName}, object); err != nil {
		t.Fatal(err)
	}
	rules, _, _ := unstructured.NestedSlice(object.Object, "spec", direction)
	for _, rule := range rules {
		peers, _, _ := unstructured.NestedSlice(rule.(map[string]any), peerField)
		for _, peer := range peers {
			labels, _, _ := unstructured.NestedStringMap(peer.(map[string]any), "matchLabels")
			if labels["k8s:app"] == app {
				if labels["k8s:io.kubernetes.pod.namespace"] != namespace {
					t.Fatalf("policy %s peer labels=%v", policyName, labels)
				}
				return
			}
		}
	}
	t.Fatalf("policy %s has no %s peer for %s", policyName, peerField, app)
}
func assertMissing(t *testing.T, c client.Client, namespace, name string, object client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, object); err == nil {
		t.Fatalf("%T %s was not deleted", object, name)
	}
}
func assertPresent(t *testing.T, c client.Client, namespace, name string, object client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		t.Fatalf("%T %s missing: %v", object, name, err)
	}
}
func configMap(t *testing.T, c client.Client, namespace, name string) *corev1.ConfigMap {
	t.Helper()
	result := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, result); err != nil {
		t.Fatal(err)
	}
	return result
}
func findEnv(values []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range values {
		if values[i].Name == name {
			return &values[i]
		}
	}
	return nil
}
func findVolume(values []corev1.Volume, name string) corev1.Volume {
	for _, value := range values {
		if value.Name == name {
			return value
		}
	}
	return corev1.Volume{}
}
func componentScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	result := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(result); err != nil {
		t.Fatal(err)
	}
	return result
}
func collectorPrerequisites(namespace string) []client.Object {
	return []client.Object{basicAuth(namespace, map[string][]byte{"htpasswd": []byte("hash")}), rootCA(namespace)}
}
func gatewayPrerequisites(namespace string) []client.Object {
	return []client.Object{&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultAPIKeySecretName}, Data: map[string][]byte{DefaultAPIKeySecretKey: []byte("key")}}, rootCA(namespace)}
}
func basicAuth(namespace string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultBasicAuthSecretName}, Data: data}
}
func rootCA(namespace string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultRootCASecretName}, Data: map[string][]byte{"tls.crt": []byte("certificate")}}
}
func owner() metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", UID: "owner", Controller: boolPtr(true)}
}
func boolPtr(value bool) *bool { return &value }
func issuerRef() resources.CertificateIssuerRef {
	return resources.CertificateIssuerRef{Name: "internal-issuer"}
}
func markReadyDeployment(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	d := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, d); err != nil {
		t.Fatal(err)
	}
	d.Status.ObservedGeneration = d.Generation
	d.Status.ReadyReplicas = *d.Spec.Replicas
	d.Status.UpdatedReplicas = *d.Spec.Replicas
	if err := c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}
func markCertificateReady(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, certificate); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(certificate.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
}
func markRouteReady(t *testing.T, c client.Client, namespace, name, kind string) {
	t.Helper()
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: kind})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, route); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(route.Object, []any{map[string]any{"type": "Accepted", "status": "True"}, map[string]any{"type": "ResolvedRefs", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
}
