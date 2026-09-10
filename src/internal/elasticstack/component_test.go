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
	outcome := NewOTelCollectorComponent(c).Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{Replicas: 2}, "identity.example.com", namespace, "gateway", "collector-image", "ca-bundle-image", issuerRef(), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady || !strings.Contains(outcome.Message, "TLS Certificate") {
		t.Fatalf("outcome=%+v", outcome)
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
	if oidc := findEnv(deployment.Spec.Template.Spec.Containers[0].Env, "OIDC_ISSUER"); oidc == nil || oidc.Value != "https://identity.example.com/auth/realms/master" {
		t.Fatalf("OIDC_ISSUER env = %+v", oidc)
	}
	if findEnv(deployment.Spec.Template.Spec.Containers[0].Env, "ELASTICSEARCH_ENDPOINTS") != nil {
		t.Fatal("collector must not receive Elasticsearch endpoints")
	}
	if len(deployment.Spec.Template.Spec.Containers[0].EnvFrom) != 0 {
		t.Fatalf("collector must not use envFrom: %#v", deployment.Spec.Template.Spec.Containers[0].EnvFrom)
	}
	assertMissing(t, c, namespace, VariablesConfigMapName, &corev1.ConfigMap{})
	if findVolume(deployment.Spec.Template.Spec.Volumes, "config").ConfigMap.Name != ConfigMapName {
		t.Fatal("collector config-file ConfigMap is not mounted")
	}
	assertPipelineReferences(t, configMap(t, c, namespace, ConfigMapName).Data["otel-collector-config.yaml"], false)
	assertCollectorBatching(t, configMap(t, c, namespace, ConfigMapName).Data["otel-collector-config.yaml"])
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
			outcome := NewOTelCollectorComponent(c).Ensure(context.Background(), namespace, test.spec, identity, namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner())
			if outcome.Phase != PhaseDegraded || outcome.Reason == "" || outcome.Message == "" {
				t.Fatalf("outcome=%+v", outcome)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Fatal("workload created before valid prerequisites")
			}
		})
	}
}

func TestEDOTGatewayBuildsElasticsearchBoundary(t *testing.T) {
	namespace := "telemetry"
	spec := &neteye.NetEyeEDOTGatewaySpec{Replicas: 2, ElasticsearchEndpoints: []string{"https://198.51.100.23:9243"}, APIKeySecret: &neteye.NetEyeSecretKeySelector{Name: "elastic-key", Key: "key"}, RootCASecretName: "elastic-ca"}
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "elastic-key"}, Data: map[string][]byte{"key": []byte("value")}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "elastic-ca"}, Data: map[string][]byte{"tls.crt": []byte("ca")}}).Build()
	outcome := NewEDOTGatewayComponent(c).Ensure(context.Background(), namespace, spec, "gateway-image", "ca-bundle-image", owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonDeploymentNotAvailable {
		t.Fatalf("outcome=%+v", outcome)
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
	if endpoints := findEnv(container.Env, "ELASTICSEARCH_ENDPOINTS"); endpoints == nil || endpoints.Value != `["https://198.51.100.23:9243"]` {
		t.Fatalf("ELASTICSEARCH_ENDPOINTS env = %+v", endpoints)
	}
	if apiKey := findEnv(container.Env, "ELASTICSEARCH_API_KEY"); apiKey == nil || apiKey.Value != "" || apiKey.ValueFrom == nil || apiKey.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("ELASTICSEARCH_API_KEY must be a SecretKeyRef with no inline value: %+v", apiKey)
	}
	if len(container.EnvFrom) != 0 {
		t.Fatalf("edot must not use envFrom: %#v", container.EnvFrom)
	}
	assertMissing(t, c, namespace, EDOTGatewayVariablesConfigMapName, &corev1.ConfigMap{})
	if findVolume(deployment.Spec.Template.Spec.Volumes, "config").ConfigMap.Name != EDOTGatewayConfigMapName {
		t.Fatal("edot config-file ConfigMap is not mounted")
	}
	assertPipelineReferences(t, configMap(t, c, namespace, EDOTGatewayConfigMapName).Data["edot-gateway-config.yaml"], true)
	assertEDOTMapping(t, configMap(t, c, namespace, EDOTGatewayConfigMapName).Data["edot-gateway-config.yaml"])
	assertEDOTPipelineTopology(t, configMap(t, c, namespace, EDOTGatewayConfigMapName).Data["edot-gateway-config.yaml"])
	assertPolicySelector(t, c, namespace, EDOTGatewayIngressPolicyName, edotGatewayAppLabel)
	assertPolicySelector(t, c, namespace, EDOTGatewayEgressPolicyName, edotGatewayAppLabel)
	assertNamespaceScopedPeer(t, c, namespace, EDOTGatewayIngressPolicyName, "ingress", "fromEndpoints", collectorAppLabel)
}

func TestInputResourceVersionsChangeDeploymentTemplate(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(collectorPrerequisites(namespace)...).Build()
	component := NewOTelCollectorComponent(c)
	if outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	before := deploymentAnnotations(t, c, namespace, DeploymentName)
	config := configMap(t, c, namespace, ConfigMapName)
	config.Data["rollout-test"] = "changed"
	if err := c.Update(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	secret := basicAuth(namespace, map[string][]byte{"htpasswd": []byte("replacement")})
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	after := deploymentAnnotations(t, c, namespace, DeploymentName)
	if before["neteye.cloud/config-resource-version"] == after["neteye.cloud/config-resource-version"] || before["neteye.cloud/basic-auth-resource-version"] == after["neteye.cloud/basic-auth-resource-version"] {
		t.Fatalf("template annotations did not track input versions: before=%v after=%v", before, after)
	}
	if _, ok := after["neteye.cloud/variables-resource-version"]; ok {
		t.Fatalf("variables-resource-version annotation must not exist: %v", after)
	}
	if len(after) != 3 {
		t.Fatalf("collector rollout annotations = %v, want exactly 3 fixed keys", after)
	}
}

func TestEDOTInputVersionsUseFixedAnnotationKeysWithLongSecretName(t *testing.T) {
	namespace := "telemetry"
	longName := strings.Repeat("a", 61) + ".example"
	spec := &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://192.0.2.1"}, APIKeySecret: &neteye.NetEyeSecretKeySelector{Name: longName, Key: "key"}}
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: longName}, Data: map[string][]byte{"key": []byte("v1")}}, rootCA(namespace)).Build()
	component := NewEDOTGatewayComponent(c)
	if outcome := component.Ensure(context.Background(), namespace, spec, "image", "ca-bundle-image", owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	before := deploymentAnnotations(t, c, namespace, EDOTGatewayDeploymentName)
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: longName}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data["key"] = []byte("v2")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if outcome := component.Ensure(context.Background(), namespace, spec, "image", "ca-bundle-image", owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	after := deploymentAnnotations(t, c, namespace, EDOTGatewayDeploymentName)
	if before["neteye.cloud/api-key-resource-version"] == after["neteye.cloud/api-key-resource-version"] || len(after) != 3 {
		t.Fatalf("annotations=%v", after)
	}
	if _, ok := after["neteye.cloud/variables-resource-version"]; ok {
		t.Fatalf("variables-resource-version annotation must not exist: %v", after)
	}
	for key := range after {
		if len(key) > 253 {
			t.Fatalf("invalid annotation key %q", key)
		}
	}
}

func TestChangingOIDCHostnameChangesCollectorPodTemplate(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(collectorPrerequisites(namespace)...).Build()
	component := NewOTelCollectorComponent(c)
	if outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	first := deploymentEnvValue(t, c, namespace, DeploymentName, "OIDC_ISSUER")
	if outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.other.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	second := deploymentEnvValue(t, c, namespace, DeploymentName, "OIDC_ISSUER")
	if first == second || second != "https://identity.other.example.com/auth/realms/master" {
		t.Fatalf("OIDC hostname change did not update the collector pod template: %q -> %q", first, second)
	}
}

func TestChangingElasticsearchEndpointsChangesEDOTPodTemplate(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(gatewayPrerequisites(namespace)...).Build()
	component := NewEDOTGatewayComponent(c)
	if outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://203.0.113.1:9200"}}, "image", "ca-bundle-image", owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	first := deploymentEnvValue(t, c, namespace, EDOTGatewayDeploymentName, "ELASTICSEARCH_ENDPOINTS")
	if outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://203.0.113.2:9200"}}, "image", "ca-bundle-image", owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("outcome=%+v", outcome)
	}
	second := deploymentEnvValue(t, c, namespace, EDOTGatewayDeploymentName, "ELASTICSEARCH_ENDPOINTS")
	if first == second || second != `["https://203.0.113.2:9200"]` {
		t.Fatalf("endpoint change did not update the EDOT pod template: %q -> %q", first, second)
	}
}

func TestLegacyVariablesConfigMapsArePrunedOnlyWhenOwned(t *testing.T) {
	namespace := "telemetry"
	controller := true
	for _, test := range []struct {
		name   string
		owners []metav1.OwnerReference
		pruned bool
	}{
		{"owned by this NetEye", []metav1.OwnerReference{owner()}, true},
		{"unowned", nil, false},
		{"controlled by another owner", []metav1.OwnerReference{{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "other", UID: "other", Controller: &controller}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			legacy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: VariablesConfigMapName, OwnerReferences: test.owners}, Data: map[string]string{"OIDC_ISSUER": "stale"}}
			objects := append(collectorPrerequisites(namespace), legacy)
			c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(objects...).Build()
			if outcome := NewOTelCollectorComponent(c).Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseProgressing {
				t.Fatalf("outcome=%+v", outcome)
			}
			if test.pruned {
				assertMissing(t, c, namespace, VariablesConfigMapName, &corev1.ConfigMap{})
			} else {
				assertPresent(t, c, namespace, VariablesConfigMapName, &corev1.ConfigMap{})
			}
		})
	}
}

func TestDeleteRemovesLegacyVariablesConfigMapsWhenOwned(t *testing.T) {
	namespace := "telemetry"
	controller := true
	foreign := metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "other", UID: "other", Controller: &controller}
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: VariablesConfigMapName, OwnerReferences: []metav1.OwnerReference{owner()}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: EDOTGatewayVariablesConfigMapName, OwnerReferences: []metav1.OwnerReference{foreign}}},
	).Build()
	if err := NewOTelCollectorComponent(c).Delete(context.Background(), namespace, owner()); err != nil {
		t.Fatal(err)
	}
	if err := NewEDOTGatewayComponent(c).Delete(context.Background(), namespace, owner()); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, c, namespace, VariablesConfigMapName, &corev1.ConfigMap{})
	assertPresent(t, c, namespace, EDOTGatewayVariablesConfigMapName, &corev1.ConfigMap{})
}

func deploymentEnvValue(t *testing.T, c client.Client, namespace, name, env string) string {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		t.Fatal(err)
	}
	value := findEnv(deployment.Spec.Template.Spec.Containers[0].Env, env)
	if value == nil {
		t.Fatalf("env %q not found on deployment %q", env, name)
	}
	return value.Value
}

func TestRouteReadyUsesMatchingParentStatus(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).Build()
	owner := owner()
	if err := resources.EnsureGRPCRoute(context.Background(), c, namespace, GRPCRouteName, namespace, "gateway", GRPCListenerName, GRPCRouteHostname, ServiceName, 4317, &owner); err != nil {
		t.Fatal(err)
	}
	expected := expectedParent{group: "gateway.networking.k8s.io", kind: "Gateway", namespace: namespace, name: "gateway", section: GRPCListenerName}
	if ready, _, err := routeReady(context.Background(), c, namespace, GRPCRouteName, "GRPCRoute", expected); err != nil || ready {
		t.Fatalf("no parents ready=%t err=%v", ready, err)
	}
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "other", true, true, 0)
	if ready, _, err := routeReady(context.Background(), c, namespace, GRPCRouteName, "GRPCRoute", expected); err != nil || ready {
		t.Fatalf("unrelated parent ready=%t err=%v", ready, err)
	}
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "gateway", false, true, 0)
	if ready, _, err := routeReady(context.Background(), c, namespace, GRPCRouteName, "GRPCRoute", expected); err != nil || ready {
		t.Fatalf("rejected parent ready=%t err=%v", ready, err)
	}
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "gateway", true, false, 0)
	if ready, _, err := routeReady(context.Background(), c, namespace, GRPCRouteName, "GRPCRoute", expected); err != nil || ready {
		t.Fatalf("unresolved parent ready=%t err=%v", ready, err)
	}
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "gateway", true, true, -1)
	if ready, _, err := routeReady(context.Background(), c, namespace, GRPCRouteName, "GRPCRoute", expected); err != nil || ready {
		t.Fatalf("stale parent ready=%t err=%v", ready, err)
	}
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "gateway", true, true, 0)
	if ready, _, err := routeReady(context.Background(), c, namespace, GRPCRouteName, "GRPCRoute", expected); err != nil || !ready {
		t.Fatalf("accepted parent ready=%t err=%v", ready, err)
	}
}

func TestCABundleCommandIsSafe(t *testing.T) {
	for _, fragment := range []string{"tmp_bundle=/work/ca-bundle.pem.tmp", "final_bundle=/work/ca-bundle.pem", "/input/system/tls-ca-bundle.pem", "for cert in /input/neteye/*", "[ -f \"$cert\" ]", "'/^#/d'", "'/^[[:space:]]*$/d'", "'s/TRUSTED //g'", "chmod 755 /work", "chmod 644 \"$final_bundle\""} {
		if !strings.Contains(caBundleCommand, fragment) {
			t.Fatalf("missing %q", fragment)
		}
	}
	if strings.Contains(caBundleCommand, "cat /input/neteye/*") || strings.Contains(caBundleCommand, "echo $") {
		t.Fatal("CA script can emit certificate contents")
	}
}

func TestTelemetryDeploymentsUseResolvedCABundleImage(t *testing.T) {
	const caBundleImage = "registry.example/ca-bundle@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	collector := collectorDeployment("telemetry", &neteye.NetEyeOtelCollectorSpec{}, "collector-image", caBundleImage, "https://identity.example.com/auth/realms/master", nil)
	gateway := edotGatewayDeployment("telemetry", &neteye.NetEyeEDOTGatewaySpec{}, "gateway-image", caBundleImage, `["https://198.51.100.10:9200"]`, nil)
	collectorInit := collector.Spec.Template.Spec.InitContainers[0].Image
	gatewayInit := gateway.Spec.Template.Spec.InitContainers[0].Image
	if collectorInit != caBundleImage {
		t.Errorf("collector CA-bundle init image = %q, want %q", collectorInit, caBundleImage)
	}
	if gatewayInit != caBundleImage {
		t.Errorf("edot CA-bundle init image = %q, want %q", gatewayInit, caBundleImage)
	}
	if collectorInit != gatewayInit {
		t.Errorf("collector and edot use different CA-bundle images: %q vs %q", collectorInit, gatewayInit)
	}
	if got := collector.Spec.Template.Spec.Containers[0].Image; got != "collector-image" {
		t.Errorf("collector main image = %q, want collector-image", got)
	}
	if got := gateway.Spec.Template.Spec.Containers[0].Image; got != "gateway-image" {
		t.Errorf("edot main image = %q, want gateway-image", got)
	}
}

func TestCollectorEgressPermitsDNSAndRestrictsNonDNS(t *testing.T) {
	if !strings.Contains(collectorConfig, "endpoint: otel-edot-gateway:4317") {
		t.Fatal("collector must export to the short EDOT service endpoint otel-edot-gateway:4317")
	}
	if strings.Contains(collectorConfig, ".svc") || strings.Contains(collectorConfig, "cluster.local") {
		t.Fatal("collector must not hardcode a cluster domain")
	}
	egress, _, err := unstructured.NestedSlice(map[string]any{"spec": collectorEgressPolicy("telemetry", "identity.example.com")}, "spec", "egress")
	if err != nil {
		t.Fatal(err)
	}
	var dnsOK, edotOK, oidcOK bool
	for _, raw := range egress {
		rule := raw.(map[string]any)
		if entities, ok := rule["toEntities"].([]any); ok {
			for _, entity := range entities {
				if entity != "host" && entity != "remote-node" {
					t.Fatalf("collector egress grants arbitrary entity egress: %v", rule)
				}
			}
			if !egressHasTCPPort(rule, "443") {
				t.Fatalf("collector node egress is not restricted to TCP/443: %v", rule)
			}
		}
		if _, ok := rule["toCIDR"]; ok {
			t.Fatalf("collector egress grants CIDR egress: %v", rule)
		}
		if _, ok := rule["toCIDRSet"]; ok {
			t.Fatalf("collector egress grants CIDR-set egress: %v", rule)
		}
		if toPorts, ok := rule["toPorts"].([]any); ok {
			for _, tp := range toPorts {
				dnsRules, _, _ := unstructured.NestedSlice(tp.(map[string]any), "rules", "dns")
				for _, d := range dnsRules {
					if d.(map[string]any)["matchPattern"] == "*" {
						dnsOK = true
					}
				}
			}
		}
		if endpoints, ok := rule["toEndpoints"].([]any); ok {
			for _, ep := range endpoints {
				labels, _, _ := unstructured.NestedStringMap(ep.(map[string]any), "matchLabels")
				if labels["k8s:app"] == edotGatewayAppLabel && labels["k8s:io.kubernetes.pod.namespace"] == "telemetry" && egressHasTCPPort(rule, "4317") {
					edotOK = true
				}
			}
		}
		if fqdns, ok := rule["toFQDNs"].([]any); ok {
			for _, f := range fqdns {
				if f.(map[string]any)["matchName"] == "identity.example.com" && egressHasTCPPort(rule, "443") {
					oidcOK = true
				}
			}
		}
	}
	if !dnsOK {
		t.Fatal("collector egress does not permit portable DNS resolution (matchPattern \"*\")")
	}
	if !edotOK {
		t.Fatal("collector egress does not permit EDOT pods on 4317")
	}
	if !oidcOK {
		t.Fatal("collector egress does not permit the OIDC issuer FQDN on 443")
	}
}

func egressHasTCPPort(rule map[string]any, port string) bool {
	toPorts, ok := rule["toPorts"].([]any)
	if !ok {
		return false
	}
	for _, tp := range toPorts {
		ports, ok := tp.(map[string]any)["ports"].([]any)
		if !ok {
			continue
		}
		for _, p := range ports {
			portMap := p.(map[string]any)
			if portMap["port"] == port && portMap["protocol"] == "TCP" {
				return true
			}
		}
	}
	return false
}

func TestCollectorWaitsForRouteReadiness(t *testing.T) {
	namespace := "telemetry"
	c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(collectorPrerequisites(namespace)...).Build()
	component := NewOTelCollectorComponent(c)
	outcome := component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonCertificateNotReady {
		t.Fatalf("outcome=%+v", outcome)
	}
	markCertificateReady(t, c, namespace, GRPCTLSCertName)
	markCertificateReady(t, c, namespace, CrossTenantTLSCertName)
	outcome = component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner())
	if outcome.Phase != PhaseProgressing || outcome.Reason != ReasonRouteNotReady {
		t.Fatalf("outcome=%+v", outcome)
	}
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "gateway", true, true, 0)
	markRouteParentConditions(t, c, namespace, HTTPRouteName, "HTTPRoute", CrossTenantListenerName, "gateway", true, true, 0)
	markReadyDeployment(t, c, namespace, DeploymentName)
	outcome = component.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "image", "ca-bundle-image", issuerRef(), owner())
	if outcome.Phase != PhaseReady {
		t.Fatalf("outcome=%+v", outcome)
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
		{"http endpoint", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"http://192.0.2.1"}}, nil},
		{"dns endpoint", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elastic.example.com:9200"}}, nil},
		{"invalid endpoint port", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://192.0.2.1:65536"}}, nil},
		{"empty secret selector", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://192.0.2.1"}, APIKeySecret: &neteye.NetEyeSecretKeySelector{}}, nil},
		{"missing key", &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://192.0.2.1"}}, []client.Object{rootCA(namespace), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultAPIKeySecretName}}}},
		{"negative replicas", &neteye.NetEyeEDOTGatewaySpec{Replicas: -1, ElasticsearchEndpoints: []string{"https://192.0.2.1"}}, gatewayPrerequisites(namespace)},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(componentScheme(t)).WithObjects(test.objects...).Build()
			outcome := NewEDOTGatewayComponent(c).Ensure(context.Background(), namespace, test.spec, "gateway-image", "ca-bundle-image", owner())
			if outcome.Phase != PhaseDegraded || outcome.Message == "" {
				t.Fatalf("outcome=%+v", outcome)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: EDOTGatewayDeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Fatal("gateway workload created from invalid configuration")
			}
		})
	}
}

func TestEDOTGatewayEgressRestrictsToEndpointCIDRs(t *testing.T) {
	targets := []egressTarget{{"192.0.2.10", "9200"}, {"2001:db8::1", "9243"}}
	egress, _, err := unstructured.NestedSlice(map[string]any{"spec": edotGatewayEgressPolicy(targets)}, "spec", "egress")
	if err != nil {
		t.Fatal(err)
	}
	if len(egress) != len(targets) {
		t.Fatalf("expected one egress rule per endpoint, got %d: %v", len(egress), egress)
	}
	want := map[string]string{"192.0.2.10/32": "9200", "2001:db8::1/128": "9243"}
	for _, raw := range egress {
		rule := raw.(map[string]any)
		if _, ok := rule["toFQDNs"]; ok {
			t.Fatalf("edot egress must not use toFQDNs: %v", rule)
		}
		if _, ok := rule["toEndpoints"]; ok {
			t.Fatalf("edot egress must not permit DNS resolution: %v", rule)
		}
		cidrs, ok := rule["toCIDR"].([]any)
		if !ok || len(cidrs) != 1 {
			t.Fatalf("edot egress rule must target exactly one CIDR: %v", rule)
		}
		cidr := cidrs[0].(string)
		port, known := want[cidr]
		if !known || !egressHasTCPPort(rule, port) {
			t.Fatalf("unexpected edot egress rule: %v", rule)
		}
		delete(want, cidr)
	}
	if len(want) != 0 {
		t.Fatalf("missing egress rules for endpoints: %v", want)
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
	if outcome := collector.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "collector-image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("collector outcome=%+v", outcome)
	}
	if outcome := gateway.Ensure(context.Background(), namespace, &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://192.0.2.1"}}, "gateway-image", "ca-bundle-image", owner()); outcome.Phase != PhaseProgressing {
		t.Fatalf("gateway outcome=%+v", outcome)
	}
	markReadyDeployment(t, c, namespace, DeploymentName)
	markReadyDeployment(t, c, namespace, EDOTGatewayDeploymentName)
	markCertificateReady(t, c, namespace, GRPCTLSCertName)
	markCertificateReady(t, c, namespace, CrossTenantTLSCertName)
	markRouteParentConditions(t, c, namespace, GRPCRouteName, "GRPCRoute", GRPCListenerName, "gateway", true, true, 0)
	markRouteParentConditions(t, c, namespace, HTTPRouteName, "HTTPRoute", CrossTenantListenerName, "gateway", true, true, 0)
	if outcome := collector.Ensure(context.Background(), namespace, &neteye.NetEyeOtelCollectorSpec{}, "identity.example.com", namespace, "gateway", "collector-image", "ca-bundle-image", issuerRef(), owner()); outcome.Phase != PhaseReady {
		t.Fatalf("collector outcome=%+v", outcome)
	}
	if outcome := gateway.Ensure(context.Background(), namespace, &neteye.NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://192.0.2.1"}}, "gateway-image", "ca-bundle-image", owner()); outcome.Phase != PhaseReady {
		t.Fatalf("gateway outcome=%+v", outcome)
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
				if !pipelineReferenceDefined(config, field, ref.(string)) {
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

func pipelineReferenceDefined(config map[string]any, section, reference string) bool {
	contains := func(name string) bool {
		values, ok := config[name].(map[string]any)
		if !ok {
			return false
		}
		_, ok = values[reference]
		return ok
	}
	switch section {
	case "receivers":
		return contains("receivers") || contains("connectors")
	case "exporters":
		return contains("exporters") || contains("connectors")
	default:
		return contains(section)
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

func assertEDOTPipelineTopology(t *testing.T, document string) {
	t.Helper()
	var config map[string]any
	if err := yaml.Unmarshal([]byte(document), &config); err != nil {
		t.Fatal(err)
	}
	if _, ok := config["processors"].(map[string]any)["elasticapm"]; !ok {
		t.Fatal("elasticapm processor missing")
	}
	if _, ok := config["connectors"].(map[string]any)["elasticapm"]; !ok {
		t.Fatal("elasticapm connector missing")
	}
	if _, ok := config["processors"].(map[string]any)["attributes/tenant"]; ok {
		t.Fatal("EDOT must not define attributes/tenant")
	}
	pipelines := config["service"].(map[string]any)["pipelines"].(map[string]any)
	aggregated := pipelines["metrics/aggregated-otel-metrics"].(map[string]any)
	if !stringListEquals(aggregated["receivers"].([]any), []string{"elasticapm"}) || !stringListEquals(aggregated["exporters"].([]any), []string{"elasticsearch/otel"}) {
		t.Fatalf("aggregated pipeline=%v", aggregated)
	}
}

func assertCollectorBatching(t *testing.T, document string) {
	t.Helper()
	var config map[string]any
	if err := yaml.Unmarshal([]byte(document), &config); err != nil {
		t.Fatal(err)
	}
	processors := config["processors"].(map[string]any)
	if processors["batch"].(map[string]any)["send_batch_max_size"] != float64(1500) || processors["batch/metrics"].(map[string]any)["send_batch_max_size"] != float64(0) {
		t.Fatalf("processors=%v", processors)
	}
	pipelines := config["service"].(map[string]any)["pipelines"].(map[string]any)
	for _, name := range []string{"metrics", "metrics/crosstenant"} {
		if !containsString(pipelines[name].(map[string]any)["processors"].([]any), "batch/metrics") {
			t.Fatalf("%s does not use batch/metrics", name)
		}
	}
	for _, name := range []string{"logs", "traces"} {
		if !containsString(pipelines[name].(map[string]any)["processors"].([]any), "batch") {
			t.Fatalf("%s does not use batch", name)
		}
	}
}

func containsString(values []any, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stringListEquals(values []any, target []string) bool {
	if len(values) != len(target) {
		return false
	}
	for i := range values {
		if values[i] != target[i] {
			return false
		}
	}
	return true
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

func markRouteParentConditions(t *testing.T, c client.Client, namespace, name, kind, section, gateway string, accepted, resolved bool, generationOffset int64) {
	t.Helper()
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: kind})
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, route); err != nil {
		t.Fatal(err)
	}
	generation := route.GetGeneration() + generationOffset
	if err := unstructured.SetNestedSlice(route.Object, []any{map[string]any{"parentRef": map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": namespace, "name": gateway, "sectionName": section}, "conditions": []any{map[string]any{"type": "Accepted", "status": map[bool]string{true: "True", false: "False"}[accepted], "observedGeneration": generation}, map[string]any{"type": "ResolvedRefs", "status": map[bool]string{true: "True", false: "False"}[resolved], "observedGeneration": generation}}}}, "status", "parents"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
}
