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
	if err != nil || !ready {
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
	config.Replicas = 3
	config.APIKeySecret = &neteye.NetEyeSecretKeySelector{Name: "debug-api-key", Key: "debug_key"}
	config.BasicAuthSecretName = "debug-basicauth"
	config.RootCAConfigMapName = "debug-root-ca"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: config.APIKeySecret.Name}, Data: map[string][]byte{config.APIKeySecret.Key: []byte("key")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: config.BasicAuthSecretName}, Data: map[string][]byte{"htpasswd": []byte("hash")}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: config.RootCAConfigMapName}},
	).Build()
	if ready, message, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, config, "identity.example.com", namespace, "neteye", owner()); err != nil || !ready {
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

func TestEnsureResourcesUsesConfiguredOIDCIssuer(t *testing.T) {
	s := elasticScheme(t)
	namespace := "neteye-tenant-shared"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(defaultPrerequisites(namespace)...).Build()
	config := elasticConfig()
	config.OIDCIssuerURL = "https://issuer.example.com/auth/realms/custom"
	if _, _, err := NewComponent(c, logr.Discard()).EnsureResources(context.Background(), namespace, config, "identity.example.com", namespace, "neteye", owner()); err != nil {
		t.Fatal(err)
	}
	variables := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: VariablesConfigMapName}, variables); err != nil {
		t.Fatal(err)
	}
	if variables.Data["OIDC_ISSUER"] != config.OIDCIssuerURL {
		t.Errorf("OIDC_ISSUER = %q, want %q", variables.Data["OIDC_ISSUER"], config.OIDCIssuerURL)
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
	return neteye.NetEyeElasticStackSpec{Enabled: true, ElasticsearchEndpoints: []string{"https://elastic.example.com:9200"}}
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
