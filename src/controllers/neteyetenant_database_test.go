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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// localDatabaseObjects are the objects of a cluster where a tenant has a local
// database and an external object store, and the CloudNativePG operator is
// installed.
func localDatabaseObjects(tenant *neteye.NetEyeTenant) []client.Object {
	return append([]client.Object{plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant, installedCloudNativePG(), netEyeResource()}, classisServerSecrets("admin", "s3", tenantDatabaseName+"-ca")...)
}

func getUnstructured(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	t.Helper()
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, object); err != nil {
		t.Fatalf("%s %q: %v", gvk.Kind, name, err)
	}
	return object
}

func setStatus(t *testing.T, c client.Client, object *unstructured.Unstructured, value any, fields ...string) {
	t.Helper()
	if err := unstructured.SetNestedField(object.Object, value, append([]string{"status"}, fields...)...); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), object); err != nil {
		t.Fatal(err)
	}
}

func markConditionReady(t *testing.T, c client.Client, object *unstructured.Unstructured) {
	t.Helper()
	setStatus(t, c, object, []any{map[string]any{"type": "Ready", "status": "True"}}, "conditions")
}

// The tenant has one PostgreSQL. The service gets a database and a role of
// its own on it, with generated credentials, and the server is created only
// once CloudNativePG applied both.
func TestLocalDatabaseGivesTheServerItsOwnDatabaseAndRole(t *testing.T) {
	r, c := newTenantReconciler(t, localDatabaseObjects(classisServerTenant(neteye.TenantStorageLocal))...)
	ctx := context.Background()
	reconcileTenant(t, r)

	cluster := getUnstructured(t, c, cnpgClusterGVK, tenantDatabaseName)
	if instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); instances != 2 {
		t.Errorf("instances = %d, want 2", instances)
	}
	if image, _, _ := unstructured.NestedString(cluster.Object, "spec", "imageName"); image != currentImages(t).PostgreSQLImage {
		t.Errorf("imageName = %q, want the image of the NetEye version", image)
	}
	if _, found, _ := unstructured.NestedMap(cluster.Object, "spec", "bootstrap"); found {
		t.Error("the cluster bootstraps the database of a service: every service gets its database declaratively")
	}
	// The database is not ready yet, so the server waits for it.
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err == nil {
		t.Error("the Deployment exists before the database is ready")
	}
	if state, message := tenantComponentState(t, c, tenantDatabaseComponentID); state != "waiting" || strings.Contains(message, "waiting for dependencies") {
		t.Errorf("database is %q: %q, want waiting for itself", state, message)
	}
	if state, message := tenantComponentState(t, c, classisServerComponentID); state != "waiting" || !strings.Contains(message, "waiting for dependencies: "+string(tenantDatabaseComponentID)) {
		t.Errorf("server is %q: %q, want waiting for the database", state, message)
	}

	markConditionReady(t, c, cluster)
	reconcileTenant(t, r)
	credentials := getSecret(t, c, classisServerDBCredentialsSecretName)
	if credentials.Type != corev1.SecretTypeBasicAuth || string(credentials.Data["username"]) != "classis" || len(credentials.Data["password"]) != classisServerAdminPasswordLength {
		t.Errorf("credentials = %s with %d keys, want basic-auth classis with a %d-character password", credentials.Type, len(credentials.Data), classisServerAdminPasswordLength)
	}
	if credentials.Labels["cnpg.io/reload"] != "true" {
		t.Error("CloudNativePG does not reload the role when its credentials change")
	}
	name := tenantDatabaseName + "-classis"
	role := getUnstructured(t, c, cnpgDatabaseRoleGVK, name)
	if spec, _, _ := unstructured.NestedMap(role.Object, "spec"); !reflect.DeepEqual(spec, map[string]any{
		"cluster": map[string]any{"name": tenantDatabaseName}, "name": "classis", "login": true, "ensure": "present",
		"passwordSecret": map[string]any{"name": classisServerDBCredentialsSecretName}, "databaseRoleReclaimPolicy": "retain",
	}) {
		t.Errorf("role spec = %v", spec)
	}
	database := getUnstructured(t, c, cnpgDatabaseGVK, name)
	if spec, _, _ := unstructured.NestedMap(database.Object, "spec"); !reflect.DeepEqual(spec, map[string]any{
		"cluster": map[string]any{"name": tenantDatabaseName}, "name": "classis", "owner": "classis", "ensure": "present", "databaseReclaimPolicy": "retain",
	}) {
		t.Errorf("database spec = %v", spec)
	}
	if state, message := tenantComponentState(t, c, tenantDatabaseComponentID); state != "waiting" || !strings.Contains(message, "classis") {
		t.Errorf("database is %q: %q, want waiting for the database of the server", state, message)
	}

	setStatus(t, c, role, true, "applied")
	setStatus(t, c, database, true, "applied")
	reconcileTenant(t, r)
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("Deployment once the database is ready: %v", err)
	}
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(resources.NetworkPolicyGVK())
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: tenantDatabaseName}, policy); err != nil {
		t.Errorf("database network policy: %v", err)
	}

	// The credentials are generated once.
	reconcileTenant(t, r)
	if again := getSecret(t, c, classisServerDBCredentialsSecretName); string(again.Data["password"]) != string(credentials.Data["password"]) {
		t.Error("the password changed on a later reconciliation")
	}
}

// Each role reaches its own database over TLS with a password; every other
// connection over the network is refused, so a service never connects to the
// database of another.
func TestLocalDatabaseAdmitsEachRoleToItsOwnDatabaseOnly(t *testing.T) {
	rules := tenantDatabaseHBA([]tenantDatabaseAccess{{DBName: "classis"}, {DBName: "glpi"}})
	want := []any{"hostssl classis classis all scram-sha-256", "hostssl glpi glpi all scram-sha-256", "host all all all reject"}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("pg_hba = %v, want %v", rules, want)
	}
	spec := tenantDatabaseClusterSpec(classisServerTenant(neteye.TenantStorageLocal), "image", tenantDatabaseAccesses(classisServerTenant(neteye.TenantStorageLocal)))
	if got, _, _ := unstructured.NestedSlice(spec, "postgresql", "pg_hba"); !reflect.DeepEqual(got, []any{"hostssl classis classis all scram-sha-256", "host all all all reject"}) {
		t.Errorf("cluster pg_hba = %v", got)
	}
}

// An existing credentials Secret that CloudNativePG could not use is reported,
// never overwritten.
func TestLocalDatabaseRefusesUnusableCredentials(t *testing.T) {
	objects := append(localDatabaseObjects(classisServerTenant(neteye.TenantStorageLocal)), secretWithData(classisServerDBCredentialsSecretName, map[string]string{"password": "p"}))
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	markConditionReady(t, c, getUnstructured(t, c, cnpgClusterGVK, tenantDatabaseName))
	reconcileTenantRequestAllowingError(t, r)
	if state, message := tenantComponentState(t, c, tenantDatabaseComponentID); state != "failed" || !strings.Contains(message, "failed to set up the database") || !strings.Contains(message, "basic-auth") {
		t.Errorf("database is %q: %q, want failed naming basic-auth", state, message)
	}
	if secret := getSecret(t, c, classisServerDBCredentialsSecretName); string(secret.Data["password"]) != "p" {
		t.Error("the Secret was overwritten")
	}
}

// The policy of the instances admits the pods marked as database clients, the
// instances themselves and the CloudNativePG operator; the instances reach the
// Kubernetes API on 443 and 6443 from any destination, with no address to list.
func TestLocalDatabaseNetworkPolicyAdmitsTheMarkedClients(t *testing.T) {
	spec := tenantDatabaseNetworkPolicySpec()
	clients := spec["ingress"].([]any)[0].(map[string]any)
	if from := clients["from"].([]any)[0].(map[string]any)["podSelector"]; !reflect.DeepEqual(from, podSelector(map[string]string{tenantDatabaseClientLabel: "true"})) {
		t.Errorf("client rule = %v", clients)
	}
	if got := peerless(spec["egress"].([]any), "to"); !reflect.DeepEqual(got, []any{resources.NetworkPolicyPorts("TCP", 443, 6443)}) {
		t.Fatalf("API server egress: %v", got)
	}
}

// The instances of a tenant database never share a node, whatever their number:
// the anti-affinity is required, never preferred.
func TestLocalDatabaseInstancesNeverShareANode(t *testing.T) {
	for _, replicas := range []int32{1, 2} {
		tenant := classisServerTenant(neteye.TenantStorageLocal)
		tenant.Spec.Database.Local = &neteye.TenantLocalDatabaseSpec{Replicas: replicas, StorageClassName: "local-path"}
		spec := tenantDatabaseClusterSpec(tenant, "image", nil)
		if got := spec["instances"]; got != int64(replicas) {
			t.Errorf("instances = %v, want %d", got, replicas)
		}
		want := map[string]any{"enablePodAntiAffinity": true, "podAntiAffinityType": "required", "topologyKey": "kubernetes.io/hostname"}
		if got := spec["affinity"]; !reflect.DeepEqual(got, want) {
			t.Errorf("affinity with %d replica(s) = %v, want %v", replicas, got, want)
		}
		if got := spec["storage"]; !reflect.DeepEqual(got, map[string]any{"size": "5Gi", "storageClass": "local-path"}) {
			t.Errorf("storage = %v", got)
		}
	}
}

func cloudNativePGExtension(t *testing.T, c client.Client) error {
	t.Helper()
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"})
	return c.Get(context.Background(), types.NamespacedName{Name: "cloudnative-pg"}, extension)
}

// The first tenant with a local database installs the operator, and its
// database waits until OLM reports the operator installed.
func TestLocalDatabaseInstallsCloudNativePGAndWaitsForIt(t *testing.T) {
	tenant := classisServerTenant(neteye.TenantStorageLocal)
	r, c := newTenantReconciler(t, plainNamespace(testTenant), cnpgSystemNamespaceObject(), tenant, netEyeResource())
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err != nil {
		t.Fatalf("ClusterExtension: %v", err)
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: tenantDatabaseName}, cluster); err == nil {
		t.Error("the database cluster was created before the operator was installed")
	}
	if state, message := tenantComponentState(t, c, tenantDatabaseComponentID); state != "waiting" || !strings.Contains(message, "CloudNativePG") {
		t.Errorf("database is %q: %q, want waiting for CloudNativePG", state, message)
	}
}

// neteye-setup creates the cnpg-system namespace, as for Keycloak: while it is
// missing the database waits, and the operator creates neither the namespace
// nor the ClusterExtension.
func TestLocalDatabaseWaitsForTheNamespaceThatSetupCreates(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), classisServerTenant(neteye.TenantStorageLocal), netEyeResource())
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err == nil {
		t.Error("the ClusterExtension was created without the namespace")
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cnpg-system"}, &corev1.Namespace{}); err == nil {
		t.Error("the operator created the cnpg-system namespace")
	}
	if state, message := tenantComponentState(t, c, tenantDatabaseComponentID); state != "waiting" || !strings.Contains(message, "cnpg-system namespace") || !strings.Contains(message, "neteye-setup") {
		t.Errorf("database is %q: %q, want waiting for the namespace that neteye-setup creates", state, message)
	}
}

// An external database installs nothing.
func TestExternalDatabaseInstallsNoCloudNativePG(t *testing.T) {
	objects := append([]client.Object{plainNamespace(testTenant), classisServerTenant(neteye.TenantStorageExternal), netEyeResource()}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	if err := cloudNativePGExtension(t, c); err == nil {
		t.Error("the CloudNativePG operator was installed for an external database")
	}
}

// The operator belongs to the cluster: disabling the server leaves it, and it
// belongs to no tenant.
func TestCloudNativePGStaysWhenTheServerIsDisabled(t *testing.T) {
	r, c := newTenantReconciler(t, localDatabaseObjects(classisServerTenant(neteye.TenantStorageLocal))...)
	reconcileTenant(t, r)
	current := getTenant(t, c, testTenant, testTenant)
	current.Spec.ClassisServer.Enabled = false
	if err := c.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	extension := &unstructured.Unstructured{}
	extension.SetGroupVersionKind(schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"})
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cloudnative-pg"}, extension); err != nil {
		t.Fatalf("the ClusterExtension left with the server: %v", err)
	}
	if len(extension.GetOwnerReferences()) != 0 {
		t.Errorf("the ClusterExtension has owners %v, it belongs to no tenant", extension.GetOwnerReferences())
	}
}
