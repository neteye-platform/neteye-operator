// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	tenantObjectStorageComponentID componentID = "object-storage"

	// tenantObjectStorageName names the Deployment, the Service, the volume and
	// the policy of the local object store, and is the host label of its DNS
	// name.
	tenantObjectStorageName = "neteye-tenant-s3"
	tenantObjectStoragePort = 9000
	// tenantObjectStorageCredentialsSecret holds the root key pair of the local
	// object store, generated once by the operator.
	tenantObjectStorageCredentialsSecret = "neteye-tenant-s3-credentials"
	// tenantObjectStorageTLSSecret holds the server certificate of the local
	// object store, with the CA of the tenant in its ca.crt key.
	tenantObjectStorageTLSSecret = "neteye-tenant-s3-tls"
	// tenantObjectStorageBucketJob creates the bucket of the local object store.
	tenantObjectStorageBucketJob = "neteye-tenant-s3-bucket"
	// tenantObjectStorageRegion is the region the requests to the local object
	// store are signed for: rustfs takes any.
	tenantObjectStorageRegion = "us-east-1"

	// tenantCAName names the self-signed CA of the tenant: its Certificate, its
	// Secret and its Issuer. It signs the certificates of the internal services
	// of the tenant.
	tenantCAName = "neteye-tenant-ca"
	// tenantSelfSignedIssuerName names the issuer that signs the CA of the
	// tenant.
	tenantSelfSignedIssuerName = "neteye-tenant-selfsigned"

	// tenantObjectStorageClientLabel marks the pods of the services that reach
	// the local object store: its network policy admits them and no others.
	tenantObjectStorageClientLabel = "neteye.cloud/object-storage-client"

	// rustfsUID is the UID of the rustfs user of its image.
	rustfsUID int64 = 10001
	// s3ClientUID is a non-root UID for the S3 client, whose image runs as root.
	s3ClientUID int64 = 65532

	tenantObjectStorageVersionsAnnotation = "neteye.cloud/secrets-resource-versions"
)

var (
	certManagerIssuerGVK      = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}
	certManagerCertificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
)

// tenantObjectStorageEndpoint is where the services reach the object store of
// the tenant, and how they authenticate and verify it.
type tenantObjectStorageEndpoint struct {
	Endpoint, Region, Bucket string
	// CredentialsSecret holds the access-key-id and secret-access-key keys. It
	// is empty for an external store reached with the workload identity of the
	// platform.
	CredentialsSecret string
	// CASecret is the Secret key of the CA of a local store. An external store
	// is verified against the CA bundle of the tenant, when it has one.
	CASecret *neteye.NetEyeSecretKeySelector
	Local    bool
}

// tenantObjectStorageBucket is the bucket of the local object store: the name
// of the tenant, which is unique and a valid bucket name.
func tenantObjectStorageBucket(tenant *neteye.NetEyeTenant) string { return tenant.Name }

func tenantObjectStorageEndpointOf(tenant *neteye.NetEyeTenant) tenantObjectStorageEndpoint {
	storage := tenant.Spec.ObjectStorage
	if storage.IsLocal() {
		return tenantObjectStorageEndpoint{
			Endpoint:          fmt.Sprintf("https://%s.%s.svc:%d", tenantObjectStorageName, tenant.Namespace, tenantObjectStoragePort),
			Region:            tenantObjectStorageRegion,
			Bucket:            tenantObjectStorageBucket(tenant),
			CredentialsSecret: tenantObjectStorageCredentialsSecret,
			CASecret:          &neteye.NetEyeSecretKeySelector{Name: tenantObjectStorageTLSSecret, Key: "ca.crt"},
			Local:             true,
		}
	}
	external := storage.External
	if external == nil {
		return tenantObjectStorageEndpoint{}
	}
	return tenantObjectStorageEndpoint{Endpoint: external.Endpoint, Region: external.EffectiveRegion(), Bucket: external.Bucket, CredentialsSecret: external.CredentialsSecretName}
}

func tenantObjectStorageSelector() map[string]string {
	return map[string]string{"app.kubernetes.io/name": tenantObjectStorageName}
}

func tenantObjectStorageBucketSelector() map[string]string {
	return map[string]string{"app.kubernetes.io/name": tenantObjectStorageBucketJob}
}

// tenantObjectStorageDNSNames are the names the services use for the local
// object store.
func tenantObjectStorageDNSNames(namespace string) []string {
	return []string{tenantObjectStorageName, tenantObjectStorageName + "." + namespace, tenantObjectStorageName + "." + namespace + ".svc"}
}

// tenantCASpecs are the self-signed issuer, the CA certificate and the CA
// issuer of the tenant, in the order they depend on each other.
func tenantCASpecs() []resources.ObjectDefinition {
	return []resources.ObjectDefinition{
		{GVK: certManagerIssuerGVK, Name: tenantSelfSignedIssuerName, Spec: map[string]any{"selfSigned": map[string]any{}}},
		{GVK: certManagerCertificateGVK, Name: tenantCAName, Spec: map[string]any{
			"isCA":       true,
			"commonName": tenantCAName,
			"secretName": tenantCAName,
			"privateKey": map[string]any{"algorithm": "ECDSA", "size": int64(256)},
			"issuerRef":  map[string]any{"name": tenantSelfSignedIssuerName, "kind": "Issuer", "group": "cert-manager.io"},
		}},
		{GVK: certManagerIssuerGVK, Name: tenantCAName, Spec: map[string]any{"ca": map[string]any{"secretName": tenantCAName}}},
	}
}

func tenantObjectStorageCertificateSpec(namespace string) map[string]any {
	return map[string]any{
		"secretName": tenantObjectStorageTLSSecret,
		"commonName": tenantObjectStorageName,
		"dnsNames":   stringsToAny(tenantObjectStorageDNSNames(namespace)),
		"usages":     []any{"server auth"},
		"privateKey": map[string]any{"algorithm": "ECDSA", "size": int64(256), "rotationPolicy": "Always"},
		"issuerRef":  map[string]any{"name": tenantCAName, "kind": "Issuer", "group": "cert-manager.io"},
	}
}

func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// tenantObjectStorageVolume is the volume of the local object store. Its spec
// cannot change once bound, so it is created once and left alone.
func tenantObjectStorageVolume(tenant *neteye.NetEyeTenant, owner metav1.OwnerReference) *corev1.PersistentVolumeClaim {
	storage := tenant.Spec.ObjectStorage
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: tenantObjectStorageName, Namespace: tenant.Namespace, Labels: tenantObjectStorageSelector(), OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(storage.EffectiveStorageSize())}},
		},
	}
	if class := storage.EffectiveStorageClassName(); class != "" {
		claim.Spec.StorageClassName = ptr.To(class)
	}
	return claim
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}}
}

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		ReadOnlyRootFilesystem:   ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func nonRootPodSecurityContext(uid int64) *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(uid),
		RunAsGroup:     ptr.To(uid),
		FSGroup:        ptr.To(uid),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// tenantObjectStorageDeployment runs one rustfs instance on its volume: one
// replica and the Recreate strategy, because the volume is ReadWriteOnce and
// local to a node. The console is off; rustfs serves the S3 API over TLS with
// the certificate of the tenant CA. versions names the resource versions of the
// credentials and of the certificate, so that a rotation restarts rustfs, which
// reads them at startup.
func tenantObjectStorageDeployment(tenant *neteye.NetEyeTenant, image, versions string) *appsv1.Deployment {
	labels := tenantObjectStorageSelector()
	tls := corev1.Volume{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName:  tenantObjectStorageTLSSecret,
		DefaultMode: ptr.To(classisServerSecretMode),
		Items:       []corev1.KeyToPath{{Key: "tls.crt", Path: "rustfs_cert.pem"}, {Key: "tls.key", Path: "rustfs_key.pem"}},
	}}}
	probe := func(handler corev1.ProbeHandler) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 10, TimeoutSeconds: 2, FailureThreshold: 3}
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: tenantObjectStorageName, Namespace: tenant.Namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{tenantObjectStorageVersionsAnnotation: versions}},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  ptr.To(false),
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					SecurityContext:               nonRootPodSecurityContext(rustfsUID),
					Containers: []corev1.Container{{
						Name:  "rustfs",
						Image: image,
						Ports: []corev1.ContainerPort{{Name: "s3", ContainerPort: tenantObjectStoragePort}},
						Env: []corev1.EnvVar{
							secretEnv("RUSTFS_ACCESS_KEY", tenantObjectStorageCredentialsSecret, "access-key-id"),
							secretEnv("RUSTFS_SECRET_KEY", tenantObjectStorageCredentialsSecret, "secret-access-key"),
							{Name: "RUSTFS_VOLUMES", Value: "/data"},
							{Name: "RUSTFS_ADDRESS", Value: fmt.Sprintf(":%d", tenantObjectStoragePort)},
							{Name: "RUSTFS_TLS_PATH", Value: "/tls"},
							{Name: "RUSTFS_CONSOLE_ENABLE", Value: "false"},
							{Name: "RUSTFS_OBS_LOG_DIRECTORY", Value: "/logs"},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/data"},
							{Name: "tls", MountPath: "/tls", ReadOnly: true},
							{Name: "logs", MountPath: "/logs"},
							{Name: "tmp", MountPath: "/tmp"},
						},
						ReadinessProbe:  probe(corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health/ready", Scheme: corev1.URISchemeHTTPS, Port: intstr.FromString("s3")}}),
						LivenessProbe:   probe(corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("s3")}}),
						SecurityContext: restrictedContainerSecurityContext(),
					}},
					Volumes: []corev1.Volume{
						{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: tenantObjectStorageName}}},
						tls,
						{Name: "logs", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

func tenantObjectStorageService(tenant *neteye.NetEyeTenant) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: tenantObjectStorageName, Namespace: tenant.Namespace, Labels: tenantObjectStorageSelector()},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: tenantObjectStorageSelector(),
			Ports:    []corev1.ServicePort{{Name: "s3", Port: tenantObjectStoragePort, TargetPort: intstr.FromString("s3"), Protocol: corev1.ProtocolTCP}},
		},
	}
}

// tenantObjectStorageNetworkPolicySpec admits the pods marked as object store
// clients to the S3 port, and lets rustfs open no connection: it needs none.
func tenantObjectStorageNetworkPolicySpec() map[string]any {
	return map[string]any{
		"podSelector": podSelector(tenantObjectStorageSelector()),
		"policyTypes": []any{"Ingress", "Egress"},
		"ingress": []any{map[string]any{
			"from":  []any{map[string]any{"podSelector": podSelector(map[string]string{tenantObjectStorageClientLabel: "true"})}},
			"ports": resources.NetworkPolicyPorts("TCP", tenantObjectStoragePort),
		}},
		"egress": []any{},
	}
}

// tenantObjectStorageEgress is the egress rule of a client of the local object
// store.
func tenantObjectStorageEgress() map[string]any {
	return map[string]any{
		"to":    []any{map[string]any{"podSelector": podSelector(tenantObjectStorageSelector())}},
		"ports": resources.NetworkPolicyPorts("TCP", tenantObjectStoragePort),
	}
}

func tenantObjectStorageBucketNetworkPolicySpec() map[string]any {
	return map[string]any{
		"podSelector": podSelector(tenantObjectStorageBucketSelector()),
		"policyTypes": []any{"Ingress", "Egress"},
		"ingress":     []any{},
		"egress":      []any{dnsEgress, tenantObjectStorageEgress()},
	}
}

// tenantObjectStorageBucketJobSpec creates the bucket of the local object
// store when it is missing, and does nothing when it exists.
func tenantObjectStorageBucketJobSpec(tenant *neteye.NetEyeTenant, image string, owner metav1.OwnerReference) *batchv1.Job {
	labels := tenantObjectStorageBucketSelector()
	labels[tenantObjectStorageClientLabel] = "true"
	endpoint := tenantObjectStorageEndpointOf(tenant)
	script := `aws s3api head-bucket --bucket "$BUCKET" || aws s3api create-bucket --bucket "$BUCKET"`
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: tenantObjectStorageBucketJob, Namespace: tenant.Namespace, Labels: tenantObjectStorageBucketSelector(), OwnerReferences: []metav1.OwnerReference{owner}},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(6)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext:              nonRootPodSecurityContext(s3ClientUID),
					Containers: []corev1.Container{{
						Name:    "create-bucket",
						Image:   image,
						Command: []string{"/bin/sh", "-c", script},
						Env: []corev1.EnvVar{
							{Name: "BUCKET", Value: endpoint.Bucket},
							{Name: "AWS_ENDPOINT_URL", Value: endpoint.Endpoint},
							{Name: "AWS_DEFAULT_REGION", Value: endpoint.Region},
							{Name: "AWS_CA_BUNDLE", Value: "/ca/ca.crt"},
							{Name: "HOME", Value: "/tmp"},
							secretEnv("AWS_ACCESS_KEY_ID", tenantObjectStorageCredentialsSecret, "access-key-id"),
							secretEnv("AWS_SECRET_ACCESS_KEY", tenantObjectStorageCredentialsSecret, "secret-access-key"),
						},
						VolumeMounts:    []corev1.VolumeMount{{Name: "ca", MountPath: "/ca", ReadOnly: true}, {Name: "tmp", MountPath: "/tmp"}},
						SecurityContext: restrictedContainerSecurityContext(),
					}},
					Volumes: []corev1.Volume{
						{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: tenantObjectStorageTLSSecret, DefaultMode: ptr.To(classisServerSecretMode), Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

// reconcileTenantObjectStorage runs the local object store of the tenant: the
// CA of the tenant and the certificate of the store, its credentials, its
// volume, rustfs and its Service and policy, and the bucket. An external store
// creates nothing: its bucket and its credentials are the business of whoever
// runs it.
func (r *NetEyeTenantReconciler) reconcileTenantObjectStorage(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	if !tenant.Spec.ObjectStorage.IsLocal() {
		return readyResult(tenantObjectStorageComponentID, "External", "The object store is external")
	}
	id := tenantObjectStorageComponentID
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	images, err := r.tenantImages(ctx)
	if err != nil {
		return r.imagesResult(id, err)
	}
	if err := r.ensureObjectStorageCredentials(ctx, tenant.Namespace, owner); err != nil {
		return degradedResult(id, "CredentialsFailed", err.Error(), r.failureRequeue(), err)
	}
	certificates := append(tenantCASpecs(), resources.ObjectDefinition{GVK: certManagerCertificateGVK, Name: tenantObjectStorageName, Spec: tenantObjectStorageCertificateSpec(tenant.Namespace)})
	for _, object := range certificates {
		object.Namespace, object.Owner = tenant.Namespace, &owner
		if _, err := resources.Apply(ctx, r.Client, object); err != nil {
			return degradedResult(id, "CertificateFailed", fmt.Sprintf("failed to apply the cert-manager %s %q: %v", object.GVK.Kind, object.Name, err), r.failureRequeue(), err)
		}
	}
	if ready, message, err := resources.IsCertificateReady(ctx, r.Client, tenant.Namespace, tenantObjectStorageName); err != nil {
		return degradedResult(id, "CertificateFailed", err.Error(), r.failureRequeue(), err)
	} else if !ready {
		return progressingResult(id, "WaitingForCertificate", message, r.waitForProgressingRequeue())
	}
	if err := r.Create(ctx, tenantObjectStorageVolume(tenant, owner)); err != nil && !apierrors.IsAlreadyExists(err) {
		return degradedResult(id, "VolumeFailed", fmt.Sprintf("failed to create the volume: %v", err), r.failureRequeue(), err)
	}
	if err := resources.EnsureService(ctx, r.Client, tenantObjectStorageService(tenant), owner); err != nil {
		return degradedResult(id, "ServiceFailed", err.Error(), r.failureRequeue(), err)
	}
	for name, spec := range map[string]map[string]any{tenantObjectStorageName: tenantObjectStorageNetworkPolicySpec(), tenantObjectStorageBucketJob: tenantObjectStorageBucketNetworkPolicySpec()} {
		if err := resources.EnsureNetworkPolicy(ctx, r.Client, tenant.Namespace, name, spec, owner); err != nil {
			return degradedResult(id, "NetworkPolicyFailed", err.Error(), r.failureRequeue(), err)
		}
	}
	versions, err := r.secretVersions(ctx, tenant.Namespace, []string{tenantObjectStorageCredentialsSecret, tenantObjectStorageTLSSecret}, nil)
	if err != nil {
		return degradedResult(id, "SecretCheckFailed", err.Error(), r.failureRequeue(), err)
	}
	if err := resources.EnsureDeployment(ctx, r.Client, tenantObjectStorageDeployment(tenant, images.RustFSImage, versions), owner); err != nil {
		return degradedResult(id, "DeploymentFailed", err.Error(), r.failureRequeue(), err)
	}
	if ready, message, err := resources.IsDeploymentReady(ctx, r.Client, tenant.Namespace, tenantObjectStorageName); err != nil {
		return degradedResult(id, "DeploymentFailed", err.Error(), r.failureRequeue(), err)
	} else if !ready {
		return progressingResult(id, "WaitingForDeployment", message, r.waitForProgressingRequeue())
	}
	return r.ensureObjectStorageBucket(ctx, tenant, images.S3ClientImage, owner)
}

// ensureObjectStorageBucket runs the Job that creates the bucket and reports
// it. A failed Job is deleted, so that the next reconciliation runs it again.
func (r *NetEyeTenantReconciler) ensureObjectStorageBucket(ctx context.Context, tenant *neteye.NetEyeTenant, image string, owner metav1.OwnerReference) (componentResult, error) {
	id := tenantObjectStorageComponentID
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: tenantObjectStorageBucketJob}, job)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, tenantObjectStorageBucketJobSpec(tenant, image, owner)); err != nil && !apierrors.IsAlreadyExists(err) {
			return degradedResult(id, "BucketFailed", fmt.Sprintf("failed to create the bucket Job: %v", err), r.failureRequeue(), err)
		}
		return progressingResult(id, "WaitingForBucket", "creating the bucket "+tenantObjectStorageBucket(tenant), r.waitForProgressingRequeue())
	} else if err != nil {
		return degradedResult(id, "BucketFailed", err.Error(), r.failureRequeue(), err)
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			return readyResult(id, "Available", "The object store and its bucket are ready")
		case batchv1.JobFailed:
			if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
				return degradedResult(id, "BucketFailed", err.Error(), r.failureRequeue(), err)
			}
			err := fmt.Errorf("the Job %q failed to create the bucket: %s", tenantObjectStorageBucketJob, condition.Message)
			return degradedResult(id, "BucketFailed", err.Error(), r.failureRequeue(), err)
		}
	}
	return progressingResult(id, "WaitingForBucket", "creating the bucket "+tenantObjectStorageBucket(tenant), r.waitForProgressingRequeue())
}

// ensureObjectStorageCredentials creates the root key pair of the local object
// store once and never overwrites it.
func (r *NetEyeTenantReconciler) ensureObjectStorageCredentials(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	existing, err := r.getSecretIfPresent(ctx, namespace, tenantObjectStorageCredentialsSecret)
	if err != nil {
		return err
	}
	if existing != nil {
		if len(existing.Data["access-key-id"]) == 0 || len(existing.Data["secret-access-key"]) == 0 {
			return fmt.Errorf("the Secret %q has no access-key-id or secret-access-key: delete it to have it generated again", tenantObjectStorageCredentialsSecret)
		}
		return nil
	}
	accessKey, err := randomPassword(20)
	if err != nil {
		return err
	}
	secretKey, err := randomPassword(40)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: tenantObjectStorageCredentialsSecret, Labels: tenantObjectStorageSelector(), OwnerReferences: []metav1.OwnerReference{owner}},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"access-key-id": []byte(accessKey), "secret-access-key": []byte(secretKey)},
	}
	if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the Secret %q: %w", tenantObjectStorageCredentialsSecret, err)
	}
	return nil
}
