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

// netEyeResource is the NetEye resource of the cluster, whose version selects
// the images of the tenant components.
func netEyeResource() *neteye.NetEye {
	return &neteye.NetEye{ObjectMeta: metav1.ObjectMeta{Namespace: "neteye-tenant-shared", Name: "neteye"}, Spec: neteye.NetEyeSpec{Version: neteye.CurrentNetEyeVersion}}
}

func currentImages(t *testing.T) neteye.NetEyeComponents {
	t.Helper()
	images, ok := neteye.ComponentsForVersion(neteye.CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("no images for NetEye %s", neteye.CurrentNetEyeVersion)
	}
	return images
}

// classisServerTenant is a tenant with a Classis Server on a database of the
// provider, and on an external object store, which needs nothing in the
// cluster.
func classisServerTenant(provider neteye.TenantStorageProvider) *neteye.NetEyeTenant {
	tenant := tenantResource()
	tenant.Spec.Database = &neteye.TenantDatabaseSpec{Provider: provider}
	tenant.Spec.ObjectStorage = &neteye.TenantObjectStorageSpec{Provider: neteye.TenantStorageExternal, External: &neteye.TenantExternalObjectStorageSpec{Endpoint: "https://s3.example.com", Bucket: "tenant", CredentialsSecretName: "s3"}}
	tenant.Spec.ClassisServer = &neteye.NetEyeClassisServerSpec{
		Enabled:              true,
		AdminUsersSecretName: "admin",
		LogLevel:             "info",
		Network:              neteye.ClassisServerNetworkSpec{AdminIngressCIDRs: []string{"10.0.1.0/24"}},
	}
	if provider == neteye.TenantStorageExternal {
		tenant.Spec.Database.External = &neteye.TenantExternalDatabaseSpec{Host: "db.example.com", CASecret: neteye.NetEyeSecretKeySelector{Name: "db-ca", Key: "ca.crt"}}
		tenant.Spec.ClassisServer.DBConnection = &neteye.ClassisServerDBConnectionSpec{
			UsernameSecret: &neteye.NetEyeSecretKeySelector{Name: "db", Key: "username"},
			PasswordSecret: &neteye.NetEyeSecretKeySelector{Name: "db", Key: "password"},
		}
	}
	return tenant
}

// externalSecrets are the Secrets a tenant on an external database and object
// store provides: the admin users, the S3 key pair, the database credentials and
// the CA of the database.
func externalSecrets() []client.Object { return classisServerSecrets("admin", "s3", "db", "db-ca") }

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

func envNamed(container corev1.Container, name string) *corev1.EnvVar {
	for i := range container.Env {
		if container.Env[i].Name == name {
			return &container.Env[i]
		}
	}
	return nil
}

func volumeNamed(pod corev1.PodSpec, name string) *corev1.Volume {
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == name {
			return &pod.Volumes[i]
		}
	}
	return nil
}

func TestClassisServerDeploymentIsHardenedAndStateless(t *testing.T) {
	d := classisServerDeployment(classisServerTenant(neteye.TenantStorageLocal), "image", "sum", "secrets")
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
	for _, volume := range pod.Volumes {
		if volume.PersistentVolumeClaim != nil {
			t.Errorf("volume %q is a PVC, the server must keep nothing on disk", volume.Name)
		}
		if volume.Secret != nil && *volume.Secret.DefaultMode != 0o440 {
			t.Errorf("secret volume %q mode = %o, want 440", volume.Name, *volume.Secret.DefaultMode)
		}
	}
	// The pods carry the marks the policies of the database and of the local
	// object store admit.
	for _, label := range []string{tenantDatabaseClientLabel, tenantObjectStorageClientLabel} {
		if d.Spec.Template.Labels[label] != "true" {
			t.Errorf("pod label %q = %q, want true", label, d.Spec.Template.Labels[label])
		}
	}
}

// The Server reads its environment under CLASSIS_SERVER__ since the product
// was renamed; the old FLEET_SERVER__ prefix is ignored by it.
func TestClassisServerDeploymentUsesTheClassisNames(t *testing.T) {
	d := classisServerDeployment(classisServerTenant(neteye.TenantStorageLocal), "image", "sum", "secrets")
	container := d.Spec.Template.Spec.Containers[0]
	for _, env := range container.Env {
		if strings.HasPrefix(env.Name, "FLEET_SERVER") {
			t.Errorf("environment variable %q uses the old prefix", env.Name)
		}
	}
	for _, name := range []string{"CLASSIS_SERVER__BLOBS__S3__ACCESS_KEY_ID", "CLASSIS_SERVER__DATABASE__POSTGRES__USER"} {
		if envNamed(container, name) == nil {
			t.Errorf("no %s in %v", name, container.Env)
		}
	}
	if len(container.Args) != 2 || container.Args[0] != "--config" || container.Args[1] != "/etc/classis/config/server.toml" {
		t.Errorf("args = %v, want --config on the mounted server.toml", container.Args)
	}
	for _, name := range []string{string(classisServerComponentID), string(tenantDatabaseComponentID), string(tenantObjectStorageComponentID), classisServerConfigVersionAnnotation, classisServerSecretsVersionsAnnotation} {
		if strings.Contains(name, "fleet") {
			t.Errorf("%q still carries the old product name", name)
		}
	}
}

// The server connects to the PostgreSQL and to the object store of the tenant:
// the CloudNativePG cluster and rustfs when they are local, the endpoints of
// the spec when they are external.
func TestClassisServerConfigUsesTheStoresOfTheTenant(t *testing.T) {
	local := classisServerTenant(neteye.TenantStorageLocal)
	local.Spec.ObjectStorage = &neteye.TenantObjectStorageSpec{Provider: neteye.TenantStorageLocal}
	config := classisServerConfig(local)
	for _, want := range []string{
		`host = "neteye-tenant-db-rw"`, "port = 5432", `dbname = "classis"`, `tls = true`,
		`endpoint = "https://neteye-tenant-s3.` + testTenant + `.svc:9000"`, `bucket = "` + testTenant + `"`, `region = "us-east-1"`, `prefix = "classis/"`,
		`ca_file = "/etc/classis/s3-ca/ca.crt"`, `store = "postgres"`, `store = "s3"`, `listen = "0.0.0.0:4321"`, "[log]\nformat = \"json\"",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("local config lacks %q:\n%s", want, config)
		}
	}
	// The user comes from the credentials Secret, not from the file.
	if strings.Contains(config, "user =") {
		t.Errorf("config names the database user:\n%s", config)
	}

	external := classisServerTenant(neteye.TenantStorageExternal)
	external.Spec.Database.External.Port = 5433
	external.Spec.ObjectStorage.External.Region = "eu-west-1"
	external.Spec.ClassisServer.DBConnection.DBName = "classis_prod"
	external.Spec.ClassisServer.ObjectStorage = &neteye.ClassisServerObjectStorageSpec{Prefix: "fleet/"}
	config = classisServerConfig(external)
	for _, want := range []string{`host = "db.example.com"`, "port = 5433", `dbname = "classis_prod"`, `endpoint = "https://s3.example.com"`, `bucket = "tenant"`, `region = "eu-west-1"`, `prefix = "fleet/"`, `secret_access_key_file = "/etc/classis/s3-credentials/secret-access-key"`} {
		if !strings.Contains(config, want) {
			t.Errorf("external config lacks %q:\n%s", want, config)
		}
	}
}

// An external object store without a credentials Secret is reached with the
// workload identity of the platform: no key pair is configured or mounted.
func TestClassisServerUsesTheWorkloadIdentityWithoutS3Credentials(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	tenant.Spec.ObjectStorage.External.CredentialsSecretName = ""
	if config := classisServerConfig(tenant); strings.Contains(config, "secret_access_key_file") {
		t.Errorf("config names a key pair:\n%s", config)
	}
	d := classisServerDeployment(tenant, "image", "sum", "secrets")
	if envNamed(d.Spec.Template.Spec.Containers[0], "CLASSIS_SERVER__BLOBS__S3__ACCESS_KEY_ID") != nil || volumeNamed(d.Spec.Template.Spec, "s3-credentials") != nil {
		t.Error("the Deployment carries a key pair")
	}
}

// The credentials of the server are the generated Secret on a local database,
// and the Secret keys of dbConnection on an external one. Each volume projects
// one key, so the pod sees nothing else of the Secret.
func TestClassisServerTakesItsDatabaseCredentialsFromWhereTheProviderSays(t *testing.T) {
	for provider, want := range map[neteye.TenantStorageProvider][3]string{
		neteye.TenantStorageLocal:    {classisServerDBCredentialsSecretName, "username", "password"},
		neteye.TenantStorageExternal: {"db", "username", "password"},
	} {
		tenant := classisServerTenant(provider)
		if provider == neteye.TenantStorageExternal {
			tenant.Spec.ClassisServer.DBConnection.PasswordSecret = &neteye.NetEyeSecretKeySelector{Name: "db", Key: "pw"}
			want[2] = "pw"
		}
		pod := classisServerDeployment(tenant, "image", "sum", "secrets").Spec.Template.Spec
		user := envNamed(pod.Containers[0], "CLASSIS_SERVER__DATABASE__POSTGRES__USER")
		if user == nil || user.ValueFrom.SecretKeyRef.Name != want[0] || user.ValueFrom.SecretKeyRef.Key != want[1] {
			t.Errorf("%s: user = %#v, want %s/%s", provider, user, want[0], want[1])
		}
		password := volumeNamed(pod, "db-password")
		if password == nil || password.Secret.SecretName != want[0] || !reflect.DeepEqual(password.Secret.Items, []corev1.KeyToPath{{Key: want[2], Path: "password"}}) {
			t.Errorf("%s: password volume = %#v", provider, password)
		}
	}
}

// The image of the server is the one of the NetEye version, and the
// RELATED_IMAGE_CLASSIS_SERVER variable of the operator overrides it.
func TestClassisServerImageFollowsTheNetEyeVersion(t *testing.T) {
	for _, override := range []string{"", "registry.example/classis-server:dev"} {
		t.Setenv(neteye.RelatedImageClassisServerEnv, override)
		tenant := classisServerTenant(neteye.TenantStorageExternal)
		objects := append([]client.Object{plainNamespace(testTenant), tenant, netEyeResource()}, externalSecrets()...)
		r, c := newTenantReconciler(t, objects...)
		reconcileTenant(t, r)
		deployment := &appsv1.Deployment{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, deployment); err != nil {
			t.Fatalf("Deployment: %v", err)
		}
		want := override
		if want == "" {
			want = currentImages(t).ClassisServerImage
		}
		if got := deployment.Spec.Template.Spec.Containers[0].Image; got != want {
			t.Errorf("image = %q, want %q", got, want)
		}
	}
}

// Without a NetEye resource the images are unknown, so the server waits.
func TestClassisServerWaitsForTheNetEyeResource(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if state, message := tenantComponentState(t, c, classisServerComponentID); state != "waiting" || !strings.Contains(message, "NetEye") {
		t.Errorf("server is %q: %q, want waiting for the NetEye resource", state, message)
	}
}

func TestExternalClassisServerCreatesNoDatabase(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	objects := append([]client.Object{plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant, netEyeResource()}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	ctx := context.Background()

	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: tenantDatabaseName}, cluster); err == nil {
		t.Error("a local database was created for an external one")
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("Deployment: %v", err)
	}
	// An external store is ready at once: the status does not name it.
	for _, id := range []componentID{tenantDatabaseComponentID, tenantObjectStorageComponentID} {
		if state, message := tenantComponentState(t, c, id); state != "" {
			t.Errorf("component %s is %q: %q, want ready", id, state, message)
		}
	}
}

func TestClassisServerWaitsForItsSecrets(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), classisServerTenant(neteye.TenantStorageExternal), netEyeResource())
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err == nil {
		t.Error("the Deployment exists without its Secrets")
	}
	state, message := tenantComponentState(t, c, classisServerComponentID)
	if state != "waiting" || !strings.Contains(message, "waiting for Secrets: ") {
		t.Errorf("server is %q: %q, want waiting for its Secrets", state, message)
	}
	for _, name := range []string{"admin", "db", "db-ca", "s3"} {
		if !strings.Contains(message, name) {
			t.Errorf("message %q does not name the Secret %q", message, name)
		}
	}
}

func TestDisabledClassisServerLosesItsDeploymentOnly(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant, netEyeResource()}, externalSecrets()...)
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

// A tenant that bypassed the webhook without an object store is not served:
// the server reports the rule it breaks.
func TestClassisServerRefusesATenantWithoutObjectStorage(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	tenant.Spec.ObjectStorage = nil
	objects := append([]client.Object{plainNamespace(testTenant), tenant, netEyeResource()}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenantRequestAllowingError(t, r)
	if state, message := tenantComponentState(t, c, classisServerComponentID); state != "failed" || !strings.Contains(message, "objectStorage") {
		t.Errorf("server is %q: %q, want failed naming objectStorage", state, message)
	}
}

// The server speaks plain HTTP behind a proxy and authenticates no agent: on
// external stores no Certificate is requested, and the Deployment is created
// once its Secrets exist, with no Secret for any key of the agents.
func TestClassisServerNeedsNoCertificateAndNoAgentKey(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant, netEyeResource()}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("the Deployment does not exist although its Secrets do: %v", err)
	}
	certificates := &unstructured.UnstructuredList{}
	certificates.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "CertificateList"})
	if err := c.List(context.Background(), certificates, client.InNamespace(testTenant)); err == nil && len(certificates.Items) != 0 {
		t.Errorf("the server asked for %d Certificate(s), it holds no certificate", len(certificates.Items))
	}
}

// The configuration has no TLS section and no [agents] section: the Server refuses
// both, and it admits the agents without checking them.
func TestClassisServerConfigHasNoTLSAndNoAgentsSection(t *testing.T) {
	config := classisServerConfig(classisServerTenant(neteye.TenantStorageLocal))
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
	probe := classisServerDeployment(classisServerTenant(neteye.TenantStorageLocal), "image", "sum", "secrets").Spec.Template.Spec.Containers[0].StartupProbe
	if total := probe.PeriodSeconds * probe.FailureThreshold; total < 200 {
		t.Errorf("startup probe allows %d s, want at least 200", total)
	}
	if probe.HTTPGet == nil || probe.HTTPGet.Scheme != corev1.URISchemeHTTP || probe.HTTPGet.Path != "/readyz" {
		t.Errorf("startup probe = %#v, want HTTP /readyz", probe.HTTPGet)
	}
}

// No Secret volume belongs to the agents, and none is left over from the TLS
// days: of the certificate of the local object store only its CA is mounted.
func TestClassisServerMountsNoAgentKeyNorTLS(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageLocal)
	tenant.Spec.ObjectStorage = &neteye.TenantObjectStorageSpec{Provider: neteye.TenantStorageLocal}
	pod := classisServerDeployment(tenant, "image", "sum", "secrets").Spec.Template.Spec
	for _, volume := range pod.Volumes {
		switch volume.Name {
		case "agent-token-key", "tls", "client-ca":
			t.Errorf("volume %q should not exist", volume.Name)
		}
	}
	ca := volumeNamed(pod, "s3-ca")
	if ca == nil || ca.Secret.SecretName != tenantObjectStorageTLSSecret || !reflect.DeepEqual(ca.Secret.Items, []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}) {
		t.Errorf("s3-ca volume = %#v, want the ca.crt key alone", ca)
	}
}

// A component that waits for another, here the Deployment, is looked at again as soon
// as NetEyeReconciler looks at its own waiting components, not after the interval of a failure.
func TestClassisServerWaitingComponentsAreRequeuedQuickly(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	objects := append([]client.Object{plainNamespace(testTenant), tenant, netEyeResource()}, externalSecrets()...)
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

// The CA bundle of the tenant is the CA of an external object store; a local
// one is verified against the CA of the tenant instead.
func TestClassisServerTrustsTheCABundleForAnExternalObjectStore(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageLocal)
	tenant.Spec.CABundle = &neteye.NetEyeConfigMapKeySelector{Name: "tenant-ca-bundle", Key: "ca.pem"}
	if config := classisServerConfig(tenant); !strings.Contains(config, `ca_file = "/etc/classis/ca-bundle/ca.pem"`) {
		t.Errorf("config has no S3 ca_file:\n%s", config)
	}
	if volume := volumeNamed(classisServerDeployment(tenant, "image", "sum", "secrets").Spec.Template.Spec, "ca-bundle"); volume == nil || volume.ConfigMap.Name != "tenant-ca-bundle" {
		t.Error("the Deployment does not mount the CA bundle ConfigMap")
	}

	tenant.Spec.ObjectStorage = &neteye.TenantObjectStorageSpec{Provider: neteye.TenantStorageLocal}
	if config := classisServerConfig(tenant); strings.Contains(config, "ca-bundle") {
		t.Errorf("a local object store is verified against the CA bundle:\n%s", config)
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
	// External stores: the database and S3 ports are open to any destination.
	external := classisServerTenant(neteye.TenantStorageExternal)
	external.Spec.Database.External.Port = 5433
	external.Spec.ObjectStorage.External.Endpoint = "https://s3.example.com:9000"
	server := classisServerNetworkPolicySpec(external)
	if got := peerless(server["egress"].([]any), "to"); !reflect.DeepEqual(got, []any{resources.NetworkPolicyPorts("TCP", 5433, 9000)}) {
		t.Errorf("egress to any destination = %v, want 5433 and 9000", got)
	}
	// 4320 is open to any source, and 4321 is narrowed by adminIngressCIDRs.
	if got := peerless(server["ingress"].([]any), "from"); len(got) != 1 {
		t.Fatalf("expected the agent plane alone without a peer, got %v", got)
	}
	external.Spec.ClassisServer.Network.AdminIngressCIDRs = nil
	if got := peerless(classisServerNetworkPolicySpec(external)["ingress"].([]any), "from"); len(got) != 2 {
		t.Fatalf("expected both planes without a peer, got %v", got)
	}

	// Local stores: every egress names its peer.
	local := classisServerTenant(neteye.TenantStorageLocal)
	local.Spec.ObjectStorage = &neteye.TenantObjectStorageSpec{Provider: neteye.TenantStorageLocal}
	if got := peerless(classisServerNetworkPolicySpec(local)["egress"].([]any), "to"); len(got) != 0 {
		t.Errorf("egress to any destination with local stores: %v", got)
	}
}

// reconcileTenantRequestAllowingError reconciles the test tenant once and
// ignores the error a degraded component returns: the test reads the status.
func reconcileTenantRequestAllowingError(t *testing.T, r *NetEyeTenantReconciler) {
	t.Helper()
	_, _ = r.Reconcile(context.Background(), requestFor(&neteye.NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: testTenant}}))
}
