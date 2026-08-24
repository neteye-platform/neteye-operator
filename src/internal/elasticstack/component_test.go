// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
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

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func TestEnsureResourcesRequiresUserManagedPrerequisites(t *testing.T) {
	s := elasticScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	ready, message, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), "neteye-tenant-shared", elasticConfig(), "identity.example.com", "neteye-tenant-shared", "neteye", owner())
	if err != nil || ready || message == "" {
		t.Fatalf("result = ready %t, message %q, err %v", ready, message, err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "neteye-tenant-shared", Name: DeploymentName}, &appsv1.Deployment{}); err == nil {
		t.Fatal("deployment was fabricated before prerequisites existed")
	}
}

func TestEnsureResourcesRequiresCredentialData(t *testing.T) {
	namespace := "neteye-tenant-shared"
	for _, test := range []struct {
		name              string
		apiKey, basicAuth map[string][]byte
	}{{"missing api key", nil, map[string][]byte{"htpasswd": []byte("hash")}}, {"empty api key", map[string][]byte{"api_key": {}}, map[string][]byte{"htpasswd": []byte("hash")}}, {"missing htpasswd", map[string][]byte{"api_key": []byte("key")}, nil}, {"empty htpasswd", map[string][]byte{"api_key": []byte("key")}, map[string][]byte{"htpasswd": {}}}} {
		t.Run(test.name, func(t *testing.T) {
			s := elasticScheme(t)
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultAPIKeySecretName}, Data: test.apiKey}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultBasicAuthSecretName}, Data: test.basicAuth}, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultRootCAConfigMapName}}).Build()
			ready, message, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, elasticConfig(), "identity.example.com", namespace, "neteye", owner())
			if err != nil || ready || message == "" {
				t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Fatal("deployment created with invalid credential data")
			}
		})
	}
}

func TestEnsureResourcesRendersWorkloadAndVariables(t *testing.T) {
	s := elasticScheme(t)
	namespace := "neteye-tenant-shared"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(defaultPrerequisites(namespace)...).Build()
	ready, _, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, elasticConfig(), "identity.example.com", namespace, "neteye", owner())
	if err != nil || ready {
		t.Fatalf("ensure resources: ready %t err %v", ready, err)
	}
	variables := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: VariablesConfigMapName}, variables); err != nil {
		t.Fatal(err)
	}
	if variables.Data["ELASTICSEARCH_ENDPOINTS"] != `["https://elastic.example.com:9200"]` || variables.Data["OIDC_ISSUER"] != "https://identity.example.com/auth/realms/master" {
		t.Errorf("variables = %#v", variables.Data)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.OwnerReferences) != 1 || deployment.OwnerReferences[0].Name != "platform" {
		t.Errorf("owner refs = %#v", deployment.OwnerReferences)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Errorf("replicas = %v, want 1", deployment.Spec.Replicas)
	}
	for _, route := range []struct {
		name string
		kind string
	}{{GRPCRouteName, "GRPCRoute"}, {HTTPRouteName, "HTTPRoute"}} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: route.kind})
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: route.name}, u); err != nil {
			t.Fatalf("get %s: %v", route.kind, err)
		}
		parents, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
		if parents[0].(map[string]any)["namespace"] != namespace {
			t.Errorf("%s parents = %#v", route.kind, parents)
		}
		hostnames, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
		wantHostname := GRPCRouteHostname
		if route.kind == "HTTPRoute" {
			wantHostname = CrossTenantRouteHostname
		}
		if len(hostnames) != 1 || hostnames[0] != wantHostname {
			t.Errorf("%s hostnames = %#v, want %q", route.kind, hostnames, wantHostname)
		}
	}
}

func TestEnsureResourcesUsesConfiguredReferencesAndReplicas(t *testing.T) {
	s := elasticScheme(t)
	namespace := "neteye-tenant-shared"
	config := elasticConfig()
	config.OTelCollector.Replicas = 3
	config.OTelCollector.APIKeySecret = &neteye.NetEyeSecretKeySelector{Name: "debug-api-key", Key: "debug_key"}
	config.OTelCollector.BasicAuthSecretName = "debug-basicauth"
	config.OTelCollector.RootCAConfigMapName = "debug-root-ca"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: config.OTelCollector.APIKeySecret.Name}, Data: map[string][]byte{config.OTelCollector.APIKeySecret.Key: []byte("key")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: config.OTelCollector.BasicAuthSecretName}, Data: map[string][]byte{"htpasswd": []byte("hash")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: config.OTelCollector.RootCAConfigMapName}},
	).Build()
	if ready, message, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, config, "identity.example.com", namespace, "neteye", owner()); err != nil || ready {
		t.Fatalf("ready=%t message=%q err=%v", ready, message, err)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 3 {
		t.Errorf("replicas = %v, want 3", deployment.Spec.Replicas)
	}
}

func TestEnsureResourcesReportsReadyAfterDeploymentStatus(t *testing.T) {
	namespace := "neteye-tenant-shared"
	s := elasticScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&appsv1.Deployment{}).WithObjects(defaultPrerequisites(namespace)...).Build()
	component := NewComponent(c, logr.Discard())
	if ready, _, err := component.EnsureResources(context.Background(), namespace, elasticConfig(), "identity.example.com", namespace, "neteye", owner()); err != nil || ready {
		t.Fatalf("before status: ready=%t err=%v", ready, err)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.ReadyReplicas = *deployment.Spec.Replicas
	deployment.Status.UpdatedReplicas = *deployment.Spec.Replicas
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	if ready, message, err := component.EnsureResources(context.Background(), namespace, elasticConfig(), "identity.example.com", namespace, "neteye", owner()); err != nil || !ready {
		t.Fatalf("after status: ready=%t message=%q err=%v", ready, message, err)
	}
}

func TestEnsureResourcesCreatesOwnedCiliumPolicies(t *testing.T) {
	namespace := "neteye-tenant-shared"
	s := elasticScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(defaultPrerequisites(namespace)...).Build()
	config := elasticConfig()
	config.OTelCollector.ElasticsearchEndpoints = []string{"https://elastic.example.com", "https://logs.example.com:9243"}
	config.OTelCollector.OIDCIssuerURL = "https://issuer.example.com:8443/auth/realms/master"
	if _, _, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, config, "identity.example.com", namespace, "neteye", owner()); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []struct {
		name    string
		ingress bool
	}{{IngressPolicyName, true}, {EgressPolicyName, false}} {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"})
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: policy.name}, object); err != nil {
			t.Fatal(err)
		}
		if len(object.GetOwnerReferences()) != 1 || object.GetOwnerReferences()[0].Name != "platform" {
			t.Errorf("%s owner refs=%v", policy.name, object.GetOwnerReferences())
		}
		if policy.ingress {
			rules, _, _ := unstructured.NestedSlice(object.Object, "spec", "ingress")
			if rules[0].(map[string]any)["fromEntities"].([]any)[0] != "ingress" {
				t.Errorf("ingress rules=%#v", rules)
			}
		} else {
			rules, _, _ := unstructured.NestedSlice(object.Object, "spec", "egress")
			if len(rules) != 4 {
				t.Errorf("egress rules=%#v", rules)
			}
			labels := rules[0].(map[string]any)["toEndpoints"].([]any)[0].(map[string]any)["matchLabels"].(map[string]any)
			if labels["k8s:io.kubernetes.pod.namespace"] != "kube-system" || labels["k8s:k8s-app"] != "kube-dns" || len(labels) != 2 {
				t.Errorf("DNS endpoint labels=%#v", labels)
			}
		}
	}
}

func TestEnsureResourcesUsesConfiguredOIDCIssuer(t *testing.T) {
	s := elasticScheme(t)
	namespace := "neteye-tenant-shared"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(defaultPrerequisites(namespace)...).Build()
	config := elasticConfig()
	config.OTelCollector.OIDCIssuerURL = "https://issuer.example.com/auth/realms/custom"
	if _, _, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, config, "identity.example.com", namespace, "neteye", owner()); err != nil {
		t.Fatal(err)
	}
	variables := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: VariablesConfigMapName}, variables); err != nil {
		t.Fatal(err)
	}
	if variables.Data["OIDC_ISSUER"] != config.OTelCollector.OIDCIssuerURL {
		t.Errorf("OIDC_ISSUER = %q, want %q", variables.Data["OIDC_ISSUER"], config.OTelCollector.OIDCIssuerURL)
	}
}

func elasticScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}
func elasticConfig() neteye.NetEyeElasticStackSpec {
	return neteye.NetEyeElasticStackSpec{Enabled: true, OTelCollector: &neteye.NetEyeOtelCollectorSpec{ElasticsearchEndpoints: []string{"https://elastic.example.com:9200"}}}
}
func defaultPrerequisites(namespace string) []client.Object {
	return []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultAPIKeySecretName}, Data: map[string][]byte{DefaultAPIKeySecretKey: []byte("key")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultBasicAuthSecretName}, Data: map[string][]byte{"htpasswd": []byte("hash")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultRootCAConfigMapName}},
	}
}
func owner() metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", Controller: &controller}
}
