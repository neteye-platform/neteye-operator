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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

func classisServerTenant(mode neteye.TenantDatabaseType) *neteye.NetEyeTenant {
	tenant := tenantResource()
	tenant.Spec.Database = &neteye.TenantDatabaseSpec{Type: mode}
	tenant.Spec.ClassisServer = &neteye.NetEyeClassisServerSpec{
		Enabled:              true,
		Image:                "ghcr.io/neteye-platform/classis-server:1.0.0",
		Blobs:                neteye.ClassisServerBlobsSpec{Endpoint: "https://s3.example.com", Bucket: "classis", Region: "us-east-1", Prefix: "tenant/", CredentialsSecretName: "s3"},
		AdminUsersSecretName: "admin",
		LogLevel:             "info",
		Network:              neteye.ClassisServerNetworkSpec{AdminIngressCIDRs: []string{"10.0.1.0/24"}},
	}
	if mode == neteye.TenantDatabaseExternal {
		tenant.Spec.Database.Hostname = "db.example.com"
		tenant.Spec.ClassisServer.Database = &neteye.ClassisServerDatabaseSpec{External: &neteye.ClassisServerExternalDatabaseSpec{DBName: "classis", User: "classis", CredentialsSecretName: "db"}}
	}
	return tenant
}

// installedCloudNativePG is the ClusterExtension of the CloudNativePG operator
// once OLM installed it.
func installedCloudNativePG() *unstructured.Unstructured {
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"})
	extension.SetName("cloudnative-pg")
	_ = unstructured.SetNestedSlice(extension.Object, []any{map[string]any{"type": "Installed", "status": "True"}}, "status", "conditions")
	return extension
}

// cnpgSystemNamespaceObject is the namespace of the CloudNativePG operator as
// neteye-setup creates it: the operator creates none.
func cnpgSystemNamespaceObject() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cnpg-system"}}
}

func classisServerSecrets(names ...string) []client.Object {
	objects := make([]client.Object, 0, len(names))
	for _, name := range names {
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: name}})
	}
	return objects
}

func TestClassisServerDeploymentIsHardenedAndStateless(t *testing.T) {
	d := classisServerDeployment(classisServerTenant(neteye.TenantDatabaseLocal), "sum", "secrets")
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *d.Spec.Replicas != 1 {
		t.Errorf("strategy = %v replicas = %d, want Recreate with one replica", d.Spec.Strategy.Type, *d.Spec.Replicas)
	}
	pod := d.Spec.Template.Spec
	if *pod.SecurityContext.RunAsUser != 65532 || !*pod.SecurityContext.RunAsNonRoot || *pod.SecurityContext.FSGroup != 65532 {
		t.Errorf("pod security context = %#v", pod.SecurityContext)
	}
	container := pod.Containers[0]
	if !*container.SecurityContext.ReadOnlyRootFilesystem || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("container security context = %#v", container.SecurityContext)
	}
	for probe, path := range map[*corev1.Probe]string{container.LivenessProbe: "/livez", container.ReadinessProbe: "/readyz", container.StartupProbe: "/readyz"} {
		if probe.HTTPGet == nil || probe.HTTPGet.Path != path || probe.HTTPGet.Scheme != corev1.URISchemeHTTP || probe.HTTPGet.Port.StrVal != "admin" {
			t.Errorf("probe = %v, want HTTP %s on the admin port", probe.HTTPGet, path)
		}
	}
	for _, port := range container.Ports {
		if port.Name == "health" {
			t.Error("the server has no separate health port")
		}
	}
	for _, volume := range pod.Volumes {
		if volume.PersistentVolumeClaim != nil {
			t.Errorf("volume %q is a PVC, the server must keep nothing on disk", volume.Name)
		}
		if volume.Secret != nil && *volume.Secret.DefaultMode != 0o440 {
			t.Errorf("secret volume %q mode = %o, want 440", volume.Name, *volume.Secret.DefaultMode)
		}
	}
}

// The Server reads its environment under CLASSIS_SERVER__ since the product
// was renamed; the old FLEET_SERVER__ prefix is ignored by it.
func TestClassisServerDeploymentUsesTheClassisNames(t *testing.T) {
	d := classisServerDeployment(classisServerTenant(neteye.TenantDatabaseLocal), "sum", "secrets")
	container := d.Spec.Template.Spec.Containers[0]
	var accessKey bool
	for _, env := range container.Env {
		if strings.HasPrefix(env.Name, "FLEET_SERVER") {
			t.Errorf("environment variable %q uses the old prefix", env.Name)
		}
		if env.Name == "CLASSIS_SERVER__BLOBS__S3__ACCESS_KEY_ID" {
			accessKey = true
		}
	}
	if !accessKey {
		t.Errorf("no CLASSIS_SERVER__BLOBS__S3__ACCESS_KEY_ID in %v", container.Env)
	}
	if len(container.Args) != 2 || container.Args[0] != "--config" || container.Args[1] != "/etc/classis/config/server.toml" {
		t.Errorf("args = %v, want --config on the mounted server.toml", container.Args)
	}
	for _, name := range []string{string(classisServerComponentID), string(classisServerDatabaseComponentID), classisServerConfigVersionAnnotation, classisServerSecretsVersionsAnnotation} {
		if strings.Contains(name, "fleet") {
			t.Errorf("%q still carries the old product name", name)
		}
	}
}

func TestClassisServerConfigNamesTheFilesOfTheTwoDatabaseModes(t *testing.T) {
	internal := classisServerConfig(classisServerTenant(neteye.TenantDatabaseLocal))
	for _, want := range []string{`host = "classis-server-db-rw"`, `store = "postgres"`, `store = "s3"`, `tls = true`, `endpoint = "https://s3.example.com"`, `listen = "0.0.0.0:4321"`, "[log]\nformat = \"json\""} {
		if !strings.Contains(internal, want) {
			t.Errorf("internal config lacks %q:\n%s", want, internal)
		}
	}
	if strings.Contains(internal, "[health]") {
		t.Errorf("config has a [health] section:\n%s", internal)
	}
	if external := classisServerConfig(classisServerTenant(neteye.TenantDatabaseExternal)); !strings.Contains(external, `host = "db.example.com"`) || !strings.Contains(external, "port = 5432") {
		t.Errorf("external config:\n%s", external)
	}
}

func TestInternalClassisServerCreatesTheDatabaseAndTheServer(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseLocal)
	objects := append([]client.Object{plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant, installedCloudNativePG()}, classisServerSecrets("admin", "s3", "classis-server-db-app", "classis-server-db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	ctx := context.Background()

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerDatabaseName}, cluster); err != nil {
		t.Fatalf("CloudNativePG cluster: %v", err)
	}
	if instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); instances != 2 {
		t.Errorf("instances = %d, want 2", instances)
	}
	if image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName"); !strings.HasSuffix(image, "postgresql:18") {
		t.Errorf("imageName = %q, want PostgreSQL 18", image)
	}
	// The database is not ready yet, so the server waits for it.
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err == nil {
		t.Error("the Deployment exists before the database is ready")
	}
	status := getTenant(t, c, testTenant, testTenant).Status.Components
	if status[string(classisServerDatabaseComponentID)].Status != neteye.ComponentStateProgressing || status[string(classisServerComponentID)].Status != neteye.ComponentStateBlocked {
		t.Errorf("components = %#v", status)
	}

	// Once the cluster is ready, the server is created.
	_ = unstructured.SetNestedSlice(cluster.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	if err := c.Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	for _, object := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &corev1.ConfigMap{}} {
		if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, object); err != nil {
			t.Errorf("%T: %v", object, err)
		}
	}
	for _, name := range []string{classisServerName, classisServerDatabaseName} {
		policy := &unstructured.Unstructured{}
		policy.SetGroupVersionKind(resources.NetworkPolicyGVK())
		if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: name}, policy); err != nil {
			t.Errorf("network policy %q: %v", name, err)
		}
	}
}

func TestExternalClassisServerCreatesNoDatabase(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseExternal)
	objects := append([]client.Object{plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant}, classisServerSecrets("admin", "s3", "db")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	ctx := context.Background()

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerDatabaseName}, cluster); err == nil {
		t.Error("an internal database was created for an external one")
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("Deployment: %v", err)
	}
}

func TestClassisServerWaitsForItsSecrets(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), classisServerTenant(neteye.TenantDatabaseExternal))
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err == nil {
		t.Error("the Deployment exists without its Secrets")
	}
	component := getTenant(t, c, testTenant, testTenant).Status.Components[string(classisServerComponentID)]
	if component.Reason != "WaitingForSecrets" || !strings.Contains(component.Message, "admin") {
		t.Errorf("component = %#v", component)
	}
}

func TestDisabledClassisServerLosesItsDeploymentOnly(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, classisServerSecrets("admin", "s3", "db")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	ctx := context.Background()

	current := getTenant(t, c, testTenant, testTenant)
	current.Spec.ClassisServer.Enabled = false
	if err := c.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err == nil {
		t.Error("the Deployment of a disabled server remains")
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &corev1.Service{}); err != nil {
		t.Errorf("the Service was removed: %v", err)
	}
}

func cloudNativePGExtension(t *testing.T, c client.Client) error {
	t.Helper()
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"})
	return c.Get(context.Background(), types.NamespacedName{Name: "cloudnative-pg"}, extension)
}

// The first tenant with an internal database installs the operator, and its
// database waits until OLM reports the operator installed.
func TestInternalClassisServerInstallsCloudNativePGAndWaitsForIt(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseLocal)
	objects := append([]client.Object{plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant}, classisServerSecrets("admin", "s3", "classis-server-db-app", "classis-server-db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err != nil {
		t.Fatalf("ClusterExtension: %v", err)
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerDatabaseName}, cluster); err == nil {
		t.Error("the database cluster was created before the operator was installed")
	}
	component := getTenant(t, c, testTenant, testTenant).Status.Components[string(classisServerDatabaseComponentID)]
	if component.Reason != "WaitingForCloudNativePG" {
		t.Errorf("component = %#v, want WaitingForCloudNativePG", component)
	}
}

// neteye-setup creates the cnpg-system namespace, as for Keycloak: while it is
// missing the database waits, and the operator creates neither the namespace
// nor the ClusterExtension.
func TestInternalClassisServerWaitsForTheNamespaceThatSetupCreates(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseLocal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, classisServerSecrets("admin", "s3", "classis-server-db-app", "classis-server-db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err == nil {
		t.Error("the ClusterExtension was created without the namespace")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cnpg-system"}, &corev1.Namespace{}); err == nil {
		t.Error("the operator created the cnpg-system namespace")
	}
	component := getTenant(t, c, testTenant, testTenant).Status.Components[string(classisServerDatabaseComponentID)]
	if component.Reason != "WaitingForCNPGNamespace" {
		t.Errorf("component = %#v, want WaitingForCNPGNamespace", component)
	}
	if !strings.Contains(component.Message, "neteye-setup") {
		t.Errorf("message = %q, want it to name neteye-setup", component.Message)
	}
}

// An external database installs nothing.
func TestExternalClassisServerInstallsNoCloudNativePG(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, classisServerSecrets("admin", "s3", "db")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err == nil {
		t.Error("the CloudNativePG operator was installed for an external database")
	}
}

// The operator belongs to the cluster: disabling the server leaves it, and a
// second tenant reuses it instead of installing another.
func TestCloudNativePGStaysWhenTheServerIsDisabled(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseLocal)
	objects := append([]client.Object{plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant}, classisServerSecrets("admin", "s3", "classis-server-db-app", "classis-server-db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	current := getTenant(t, c, testTenant, testTenant)
	current.Spec.ClassisServer.Enabled = false
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err != nil {
		t.Errorf("the ClusterExtension left with the server: %v", err)
	}
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"})
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cloudnative-pg"}, extension); err != nil {
		t.Fatal(err)
	}
	if len(extension.GetOwnerReferences()) != 0 {
		t.Errorf("the ClusterExtension has owners %v, it belongs to no tenant", extension.GetOwnerReferences())
	}
}

// The server speaks plain HTTP behind a proxy and authenticates no agent: no
// Certificate is requested, and the Deployment is created once the Secrets of the
// admin users and of S3 exist, with no Secret for any key of the agents.
func TestClassisServerNeedsNoCertificateAndNoAgentKey(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, classisServerSecrets("admin", "s3", "db")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("the Deployment does not exist although admin, s3 and db Secrets do: %v", err)
	}
	component := getTenant(t, c, testTenant, testTenant).Status.Components[string(classisServerComponentID)]
	if component.Reason == "WaitingForSecrets" {
		t.Errorf("component = %#v, no Secret is missing", component)
	}
	certificates := &unstructured.UnstructuredList{}
	certificates.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "CertificateList"})
	if err := c.List(context.Background(), certificates, client.InNamespace(testTenant)); err == nil && len(certificates.Items) != 0 {
		t.Errorf("the server asked for %d Certificate(s), it holds no certificate", len(certificates.Items))
	}
}

// A missing Secret of the admin users still holds the Deployment back.
func TestClassisServerWaitsForTheAdminUsers(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, classisServerSecrets("s3", "db")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	component := getTenant(t, c, testTenant, testTenant).Status.Components[string(classisServerComponentID)]
	if component.Reason != "WaitingForSecrets" || !strings.Contains(component.Message, "admin") {
		t.Errorf("component = %#v, want WaitingForSecrets naming admin", component)
	}
}

// The configuration has no TLS section and no [agents] section: the Server refuses
// both, and it admits the agents without checking them.
func TestClassisServerConfigHasNoTLSAndNoAgentsSection(t *testing.T) {
	config := classisServerConfig(classisServerTenant(neteye.TenantDatabaseLocal))
	for _, removed := range []string{"[tls]", "[rest.tls]", "[client_ca]", "[enrolment]", "[agents", "jwt", "client_ca_file", "tls.crt"} {
		if strings.Contains(config, removed) {
			t.Errorf("config holds %q, the server refuses it or it is gone:\n%s", removed, config)
		}
	}
	for _, want := range []string{"[rest]\nlisten = \"0.0.0.0:4321\"", "basic_users_file"} {
		if !strings.Contains(config, want) {
			t.Errorf("config lacks %q:\n%s", want, config)
		}
	}
}

// The probes ask over plain HTTP, and the startup probe outlasts the 180 s the
// server retries PostgreSQL and S3 at startup.
func TestClassisServerStartupProbeOutlastsTheServerRetries(t *testing.T) {
	probe := classisServerDeployment(classisServerTenant(neteye.TenantDatabaseLocal), "sum", "secrets").Spec.Template.Spec.Containers[0].StartupProbe
	if total := probe.PeriodSeconds * probe.FailureThreshold; total < 200 {
		t.Errorf("startup probe allows %d s, want at least 200", total)
	}
	if probe.HTTPGet == nil || probe.HTTPGet.Scheme != corev1.URISchemeHTTP || probe.HTTPGet.Path != "/readyz" {
		t.Errorf("startup probe = %#v, want HTTP /readyz", probe.HTTPGet)
	}
}

// No Secret volume belongs to the agents, and none is left over from the TLS days.
func TestClassisServerMountsNoAgentKeyNorTLS(t *testing.T) {
	pod := classisServerDeployment(classisServerTenant(neteye.TenantDatabaseLocal), "sum", "secrets").Spec.Template.Spec
	for _, volume := range pod.Volumes {
		switch volume.Name {
		case "agent-token-key", "tls", "client-ca":
			t.Errorf("volume %q should not exist", volume.Name)
		}
	}
}

// A component that waits for another, here the Deployment, is looked at again as soon
// as NetEyeReconciler looks at its own waiting components, not after the interval of a failure.
func TestClassisServerWaitingComponentsAreRequeuedQuickly(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, classisServerSecrets("admin", "s3", "db")...)
	r, _ := newTenantReconciler(t, objects...)
	result, err := r.Reconcile(context.Background(), requestFor(&neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant}}))
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if result.RequeueAfter != DefaultWaitForProgressingRequeueAfter {
		t.Errorf("requeueAfter = %s, want %s", result.RequeueAfter, DefaultWaitForProgressingRequeueAfter)
	}
	if DefaultWaitForProgressingRequeueAfter >= DefaultFailureRequeueAfter {
		t.Errorf("a waiting component is looked at again after %s, not sooner than a failure (%s)", DefaultWaitForProgressingRequeueAfter, DefaultFailureRequeueAfter)
	}
}

// The CA bundle of the tenant is the CA of the S3 service.
func TestClassisServerTrustsTheCABundleOfTheTenant(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseLocal)
	tenant.Spec.CABundle = &neteye.ConfigMapKeyReference{Name: "tenant-ca-bundle", Key: "ca.pem"}
	if config := classisServerConfig(tenant); !strings.Contains(config, `ca_file = "/etc/classis/ca-bundle/ca.pem"`) {
		t.Errorf("config has no S3 ca_file:\n%s", config)
	}
	found := false
	for _, volume := range classisServerDeployment(tenant, "sum", "secrets").Spec.Template.Spec.Volumes {
		found = found || (volume.ConfigMap != nil && volume.ConfigMap.Name == "tenant-ca-bundle")
	}
	if !found {
		t.Error("the Deployment does not mount the CA bundle ConfigMap")
	}
}

// peerless counts the rules of a policy direction that name no peer, with the
// ports they open.
func peerless(rules []any, peerKey string) []any {
	var found []any
	for _, rule := range rules {
		rule := rule.(map[string]any)
		if _, named := rule[peerKey]; !named {
			found = append(found, rule["ports"])
		}
	}
	return found
}

func TestClassisServerNetworkPoliciesAreWidenedWherePeersCannotBeNamed(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantDatabaseLocal)
	tenant.Spec.ClassisServer.Blobs.Endpoint = "https://s3.example.com:9000"

	server := classisServerNetworkPolicySpec(tenant)
	// 4320 is open to any source, and 4321 is narrowed by adminIngressCIDRs.
	ingress := server["ingress"].([]any)
	if got := peerless(ingress, "from"); len(got) != 1 {
		t.Fatalf("expected the agent plane alone without a peer, got %v", got)
	}
	// S3 is reached on 443 and on its own port, from any destination, and the
	// database through the PostgreSQL pods.
	egress := server["egress"].([]any)
	ports := peerless(egress, "to")
	if len(ports) != 2 { // DNS names its peer; S3 and the DNS-less rule do not
		t.Logf("egress rules without a peer: %v", ports)
	}
	want := resources.NetworkPolicyPorts("TCP", 443, 9000)
	found := false
	for _, p := range ports {
		if reflect.DeepEqual(p, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no egress to any destination on 443 and 9000: %v", ports)
	}

	// Without adminIngressCIDRs, 4321 is open to any source.
	tenant.Spec.ClassisServer.Network.AdminIngressCIDRs = nil
	if got := peerless(classisServerNetworkPolicySpec(tenant)["ingress"].([]any), "from"); len(got) != 2 {
		t.Fatalf("expected both planes without a peer, got %v", got)
	}

	// An external database port joins the egress to any destination.
	external := classisServerTenant(neteye.TenantDatabaseExternal)
	external.Spec.Database.Port = 5433
	if got := peerless(classisServerNetworkPolicySpec(external)["egress"].([]any), "to"); !reflect.DeepEqual(got[len(got)-1], resources.NetworkPolicyPorts("TCP", 443, 5433)) {
		t.Fatalf("external database port missing: %v", got)
	}

	// The PostgreSQL instances reach the Kubernetes API on 443 and 6443 from
	// any destination, with no address to list.
	database := classisServerDatabaseNetworkPolicySpec()
	if got := peerless(database["egress"].([]any), "to"); !reflect.DeepEqual(got, []any{resources.NetworkPolicyPorts("TCP", 443, 6443)}) {
		t.Fatalf("API server egress: %v", got)
	}
}

// The instances of a tenant database never share a node, whatever their number:
// the anti-affinity is required, never preferred.
func TestClassisServerDatabaseInstancesNeverShareANode(t *testing.T) {
	for _, instances := range []int32{1, 2} {
		spec := classisServerDatabaseSpec(&neteye.ClassisServerInternalDatabaseSpec{Instances: instances})
		if got := spec["instances"]; got != int64(instances) {
			t.Errorf("instances = %v, want %d", got, instances)
		}
		want := map[string]any{
			"enablePodAntiAffinity": true,
			"podAntiAffinityType":   "required",
			"topologyKey":           "kubernetes.io/hostname",
		}
		if got := spec["affinity"]; !reflect.DeepEqual(got, want) {
			t.Errorf("affinity with %d instance(s) = %v, want %v", instances, got, want)
		}
	}
	if got := classisServerDatabaseSpec(nil)["affinity"].(map[string]any)["podAntiAffinityType"]; got != "required" {
		t.Errorf("default spec anti-affinity = %v, want required", got)
	}
}
