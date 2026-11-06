// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	testTenant   = "neteye-tenant-2bh46bfv"
	testHostname = "neteye.example.com"
	testRootCA   = "-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"
	testImage    = "registry.example.com/permissionsync@sha256:0000"
)

func firstTenantSpec() neteye.NetEyeTenantPermissionSyncSpec {
	return neteye.NetEyeTenantPermissionSyncSpec{
		Realm:    "master",
		Provider: &neteye.NetEyeTenantPermissionSyncProviderSpec{Endpoint: "https://node.example.com/neteye/api/v2/permission-provider/desired-state"},
		GLPI: &neteye.NetEyeTenantPermissionSyncGLPISpec{
			Endpoint:             "https://node.example.com/glpi/apirest.php",
			CredentialsSecretRef: neteye.NetEyeTenantPermissionSyncGLPICredentials{Name: "permissionsync-glpi-credentials"},
		},
		Targets:         []neteye.NetEyeTenantPermissionSyncTarget{{LogicalTarget: "glpi", Adapter: "glpi"}},
		PodExtraEnvVars: []neteye.NetEyeEnvVar{{Name: "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL", Value: "debug"}},
	}
}

func tenantWith(spec neteye.NetEyeTenantPermissionSyncSpec) *neteye.NetEyeTenant {
	return &neteye.NetEyeTenant{
		ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant, UID: "tenant-uid"},
		Spec:       neteye.NetEyeTenantSpec{DisplayName: "Default", PermissionSync: spec},
	}
}

func rootCA() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: RootCASecretName}, Data: map[string][]byte{corev1.TLSCertKey: []byte(testRootCA)}}
}

func glpiCredentials(userToken string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "permissionsync-glpi-credentials"}, Data: map[string][]byte{"app_token": []byte("app-secret"), "user_token": []byte(userToken)}}
}

func fakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := neteye.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func ensure(t *testing.T, c client.Client, tenant *neteye.NetEyeTenant) Outcome {
	t.Helper()
	outcome, err := Ensure(context.Background(), c, Input{Tenant: tenant, IdentityHostname: testHostname, Image: testImage})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return outcome
}

func renderedDocument(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	configMap := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: ConfigMapName}, configMap); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal([]byte(configMap.Data[ConfigFileName]), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func getDeployment(t *testing.T, c client.Client) *appsv1.Deployment {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: DeploymentName}, deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func TestEnsureDeploysTheFirstTenantWithoutSecretsInTheFile(t *testing.T) {
	c := fakeClient(t, rootCA(), glpiCredentials("user-secret"))
	if outcome := ensure(t, c, tenantWith(firstTenantSpec())); outcome.State != StateWaiting || outcome.Reason != "Deploying" {
		t.Fatalf("outcome = %#v, want the rollout to be awaited", outcome)
	}

	configMap := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: ConfigMapName}, configMap); err != nil {
		t.Fatal(err)
	}
	if text := configMap.Data[ConfigFileName]; strings.Contains(text, "app-secret") || strings.Contains(text, "user-secret") || strings.Contains(text, "token") {
		t.Errorf("the configuration file carries a credential or a token field:\n%s", text)
	}
	doc := renderedDocument(t, c)
	authentication := doc["authentication"].(map[string]any)
	if want := "https://" + testHostname + "/auth/realms/master"; authentication["issuer"] != want {
		t.Errorf("issuer = %v, want %s", authentication["issuer"], want)
	}
	if source := authentication["source"].(map[string]any); source["jwks_uri"] != "https://keycloak.neteyelocal/auth/realms/master/protocol/openid-connect/certs" {
		t.Errorf("source = %v", source)
	}
	if anchors := authentication["additional_trust_anchors_pem"]; !reflect.DeepEqual(anchors, []any{testRootCA}) {
		t.Errorf("trust anchors = %v, want the root CA", anchors)
	}
	if endpoint := doc["glpi"].(map[string]any)["endpoint"]; endpoint != "https://node.example.com/glpi/apirest.php" {
		t.Errorf("glpi endpoint = %v", endpoint)
	}
	if targets := doc["targets"]; !reflect.DeepEqual(targets, []any{map[string]any{"logical_target": "glpi", "adapter": "glpi"}}) {
		t.Errorf("targets = %v", targets)
	}

	container := getDeployment(t, c).Spec.Template.Spec.Containers[0]
	want := []corev1.EnvVar{
		{Name: "PERMISSIONSYNC_CONFIG_FILE", Value: "/etc/permissionsync/permissionsync.yaml"},
		secretEnvVar("PERMISSIONSYNC_GLPI__APP_TOKEN", "permissionsync-glpi-credentials", "app_token"),
		secretEnvVar("PERMISSIONSYNC_GLPI__USER_TOKEN", "permissionsync-glpi-credentials", "user_token"),
		{Name: "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL", Value: "debug"},
	}
	if !reflect.DeepEqual(container.Env, want) {
		t.Errorf("env = %#v", container.Env)
	}
	if links := getDeployment(t, c).Spec.Template.Spec.EnableServiceLinks; links == nil || *links {
		t.Error("Service links are enabled, so Kubernetes adds PERMISSIONSYNC_SERVICE_HOST and similar variables")
	}
	if container.Image != testImage {
		t.Errorf("image = %q", container.Image)
	}
	for _, object := range []client.Object{&corev1.ConfigMap{}, &corev1.Service{}, &appsv1.Deployment{}} {
		name := map[string]string{"*v1.ConfigMap": ConfigMapName, "*v1.Service": ServiceName, "*v1.Deployment": DeploymentName}[reflect.TypeOf(object).String()]
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, object); err != nil {
			t.Fatal(err)
		}
		if owners := object.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "NetEyeTenant" || owners[0].UID != "tenant-uid" {
			t.Errorf("%T owners = %#v, want the tenant", object, owners)
		}
	}
}

func TestEnsurePassesProviderAndGLPIVariablesOnlyWithTheirSection(t *testing.T) {
	variables := []neteye.NetEyeEnvVar{
		{Name: "PERMISSIONSYNC_GLPI__OPERATION_TIMEOUT_MILLISECONDS", Value: "3000"},
		{Name: "PERMISSIONSYNC_PROVIDER__GENERIC_REST__OPERATION_TIMEOUT_MILLISECONDS", Value: "3000"},
		{Name: "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL", Value: "debug"},
	}
	names := func(c client.Client) []string {
		var got []string
		for _, variable := range getDeployment(t, c).Spec.Template.Spec.Containers[0].Env {
			if variable.Value != "" {
				got = append(got, variable.Name)
			}
		}
		return got
	}
	c := fakeClient(t, rootCA())
	ensure(t, c, tenantWith(neteye.NetEyeTenantPermissionSyncSpec{PodExtraEnvVars: variables}))
	if got, want := names(c), []string{configFileEnvVar, "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("env without sections = %v, want %v", got, want)
	}
	spec := firstTenantSpec()
	spec.PodExtraEnvVars = variables
	c = fakeClient(t, rootCA(), glpiCredentials("token"))
	ensure(t, c, tenantWith(spec))
	if got, want := names(c), []string{configFileEnvVar, variables[0].Name, variables[1].Name, variables[2].Name}; !reflect.DeepEqual(got, want) {
		t.Errorf("env with both sections = %v, want %v", got, want)
	}
}

func TestEnsureWithoutAnySectionDeploysPermissionSyncWithZeroTargets(t *testing.T) {
	c := fakeClient(t, rootCA())
	ensure(t, c, tenantWith(neteye.NetEyeTenantPermissionSyncSpec{}))
	doc := renderedDocument(t, c)
	if _, present := doc["provider"]; present {
		t.Error("a provider section without a provider endpoint")
	}
	if _, present := doc["glpi"]; present {
		t.Error("a glpi section without a glpi endpoint")
	}
	if targets, present := doc["targets"]; !present || !reflect.DeepEqual(targets, []any{}) {
		t.Errorf("targets = %v, want an empty list", targets)
	}
	if issuer := doc["authentication"].(map[string]any)["issuer"]; issuer != "https://"+testHostname+"/auth/realms/"+testTenant {
		t.Errorf("issuer = %v, want the realm of the tenant", issuer)
	}
	deployment := getDeployment(t, c)
	if replicas := *deployment.Spec.Replicas; replicas != neteye.DefaultPermissionSyncReplicas {
		t.Errorf("replicas = %d", replicas)
	}
	if env := deployment.Spec.Template.Spec.Containers[0].Env; len(env) != 1 {
		t.Errorf("env = %#v, want only the configuration file", env)
	}
}

func TestEnsureWaitsForTheGLPICredentialsWithoutDeploying(t *testing.T) {
	for name, objects := range map[string][]client.Object{
		"no Secret":     {rootCA()},
		"no user token": {rootCA(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "permissionsync-glpi-credentials"}, Data: map[string][]byte{"app_token": []byte("a")}}},
		"no root CA":    {glpiCredentials("u")},
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeClient(t, objects...)
			outcome := ensure(t, c, tenantWith(firstTenantSpec()))
			if outcome.State != StateWaiting || !strings.HasPrefix(outcome.Reason, "WaitingFor") {
				t.Fatalf("outcome = %#v", outcome)
			}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: DeploymentName}, &appsv1.Deployment{}); err == nil {
				t.Error("the Deployment was created before its prerequisites exist")
			}
		})
	}
}

func TestEnsureRefusesAnInvalidConfiguration(t *testing.T) {
	spec := firstTenantSpec()
	spec.GLPI = nil
	c := fakeClient(t, rootCA())
	if outcome := ensure(t, c, tenantWith(spec)); outcome.State != StateInvalid {
		t.Fatalf("outcome = %#v, want an invalid configuration for a glpi target without glpi", outcome)
	}
}

func TestEnsureRollsOutNewPodsOnlyWhenTheInputsChange(t *testing.T) {
	c := fakeClient(t, rootCA(), glpiCredentials("first"))
	tenant := tenantWith(firstTenantSpec())
	ensure(t, c, tenant)
	first := getDeployment(t, c).Spec.Template.Annotations[ConfigurationHashAnnotation]
	ensure(t, c, tenant)
	if again := getDeployment(t, c).Spec.Template.Annotations[ConfigurationHashAnnotation]; again != first {
		t.Errorf("hash changed without a change: %s -> %s", first, again)
	}
	secret := glpiCredentials("second")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	ensure(t, c, tenant)
	if rotated := getDeployment(t, c).Spec.Template.Annotations[ConfigurationHashAnnotation]; rotated == first {
		t.Error("a new GLPI token did not change the Pod template, so the Pods keep the old token")
	}
	if strings.Contains(getDeployment(t, c).Spec.Template.Annotations[ConfigurationHashAnnotation], "second") {
		t.Error("the hash annotation shows a credential")
	}
}

// testCertificatePEM returns a self-signed CA certificate.
func testCertificatePEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Example CA"}, NotBefore: time.Unix(0, 0), NotAfter: time.Unix(0, 0).Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

var testCABundleRef = resources.CABundleRef{Name: "neteye-ca-bundle", Key: "ca-bundle.pem"}

func caBundle(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: keycloak.WorkloadNamespace, Name: testCABundleRef.Name}, Data: data}
}

// trustAnchors returns the trust anchors of each section that has them.
func trustAnchors(t *testing.T, c client.Client) map[string]any {
	t.Helper()
	doc := renderedDocument(t, c)
	provider := doc["provider"].(map[string]any)["generic_rest"].(map[string]any)
	return map[string]any{
		"authentication": doc["authentication"].(map[string]any)["additional_trust_anchors_pem"],
		"provider":       provider["additional_trust_anchors_pem"],
		"glpi":           doc["glpi"].(map[string]any)["additional_trust_anchors_pem"],
	}
}

func TestEnsureTrustsOnlyTheCertificatesOfTheCABundle(t *testing.T) {
	certificate := testCertificatePEM(t)
	bundle := "a comment\n" + certificate +
		"-----BEGIN X509 CRL-----\nAAAA\n-----END X509 CRL-----\n" +
		"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	c := fakeClient(t, rootCA(), glpiCredentials("token"), caBundle(map[string]string{testCABundleRef.Key: bundle}))
	in := Input{Tenant: tenantWith(firstTenantSpec()), IdentityHostname: testHostname, Image: testImage, CABundle: testCABundleRef}
	if _, err := Ensure(context.Background(), c, in); err != nil {
		t.Fatal(err)
	}
	for section, got := range trustAnchors(t, c) {
		if want := []any{testRootCA, certificate}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s trust anchors = %q, want the root CA and the valid certificate of the bundle only", section, got)
		}
	}
}

func TestEnsureTrustsOnlyTheRootCAWithoutAUsableCABundle(t *testing.T) {
	for name, objects := range map[string][]client.Object{
		"no ConfigMap":   nil,
		"no key":         {caBundle(map[string]string{"other.pem": testCertificatePEM(t)})},
		"no valid block": {caBundle(map[string]string{testCABundleRef.Key: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"})},
		"not PEM at all": {caBundle(map[string]string{testCABundleRef.Key: "not a certificate"})},
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeClient(t, append([]client.Object{rootCA(), glpiCredentials("token")}, objects...)...)
			in := Input{Tenant: tenantWith(firstTenantSpec()), IdentityHostname: testHostname, Image: testImage, CABundle: testCABundleRef}
			if _, err := Ensure(context.Background(), c, in); err != nil {
				t.Fatal(err)
			}
			for section, got := range trustAnchors(t, c) {
				if want := []any{testRootCA}; !reflect.DeepEqual(got, want) {
					t.Errorf("%s trust anchors = %q, want the root CA only", section, got)
				}
			}
		})
	}
}

func TestEnsureReportsReadyAfterTheRollout(t *testing.T) {
	c := fakeClient(t, rootCA())
	tenant := tenantWith(neteye.NetEyeTenantPermissionSyncSpec{Realm: "master", Replicas: ptr.To(int32(1))})
	ensure(t, c, tenant)
	deployment := getDeployment(t, c)
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	if outcome := ensure(t, c, tenant); outcome.State != StateReady || !strings.Contains(outcome.Message, `"master"`) {
		t.Errorf("outcome = %#v, want ready with the effective realm", outcome)
	}
}

func TestNetworkPolicyAllowsNoIngressAndOnlyTheNeededEgress(t *testing.T) {
	c := fakeClient(t, rootCA(), glpiCredentials("u"))
	spec := firstTenantSpec()
	spec.Provider.Endpoint = "https://node.example.com:8443/provider"
	ensure(t, c, tenantWith(spec))
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(resources.NetworkPolicyGVK())
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: NetworkPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	policySpec := policy.Object["spec"].(map[string]any)
	if _, present := policySpec["ingress"]; present {
		t.Errorf("ingress rules = %v, want none", policySpec["ingress"])
	}
	if types := policySpec["policyTypes"]; !reflect.DeepEqual(types, []any{"Ingress", "Egress"}) {
		t.Errorf("policy types = %v", types)
	}
	egress := policySpec["egress"].([]any)
	if len(egress) != 3 {
		t.Fatalf("egress = %v, want DNS, Keycloak and the endpoint ports", egress)
	}
	dns := egress[0].(map[string]any)
	if _, present := dns["to"]; present || len(dns["ports"].([]any)) != 4 {
		t.Errorf("DNS rule = %v, want UDP and TCP 53 and 5353 to any destination", dns)
	}
	keycloakRule := egress[1].(map[string]any)
	peer := keycloakRule["to"].([]any)[0].(map[string]any)
	if namespace := peer["namespaceSelector"].(map[string]any)["matchLabels"].(map[string]any)["kubernetes.io/metadata.name"]; namespace != keycloak.WorkloadNamespace {
		t.Errorf("Keycloak rule namespace = %v, want %s", namespace, keycloak.WorkloadNamespace)
	}
	if pods := peer["podSelector"].(map[string]any)["matchLabels"].(map[string]any); pods["app"] != "keycloak" || pods["app.kubernetes.io/instance"] != keycloak.InstanceName {
		t.Errorf("Keycloak rule Pods = %v, want the Keycloak Pods", pods)
	}
	if ports := keycloakRule["ports"]; !reflect.DeepEqual(ports, []any{map[string]any{"protocol": "TCP", "port": keycloak.HTTPPort}}) {
		t.Errorf("Keycloak rule ports = %v, want TCP %d", ports, keycloak.HTTPPort)
	}
	if ports := egress[2].(map[string]any)["ports"]; !reflect.DeepEqual(ports, []any{map[string]any{"protocol": "TCP", "port": int64(443)}, map[string]any{"protocol": "TCP", "port": int64(8443)}}) {
		t.Errorf("endpoint ports = %v, want each port once", ports)
	}
	if owners := policy.GetOwnerReferences(); len(owners) != 1 || owners[0].UID != "tenant-uid" {
		t.Errorf("owners = %#v", owners)
	}

	rules := networkPolicySpec(nil)["egress"].([]any)
	if len(rules) != 3 || !reflect.DeepEqual(rules[2].(map[string]any)["ports"], []any{map[string]any{"protocol": "TCP", "port": int64(443)}}) {
		t.Errorf("egress without endpoints = %v, want DNS, the Keycloak Pods and the Gateway on 443", rules)
	}
}

// goldenFirstTenantConfiguration is the configuration file of the first tenant,
// written from the configuration contract of PermissionSync rather than from
// the renderer: every section, key and type that PermissionSync expects, and
// no token field, because the tokens come from the environment.
const goldenFirstTenantConfiguration = `
listener:
  address: 0.0.0.0
  port: 8443
request:
  overall_deadline_milliseconds: 10000
  inbound_admission_limit: 64
  synchronization_capacity: 16
shutdown:
  grace_milliseconds: 20000
authentication:
  issuer: https://neteye.example.com/auth/realms/master
  audience: permissionsync
  algorithms: [RS256]
  source:
    jwks_uri: https://keycloak.neteyelocal/auth/realms/master/protocol/openid-connect/certs
  cache:
    freshness_milliseconds: 300000
    stale_if_error_milliseconds: 600000
  metadata_operation_timeout_milliseconds: 3000
  clock_skew_milliseconds: 30000
  additional_trust_anchors_pem: ["-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"]
observability:
  log_level: info
provider:
  generic_rest:
    endpoint: https://node.example.com/neteye/api/v2/permission-provider/desired-state
    operation_timeout_milliseconds: 5000
    additional_trust_anchors_pem: ["-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"]
glpi:
  endpoint: https://node.example.com/glpi/apirest.php
  operation_timeout_milliseconds: 5000
  additional_trust_anchors_pem: ["-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"]
targets:
  - logical_target: glpi
    adapter: glpi
`

func TestRenderedConfigurationMatchesTheContractOfPermissionSync(t *testing.T) {
	spec := firstTenantSpec()
	rendered, err := renderConfiguration(&spec, testHostname, "master", []string{testRootCA})
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := yaml.Unmarshal(rendered, &got); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(goldenFirstTenantConfiguration), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rendered configuration:\n%s\nwant the contract:\n%s", rendered, goldenFirstTenantConfiguration)
	}
}
