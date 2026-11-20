// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// localObjectStorageTenant is a tenant with a local object store and an
// external database, which needs nothing in the cluster.
func localObjectStorageTenant() *neteye.NetEyeTenant {
	tenant := classisServerTenant(neteye.TenantStorageExternal)
	tenant.Spec.ObjectStorage = &neteye.TenantObjectStorageSpec{Provider: neteye.TenantStorageLocal, Local: &neteye.TenantLocalObjectStorageSpec{StorageClassName: "local-path"}}
	return tenant
}

// tenantComponentState is what the status of the tenant says about one
// component. The tenant has no status for each component: its message names
// the components that failed or, when none failed, the ones that wait, each
// with its message, after what the tenant is installing or upgrading. state is "failed", "waiting", or "" when the message does
// not name the component.
func tenantComponentState(t *testing.T, c client.Client, id componentID) (state, message string) {
	t.Helper()
	status := getTenant(t, c, testTenant, testTenant).Status.Message
	for prefix, name := range map[string]string{"Failed components: ": "failed", "Waiting for components: ": "waiting"} {
		_, components, ok := strings.Cut(status, prefix)
		if !ok {
			continue
		}
		starts := tenantComponentEntry.FindAllStringSubmatchIndex(components, -1)
		for i, start := range starts {
			if components[start[2]:start[3]] != string(id) {
				continue
			}
			end := len(components)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			return name, components[start[1]:end]
		}
	}
	return "", ""
}

// tenantComponentEntry finds where each component starts in the status
// message of the tenant: at the beginning or after "; ", its ID and ": ".
var tenantComponentEntry = regexp.MustCompile(`(?:^|; )([a-z0-9-]+): `)

func markTenantDeploymentReady(t *testing.T, c client.Client, name string) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, deployment); err != nil {
		t.Fatalf("Deployment %q: %v", name, err)
	}
	deployment.Status.ObservedGeneration = deployment.Generation
	deployment.Status.ReadyReplicas, deployment.Status.UpdatedReplicas = 1, 1
	if err := c.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}

func bucketJob(t *testing.T, c client.Client) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: tenantObjectStorageBucketJob}, job); err != nil {
		t.Fatalf("bucket Job: %v", err)
	}
	return job
}

func finishJob(t *testing.T, c client.Client, condition batchv1.JobConditionType) {
	t.Helper()
	job := bucketJob(t, c)
	job.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue, Message: "done"}}
	if err := c.Status().Update(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}

// The local object store comes up in order: the CA of the tenant and the
// certificate of the store, then rustfs on its volume, then the bucket. The
// server waits for all of it.
func TestLocalObjectStorageRunsRustFSAndCreatesTheBucket(t *testing.T) {
	objects := append([]client.Object{plainNamespace(testTenant), localObjectStorageTenant(), netEyeResource()}, classisServerSecrets("admin", "db", "db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	ctx := context.Background()
	reconcileTenant(t, r)

	for _, name := range []string{tenantSelfSignedIssuerName, tenantCAName} {
		getUnstructured(t, c, certManagerIssuerGVK, name)
	}
	ca := getUnstructured(t, c, certManagerCertificateGVK, tenantCAName)
	if isCA, _, _ := unstructured.NestedBool(ca.Object, "spec", "isCA"); !isCA {
		t.Error("the CA of the tenant is not a CA")
	}
	certificate := getUnstructured(t, c, certManagerCertificateGVK, tenantObjectStorageName)
	if names, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames"); !reflect.DeepEqual(names, tenantObjectStorageDNSNames(testTenant)) {
		t.Errorf("dnsNames = %v", names)
	}
	credentials := getSecret(t, c, tenantObjectStorageCredentialsSecret)
	if len(credentials.Data["access-key-id"]) != 20 || len(credentials.Data["secret-access-key"]) != 40 {
		t.Errorf("credentials have %d and %d characters, want 20 and 40", len(credentials.Data["access-key-id"]), len(credentials.Data["secret-access-key"]))
	}
	if state, message := tenantComponentState(t, c, tenantObjectStorageComponentID); state != "waiting" || !strings.Contains(message, "Certificate") {
		t.Errorf("object storage is %q: %q, want waiting for its certificate", state, message)
	}
	if state, message := tenantComponentState(t, c, classisServerComponentID); state != "waiting" || !strings.Contains(message, "waiting for dependencies: "+string(tenantObjectStorageComponentID)) {
		t.Errorf("server is %q: %q, want waiting for the object storage", state, message)
	}

	// cert-manager issues the certificate into its Secret.
	markConditionReady(t, c, certificate)
	if err := c.Create(ctx, secretWithData(tenantObjectStorageTLSSecret, map[string]string{"tls.crt": "crt", "tls.key": "key", "ca.crt": "ca"})); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	claim := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: tenantObjectStorageName}, claim); err != nil {
		t.Fatalf("volume: %v", err)
	}
	if *claim.Spec.StorageClassName != "local-path" || claim.Spec.Resources.Requests.Storage().String() != "10Gi" {
		t.Errorf("volume = %s of %s", *claim.Spec.StorageClassName, claim.Spec.Resources.Requests.Storage())
	}
	deployment := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: tenantObjectStorageName}, deployment); err != nil {
		t.Fatalf("rustfs Deployment: %v", err)
	}
	if image := deployment.Spec.Template.Spec.Containers[0].Image; image != currentImages(t).RustFSImage {
		t.Errorf("rustfs image = %q, want the image of the NetEye version", image)
	}
	for _, name := range []string{tenantObjectStorageName, tenantObjectStorageBucketJob} {
		policy := &unstructured.Unstructured{}
		policy.SetGroupVersionKind(resources.NetworkPolicyGVK())
		if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: name}, policy); err != nil {
			t.Errorf("network policy %q: %v", name, err)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: tenantObjectStorageBucketJob}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Errorf("the bucket Job exists before rustfs is ready: %v", err)
	}

	markTenantDeploymentReady(t, c, tenantObjectStorageName)
	reconcileTenant(t, r)
	job := bucketJob(t, c)
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != currentImages(t).S3ClientImage || envNamed(container, "BUCKET").Value != testTenant {
		t.Errorf("bucket Job = %s for %v", container.Image, envNamed(container, "BUCKET"))
	}
	if job.Spec.Template.Labels[tenantObjectStorageClientLabel] != "true" {
		t.Error("the policy of the store does not admit the bucket Job")
	}
	if state, message := tenantComponentState(t, c, tenantObjectStorageComponentID); state != "waiting" || !strings.Contains(message, "creating the bucket") {
		t.Errorf("object storage is %q: %q, want waiting for the bucket", state, message)
	}

	finishJob(t, c, batchv1.JobComplete)
	reconcileTenant(t, r)
	if state, message := tenantComponentState(t, c, tenantObjectStorageComponentID); state != "" {
		t.Errorf("object storage is %q: %q, want ready", state, message)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: testTenant, Name: classisServerName}, &appsv1.Deployment{}); err != nil {
		t.Errorf("server Deployment once the store is ready: %v", err)
	}

	// The credentials are generated once.
	if again := getSecret(t, c, tenantObjectStorageCredentialsSecret); !reflect.DeepEqual(again.Data, credentials.Data) {
		t.Error("the credentials changed on a later reconciliation")
	}
}

// A failed bucket Job is reported and deleted, so the next reconciliation runs
// it again.
func TestLocalObjectStorageRetriesAFailedBucketJob(t *testing.T) {
	objects := append([]client.Object{plainNamespace(testTenant), localObjectStorageTenant(), netEyeResource()}, classisServerSecrets("admin", "db", "db-ca")...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	markConditionReady(t, c, getUnstructured(t, c, certManagerCertificateGVK, tenantObjectStorageName))
	reconcileTenant(t, r)
	markTenantDeploymentReady(t, c, tenantObjectStorageName)
	reconcileTenant(t, r)

	finishJob(t, c, batchv1.JobFailed)
	reconcileTenantRequestAllowingError(t, r)
	if state, message := tenantComponentState(t, c, tenantObjectStorageComponentID); state != "failed" || !strings.Contains(message, "bucket") {
		t.Errorf("object storage is %q: %q, want failed on the bucket", state, message)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: tenantObjectStorageBucketJob}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Errorf("the failed Job remains: %v", err)
	}
	reconcileTenant(t, r)
	bucketJob(t, c)
}

// rustfs runs as its non-root user on a read-only root filesystem, with its
// console off and its TLS files named as it expects them; credentials and
// certificate versions roll it, because it reads them at startup.
func TestLocalObjectStorageDeploymentIsHardened(t *testing.T) {
	d := tenantObjectStorageDeployment(localObjectStorageTenant(), "image", "versions")
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *d.Spec.Replicas != 1 {
		t.Errorf("strategy = %v replicas = %d, want Recreate with one replica", d.Spec.Strategy.Type, *d.Spec.Replicas)
	}
	pod := d.Spec.Template.Spec
	if *pod.SecurityContext.RunAsUser != rustfsUID || !*pod.SecurityContext.RunAsNonRoot || *pod.AutomountServiceAccountToken {
		t.Errorf("pod security context = %#v", pod.SecurityContext)
	}
	container := pod.Containers[0]
	if !*container.SecurityContext.ReadOnlyRootFilesystem || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("container security context = %#v", container.SecurityContext)
	}
	if env := envNamed(container, "RUSTFS_CONSOLE_ENABLE"); env == nil || env.Value != "false" {
		t.Errorf("console = %#v, want disabled", env)
	}
	tls := volumeNamed(pod, "tls")
	if want := []corev1.KeyToPath{{Key: "tls.crt", Path: "rustfs_cert.pem"}, {Key: "tls.key", Path: "rustfs_key.pem"}}; tls == nil || !reflect.DeepEqual(tls.Secret.Items, want) {
		t.Errorf("tls volume = %#v", tls)
	}
	if probe := container.ReadinessProbe.HTTPGet; probe == nil || probe.Scheme != corev1.URISchemeHTTPS || probe.Path != "/health/ready" {
		t.Errorf("readiness probe = %#v", probe)
	}
	if d.Spec.Template.Annotations[tenantObjectStorageVersionsAnnotation] != "versions" {
		t.Error("the pod template does not carry the versions of its Secrets")
	}
}

// The store admits only the pods marked as its clients, and opens no
// connection itself.
func TestLocalObjectStorageNetworkPolicyAdmitsTheMarkedClients(t *testing.T) {
	spec := tenantObjectStorageNetworkPolicySpec()
	ingress := spec["ingress"].([]any)
	if len(ingress) != 1 || !reflect.DeepEqual(ingress[0].(map[string]any)["from"], []any{map[string]any{"podSelector": podSelector(map[string]string{tenantObjectStorageClientLabel: "true"})}}) {
		t.Errorf("ingress = %v", ingress)
	}
	if egress := spec["egress"].([]any); len(egress) != 0 {
		t.Errorf("egress = %v, want none", egress)
	}
}

// An external object store creates nothing, and its bucket is the one of the
// spec.
func TestExternalObjectStorageCreatesNothing(t *testing.T) {
	objects := append([]client.Object{plainNamespace(testTenant), classisServerTenant(neteye.TenantStorageExternal), netEyeResource()}, externalSecrets()...)
	r, c := newTenantReconciler(t, objects...)
	reconcileTenant(t, r)
	for _, object := range []client.Object{&appsv1.Deployment{}, &corev1.PersistentVolumeClaim{}, &corev1.Secret{}} {
		name := tenantObjectStorageName
		if _, ok := object.(*corev1.Secret); ok {
			name = tenantObjectStorageCredentialsSecret
		}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: testTenant, Name: name}, object); !apierrors.IsNotFound(err) {
			t.Errorf("%T %q exists for an external store: %v", object, name, err)
		}
	}
	if config := mustClassisServerConfig(t, classisServerTenant(neteye.TenantStorageExternal)); !strings.Contains(config, `bucket = "tenant"`) {
		t.Errorf("config:\n%s", config)
	}
}
