// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/elasticstack"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

var (
	certificateGVK   = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
	issuerGVK        = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}
	gatewayGVK       = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}
	httpRouteGVK     = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
	grpcRouteGVK     = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"}
	networkPolicyGVK = schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
	ciliumPolicyGVK  = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}
)

// startEnvtest boots a real API server via envtest. It skips the test when the
// envtest binaries are not installed (run "make envtest" / set KUBEBUILDER_ASSETS)
// so that a plain "go test ./..." stays green without them.
func startEnvtest(t *testing.T) (client.Client, *runtime.Scheme) {
	t.Helper()

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := neteye.AddToScheme(s); err != nil {
		t.Fatalf("neteye scheme: %v", err)
	}

	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				filepath.Join("testdata", "crds"),
				filepath.Join("..", "config", "crd", "bases", "neteye.cloud_neteyes.yaml"),
				filepath.Join("..", "config", "crd", "bases", "neteye.cloud_keycloakclients.yaml"),
				filepath.Join("..", "config", "crd", "bases", "neteye.cloud_keycloakusers.yaml"),
			},
		},
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("envtest binaries not available (run 'make envtest' / set KUBEBUILDER_ASSETS)")
	}
	cfg, err := env.Start()
	t.Cleanup(func() { _ = env.Stop() })
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c, s
}

func TestReconcileElasticStackDisabledDoesNotCreateCollector(t *testing.T) {
	c, s, ctx, ne, r := readyElasticStackTestPlatform(t, nil)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}); err != nil {
		t.Fatalf("reconcile ready platform: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: keycloak.WorkloadNamespace, Name: elasticstack.DeploymentName}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Elastic Stack feature module deployment exists or lookup failed while disabled: %v", err)
	}
	_ = s
}

func TestReconcileElasticStackEnabledCreatesCollector(t *testing.T) {
	config := &neteye.NetEyeElasticStackSpec{
		Enabled:       true,
		OTelCollector: &neteye.NetEyeOtelCollectorSpec{Replicas: 3, ElasticsearchEndpoints: []string{"https://elasticsearch.example.com:9200"}},
	}
	c, _, ctx, ne, r := readyElasticStackTestPlatform(t, config)
	prerequisites := []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: elasticstack.DefaultAPIKeySecretName}, Data: map[string][]byte{elasticstack.DefaultAPIKeySecretKey: []byte("key")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: elasticstack.DefaultBasicAuthSecretName}, Data: map[string][]byte{"htpasswd": []byte("user:hash")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: elasticstack.DefaultRootCASecretName}, Data: map[string][]byte{"tls.crt": []byte("certificate")}},
	}
	for _, prerequisite := range prerequisites {
		if err := c.Create(ctx, prerequisite); err != nil {
			t.Fatalf("create prerequisite %s: %v", prerequisite.GetName(), err)
		}
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}); err != nil {
		t.Fatalf("reconcile elastic stack: %v", err)
	}
	for _, resource := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, elasticstack.ConfigMapName}, {schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, elasticstack.VariablesConfigMapName}, {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, elasticstack.DeploymentName}, {schema.GroupVersionKind{Version: "v1", Kind: "Service"}, elasticstack.ServiceName}, {grpcRouteGVK, elasticstack.GRPCRouteName}, {httpRouteGVK, elasticstack.HTTPRouteName}, {certificateGVK, elasticstack.GRPCTLSCertName}, {certificateGVK, elasticstack.CrossTenantTLSCertName}, {ciliumPolicyGVK, elasticstack.IngressPolicyName}, {ciliumPolicyGVK, elasticstack.EgressPolicyName}} {
		requireExists(ctx, t, c, resource.gvk, keycloak.WorkloadNamespace, resource.name)
	}
	variables := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: keycloak.WorkloadNamespace, Name: elasticstack.VariablesConfigMapName}, variables); err != nil {
		t.Fatal(err)
	}
	if variables.Data["ELASTICSEARCH_ENDPOINTS"] != `["https://elasticsearch.example.com:9200"]` || variables.Data["OIDC_ISSUER"] != "https://keycloak.example.com/auth/realms/master" {
		t.Errorf("variables = %#v", variables.Data)
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: keycloak.WorkloadNamespace, Name: elasticstack.DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 3 {
		t.Errorf("replicas = %v, want 3", deployment.Spec.Replicas)
	}
	current := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.ServicesStatus.ElasticStack.OTelCollector.Status != neteye.ServiceStateNotReady {
		t.Errorf("Elastic Stack collector status before deployment readiness = %q", current.Status.ServicesStatus.ElasticStack.OTelCollector.Status)
	}
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.Replicas = *deployment.Spec.Replicas
	deployment.Status.ReadyReplicas = *deployment.Spec.Replicas
	deployment.Status.UpdatedReplicas = *deployment.Spec.Replicas
	if err := c.Status().Update(ctx, deployment); err != nil {
		t.Fatalf("mark collector deployment ready: %v", err)
	}
	markReady(ctx, t, c, requireExists(ctx, t, c, certificateGVK, keycloak.WorkloadNamespace, elasticstack.GRPCTLSCertName))
	markReady(ctx, t, c, requireExists(ctx, t, c, certificateGVK, keycloak.WorkloadNamespace, elasticstack.CrossTenantTLSCertName))
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}); err != nil {
		t.Fatalf("reconcile ready collector deployment: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.ServicesStatus.ElasticStack.OTelCollector.Status != neteye.ServiceStateReady {
		t.Errorf("Elastic Stack collector status after deployment readiness = %q", current.Status.ServicesStatus.ElasticStack.OTelCollector.Status)
	}
	for _, route := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{{grpcRouteGVK, elasticstack.GRPCRouteName}, {httpRouteGVK, elasticstack.HTTPRouteName}} {
		object := requireExists(ctx, t, c, route.gvk, keycloak.WorkloadNamespace, route.name)
		parents, _, _ := unstructured.NestedSlice(object.Object, "spec", "parentRefs")
		if parents[0].(map[string]any)["namespace"] != keycloak.WorkloadNamespace {
			t.Errorf("%s parent refs = %#v", route.name, parents)
		}
	}
	for _, prerequisite := range prerequisites {
		current := prerequisite.DeepCopyObject().(client.Object)
		if err := c.Get(ctx, client.ObjectKeyFromObject(prerequisite), current); err != nil {
			t.Fatal(err)
		}
		if len(current.GetOwnerReferences()) != 0 {
			t.Errorf("prerequisite %s owner refs = %#v", current.GetName(), current.GetOwnerReferences())
		}
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.ElasticStack.Enabled = false
	if err := c.Update(ctx, current); err != nil {
		t.Fatalf("disable elastic stack: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}); err != nil {
		t.Fatalf("reconcile disabled elastic stack: %v", err)
	}
	for _, resource := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, elasticstack.ConfigMapName}, {schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, elasticstack.VariablesConfigMapName}, {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, elasticstack.DeploymentName}, {schema.GroupVersionKind{Version: "v1", Kind: "Service"}, elasticstack.ServiceName}, {grpcRouteGVK, elasticstack.GRPCRouteName}, {httpRouteGVK, elasticstack.HTTPRouteName}, {certificateGVK, elasticstack.GRPCTLSCertName}, {certificateGVK, elasticstack.CrossTenantTLSCertName}, {ciliumPolicyGVK, elasticstack.IngressPolicyName}, {ciliumPolicyGVK, elasticstack.EgressPolicyName}} {
		object := newUnstructured(resource.gvk, keycloak.WorkloadNamespace, resource.name)
		if err := c.Get(ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s still exists or lookup failed: %v", resource.gvk.Kind, resource.name, err)
		}
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.ElasticStack.Enabled = true
	if err := c.Update(ctx, current); err != nil {
		t.Fatalf("re-enable elastic stack: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}); err != nil {
		t.Fatalf("reconcile re-enabled elastic stack: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.ElasticStack = nil
	if err := c.Update(ctx, current); err != nil {
		t.Fatalf("remove elastic stack block: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}); err != nil {
		t.Fatalf("reconcile removed elastic stack block: %v", err)
	}
	for _, resource := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, elasticstack.ConfigMapName}, {schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, elasticstack.VariablesConfigMapName}, {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, elasticstack.DeploymentName}, {schema.GroupVersionKind{Version: "v1", Kind: "Service"}, elasticstack.ServiceName}, {grpcRouteGVK, elasticstack.GRPCRouteName}, {httpRouteGVK, elasticstack.HTTPRouteName}, {certificateGVK, elasticstack.GRPCTLSCertName}, {certificateGVK, elasticstack.CrossTenantTLSCertName}, {ciliumPolicyGVK, elasticstack.IngressPolicyName}, {ciliumPolicyGVK, elasticstack.EgressPolicyName}} {
		object := newUnstructured(resource.gvk, keycloak.WorkloadNamespace, resource.name)
		if err := c.Get(ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s still exists after block removal or lookup failed: %v", resource.gvk.Kind, resource.name, err)
		}
	}
}

func readyElasticStackTestPlatform(t *testing.T, elasticConfig *neteye.NetEyeElasticStackSpec) (client.Client, *runtime.Scheme, context.Context, *neteye.NetEye, *NetEyeReconciler) {
	t.Helper()
	c, s := startEnvtest(t)
	ctx := context.Background()
	namespace := keycloak.WorkloadNamespace
	for _, name := range []string{namespace} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatalf("create namespace: %v", err)
		}
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, namespace, "internal-issuer")); err != nil {
		t.Fatal(err)
	}
	ne := &neteye.NetEye{ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: namespace}, Spec: neteye.NetEyeSpec{Version: neteye.CurrentNetEyeVersion, InternalCertificateIssuerRef: "internal-issuer", Gateway: neteye.NetEyeGatewaySpec{Name: "neteye", ClassName: "cilium"}, Identity: neteye.NetEyeIdentitySpec{Hostname: "keycloak.example.com", DBConnection: neteye.NetEyeDBConnectionSpec{Host: "mariadb.example.com", DBName: "keycloak", UsernameSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "username"}, PasswordSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "password"}}}, ElasticStack: elasticConfig}}
	if err := c.Create(ctx, ne); err != nil {
		t.Fatal(err)
	}
	r := &NetEyeReconciler{Client: c, Log: logr.Discard(), Scheme: s, KeycloakComponent: keycloak.NewComponent(c, logr.Discard()), ElasticStackReconciler: elasticstack.NewReconciler(elasticstack.NewComponent(c, logr.Discard()))}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	markReady(ctx, t, c, requireExists(ctx, t, c, certificateGVK, namespace, keycloak.TLSCertificateName))
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	kc := requireExists(ctx, t, c, schema.GroupVersionKind{Group: "k8s.keycloak.org", Version: "v2beta1", Kind: "Keycloak"}, namespace, keycloak.InstanceName)
	if err := unstructured.SetNestedField(kc.Object, kc.GetGeneration(), "status", "observedGeneration"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(kc.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(ctx, kc); err != nil {
		t.Fatal(err)
	}
	return c, s, ctx, ne, r
}

func markReady(ctx context.Context, t *testing.T, c client.Client, object *unstructured.Unstructured) {
	t.Helper()
	if err := unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(ctx, object); err != nil {
		t.Fatal(err)
	}
}

func newUnstructured(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

func requireExists(ctx context.Context, t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, u); err != nil {
		t.Fatalf("expected %s %s/%s to exist: %v", gvk.Kind, namespace, name, err)
	}
	return u
}

func TestReconcileBaseResourcesAgainstAPIServer(t *testing.T) {
	c, s := startEnvtest(t)
	ctx := context.Background()
	ns := keycloak.WorkloadNamespace

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if err := c.Create(ctx, newUnstructured(issuerGVK, ns, "internal-issuer")); err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	ne := &neteye.NetEye{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: ns},
		Spec: neteye.NetEyeSpec{
			Version:                      neteye.CurrentNetEyeVersion,
			InternalCertificateIssuerRef: "internal-issuer",
			Gateway: neteye.NetEyeGatewaySpec{
				Name:      "neteye-gw",
				ClassName: "cilium",
			},
			Identity: neteye.NetEyeIdentitySpec{
				Hostname: "keycloak.example.com",
				DBConnection: neteye.NetEyeDBConnectionSpec{
					Host:           "mariadb.example.com",
					DBName:         "keycloak",
					UsernameSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "username"},
					PasswordSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "password"},
				},
			},
		},
	}
	if err := c.Create(ctx, ne); err != nil {
		t.Fatalf("create neteye: %v", err)
	}

	r := &NetEyeReconciler{
		Client:            c,
		Log:               logr.Discard(),
		Scheme:            s,
		KeycloakComponent: keycloak.NewComponent(c, logr.Discard()),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "platform"}}

	legacyDefaultDeny := newUnstructured(networkPolicyGVK, keycloak.WorkloadNamespace, resources.DefaultDenyPolicyName)
	legacyDefaultDeny.Object["spec"] = map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Ingress"},
	}
	if err := c.Create(ctx, legacyDefaultDeny); err != nil {
		t.Fatalf("create legacy native default deny: %v", err)
	}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter != DefaultWaitForProgressingRequeueAfter {
		t.Errorf("requeueAfter = %v, want %v", res.RequeueAfter, DefaultWaitForProgressingRequeueAfter)
	}

	gateway := requireExists(ctx, t, c, gatewayGVK, ns, "neteye-gw")
	assertNetEyeOwner(t, gateway)
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	if len(listeners) != 2 {
		t.Fatalf("gateway listeners = %d, want 2 (http + keycloak)", len(listeners))
	}
	for _, listener := range listeners {
		namespaces, _, _ := unstructured.NestedMap(listener.(map[string]any), "allowedRoutes", "namespaces")
		if namespaces["from"] != "Same" {
			t.Errorf("allowedRoutes.namespaces.from = %v, want Same", namespaces["from"])
		}
	}
	keycloakListener := gatewayListenerByName(t, listeners, keycloak.GatewayListenerName)
	if keycloakListener["hostname"] != keycloak.RouteHostname {
		t.Errorf("keycloak listener hostname = %v, want %q", keycloakListener["hostname"], keycloak.RouteHostname)
	}
	certRefs, _, _ := unstructured.NestedSlice(keycloakListener, "tls", "certificateRefs")
	if len(certRefs) != 1 || certRefs[0].(map[string]any)["name"] != keycloak.TLSSecretName {
		t.Errorf("keycloak listener certificateRefs = %v, want secret %q", certRefs, keycloak.TLSSecretName)
	}
	requireExists(ctx, t, c, httpRouteGVK, ns, resources.HTTPToHTTPSRedirectRouteName)

	legacyLookup := newUnstructured(networkPolicyGVK, keycloak.WorkloadNamespace, resources.DefaultDenyPolicyName)
	if err := c.Get(ctx, client.ObjectKeyFromObject(legacyLookup), legacyLookup); !apierrors.IsNotFound(err) {
		t.Errorf("legacy native default deny still exists or lookup failed: %v", err)
	}
	defaultDeny := requireExists(ctx, t, c, ciliumPolicyGVK, keycloak.WorkloadNamespace, resources.DefaultDenyPolicyName)
	assertNetEyeOwner(t, defaultDeny)
	assertNetEyeOwner(t, requireExists(ctx, t, c, schema.GroupVersionKind{Group: "coordination.k8s.io", Version: "v1", Kind: "Lease"}, keycloak.WorkloadNamespace, clusterAuthorityLeaseName))
	if endpointSelector, _, _ := unstructured.NestedMap(defaultDeny.Object, "spec", "endpointSelector"); len(endpointSelector) != 0 {
		t.Errorf("default deny endpoint selector = %v, want empty", endpointSelector)
	}
	enableDefaultDeny, _, _ := unstructured.NestedMap(defaultDeny.Object, "spec", "enableDefaultDeny")
	if enableDefaultDeny["ingress"] != true || enableDefaultDeny["egress"] != true {
		t.Errorf("default deny settings = %#v, want ingress and egress enabled", enableDefaultDeny)
	}
	ingress, found, err := unstructured.NestedSlice(defaultDeny.Object, "spec", "ingress")
	if err != nil || !found || !reflect.DeepEqual(ingress, []any{map[string]any{"fromEntities": []any{"none"}}}) {
		t.Errorf("default deny ingress = %#v, want non-matching none entity", ingress)
	}
	egress, found, err := unstructured.NestedSlice(defaultDeny.Object, "spec", "egress")
	if err != nil || !found || !reflect.DeepEqual(egress, []any{map[string]any{"toEntities": []any{"none"}}}) {
		t.Errorf("default deny egress = %#v, want non-matching none entity", egress)
	}

	got := &neteye.NetEye{}
	if err := c.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatalf("get neteye: %v", err)
	}
	if got.Status.Phase != neteye.PhaseNotReady {
		t.Errorf("phase = %q, want %q", got.Status.Phase, neteye.PhaseNotReady)
	}

	keycloakCertificate := requireExists(ctx, t, c, certificateGVK, keycloak.WorkloadNamespace, keycloak.TLSCertificateName)
	assertNetEyeOwner(t, keycloakCertificate)
	if dnsNames, _, _ := unstructured.NestedStringSlice(keycloakCertificate.Object, "spec", "dnsNames"); len(dnsNames) != 1 || dnsNames[0] != keycloak.RouteHostname {
		t.Errorf("keycloak certificate dnsNames = %v, want [%s]", dnsNames, keycloak.RouteHostname)
	}
	if err := unstructured.SetNestedSlice(keycloakCertificate.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(ctx, keycloakCertificate); err != nil {
		t.Fatalf("update shared Keycloak certificate status: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	route := requireExists(ctx, t, c, httpRouteGVK, keycloak.WorkloadNamespace, keycloak.HTTPRouteName)
	assertNetEyeOwner(t, route)
	assertNetEyeOwner(t, requireExists(ctx, t, c, schema.GroupVersionKind{Group: "k8s.keycloak.org", Version: "v2beta1", Kind: "Keycloak"}, keycloak.WorkloadNamespace, keycloak.InstanceName))
	assertNetEyeOwner(t, requireExists(ctx, t, c, networkPolicyGVK, keycloak.WorkloadNamespace, keycloak.EgressPolicyName))
	assertNetEyeOwner(t, requireExists(ctx, t, c, networkPolicyGVK, keycloak.WorkloadNamespace, keycloak.IngressPolicyName))
	assertNetEyeOwner(t, requireExists(ctx, t, c, ciliumPolicyGVK, keycloak.WorkloadNamespace, keycloak.HostPolicyName))
	parentRefs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	parentRef := parentRefs[0].(map[string]any)
	if parentRef["namespace"] != keycloak.WorkloadNamespace {
		t.Errorf("route parent namespace = %v, want %q", parentRef["namespace"], keycloak.WorkloadNamespace)
	}
	if parentRef["sectionName"] != keycloak.GatewayListenerName {
		t.Errorf("route sectionName = %v, want %q", parentRef["sectionName"], keycloak.GatewayListenerName)
	}
	hostnames, _, _ := unstructured.NestedSlice(route.Object, "spec", "hostnames")
	if len(hostnames) != 1 || hostnames[0] != keycloak.RouteHostname {
		t.Errorf("route hostnames = %v, want [%s]", hostnames, keycloak.RouteHostname)
	}
}

func assertNetEyeOwner(t *testing.T, object *unstructured.Unstructured) {
	t.Helper()
	owners := object.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != "platform" || owners[0].Kind != "NetEye" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Errorf("%s %s/%s owner references = %v, want NetEye/platform controller owner", object.GetKind(), object.GetNamespace(), object.GetName(), owners)
	}
}

func gatewayListenerByName(t *testing.T, listeners []any, name string) map[string]any {
	t.Helper()
	for _, listener := range listeners {
		listenerSpec := listener.(map[string]any)
		if listenerSpec["name"] == name {
			return listenerSpec
		}
	}
	t.Fatalf("gateway listener %q not found", name)
	return nil
}
