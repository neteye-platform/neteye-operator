// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

// TenantStorageProvider says who runs a store of the tenant.
// +kubebuilder:validation:Enum=local;external
type TenantStorageProvider string

const (
	// TenantStorageLocal is a store that the operator runs in the tenant
	// namespace.
	TenantStorageLocal TenantStorageProvider = "local"
	// TenantStorageExternal is a store that the operator does not manage, for
	// example a managed PostgreSQL or an S3 service of a cloud provider.
	TenantStorageExternal TenantStorageProvider = "external"
)

const (
	// DefaultTenantDatabaseReplicas is the number of PostgreSQL instances of a
	// local database.
	DefaultTenantDatabaseReplicas int32 = 2
	// DefaultTenantDatabaseStorageSize is the volume size of each instance.
	DefaultTenantDatabaseStorageSize = "5Gi"
	// DefaultTenantExternalDatabasePort is the port of an external PostgreSQL.
	DefaultTenantExternalDatabasePort int32 = 5432
	// DefaultTenantObjectStorageSize is the volume size of a local object store.
	DefaultTenantObjectStorageSize = "10Gi"
	// DefaultTenantObjectStorageRegion is the region the requests to an
	// external object store are signed for.
	DefaultTenantObjectStorageRegion = "us-east-1"
)

// TenantDatabaseSpec is the PostgreSQL of the tenant: one for the tenant,
// shared by all its services. Each service has a database and a role of its
// own on it, and reaches no other database. The provider cannot change once
// set: moving the records between a local and an external PostgreSQL is a
// migration, not an edit.
// +kubebuilder:validation:XValidation:rule="self.provider == oldSelf.provider",message="provider is immutable"
// +kubebuilder:validation:XValidation:rule="self.provider != 'local' || !has(self.external)",message="external must not be set when provider is local"
// +kubebuilder:validation:XValidation:rule="self.provider != 'external' || has(self.external)",message="external is required when provider is external"
// +kubebuilder:validation:XValidation:rule="self.provider != 'external' || !has(self.local)",message="local must not be set when provider is external"
type TenantDatabaseSpec struct {
	// Provider is local for a CloudNativePG cluster that the operator runs in
	// the tenant namespace, or external for a PostgreSQL that someone else runs.
	// +kubebuilder:validation:Required
	Provider TenantStorageProvider `json:"provider"`

	// Local tunes the CloudNativePG cluster of the tenant.
	// +kubebuilder:validation:Optional
	Local *TenantLocalDatabaseSpec `json:"local,omitempty"`

	// External is where the external PostgreSQL lives.
	// +kubebuilder:validation:Optional
	External *TenantExternalDatabaseSpec `json:"external,omitempty"`
}

// TenantLocalDatabaseSpec tunes the CloudNativePG cluster of the tenant. The
// image is the one the operator ships for the NetEye version.
type TenantLocalDatabaseSpec struct {
	// Replicas is the number of PostgreSQL instances. The instances never share
	// a node (required pod anti-affinity on the node name): with two replicas on
	// a cluster that has a single worker node, the second one stays Pending, so
	// set it to 1 there.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Replicas int32 `json:"replicas,omitempty"`

	// StorageSize is the size of the volume of each instance.
	// +kubebuilder:default="5Gi"
	// +kubebuilder:validation:MaxLength=32
	StorageSize string `json:"storageSize,omitempty"`

	// StorageClassName is the storage class of the volumes. When empty, the
	// default class is used.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=253
	StorageClassName string `json:"storageClassName,omitempty"`
}

// TenantExternalDatabaseSpec is where the external PostgreSQL of the tenant
// lives. The services connect with TLS and verify the certificate.
type TenantExternalDatabaseSpec struct {
	// Host is the DNS name or the IP address of the PostgreSQL.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host"`

	// Port is the port of the PostgreSQL.
	// +kubebuilder:default=5432
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// CASecret references the Secret key of the tenant namespace that holds the
	// PEM of the CA that signs the certificate of the PostgreSQL.
	// +kubebuilder:validation:Required
	CASecret NetEyeSecretKeySelector `json:"caSecret"`
}

// TenantObjectStorageSpec is the S3 object store of the tenant, shared by its
// services: each one owns a key prefix of the bucket. The provider cannot
// change once set.
// +kubebuilder:validation:XValidation:rule="self.provider == oldSelf.provider",message="provider is immutable"
// +kubebuilder:validation:XValidation:rule="self.provider != 'local' || !has(self.external)",message="external must not be set when provider is local"
// +kubebuilder:validation:XValidation:rule="self.provider != 'external' || has(self.external)",message="external is required when provider is external"
// +kubebuilder:validation:XValidation:rule="self.provider != 'external' || !has(self.local)",message="local must not be set when provider is external"
type TenantObjectStorageSpec struct {
	// Provider is local for a single rustfs instance that the operator runs in
	// the tenant namespace, or external for an S3 service that someone else
	// runs.
	// +kubebuilder:validation:Required
	Provider TenantStorageProvider `json:"provider"`

	// Local tunes the rustfs instance of the tenant.
	// +kubebuilder:validation:Optional
	Local *TenantLocalObjectStorageSpec `json:"local,omitempty"`

	// External is the bucket of the external S3 service.
	// +kubebuilder:validation:Optional
	External *TenantExternalObjectStorageSpec `json:"external,omitempty"`
}

// TenantLocalObjectStorageSpec tunes the rustfs instance of the tenant: one
// replica on one volume, so the store is unavailable while its node is down.
// The image is the one the operator ships for the NetEye version.
type TenantLocalObjectStorageSpec struct {
	// StorageSize is the size of the volume.
	// +kubebuilder:default="10Gi"
	// +kubebuilder:validation:MaxLength=32
	StorageSize string `json:"storageSize,omitempty"`

	// StorageClassName is the storage class of the volume. When empty, the
	// default class is used.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=253
	StorageClassName string `json:"storageClassName,omitempty"`
}

// TenantExternalObjectStorageSpec is the bucket of an external S3 service. The
// bucket must exist: the operator creates nothing on the service.
type TenantExternalObjectStorageSpec struct {
	// Endpoint is the https URL of the S3 service. When a private CA signs its
	// certificate, the tenant's spec.caBundle holds that CA.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https://.+`
	// +kubebuilder:validation:MaxLength=512
	Endpoint string `json:"endpoint"`

	// Region is the region the requests are signed for. It must be the region
	// of the bucket on a service that checks it.
	// +kubebuilder:default=us-east-1
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Region string `json:"region,omitempty"`

	// Bucket is the name of the bucket.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	Bucket string `json:"bucket"`

	// CredentialsSecretName names a Secret of the tenant namespace with the
	// access-key-id and secret-access-key keys. When empty, the services use
	// the workload identity of their platform.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=253
	CredentialsSecretName string `json:"credentialsSecretName,omitempty"`
}

// IsLocal tells whether the operator runs the database.
func (s *TenantDatabaseSpec) IsLocal() bool {
	return s != nil && s.Provider == TenantStorageLocal
}

// EffectiveReplicas returns the number of instances of a local database.
func (s *TenantDatabaseSpec) EffectiveReplicas() int32 {
	if s == nil || s.Local == nil || s.Local.Replicas == 0 {
		return DefaultTenantDatabaseReplicas
	}
	return s.Local.Replicas
}

// EffectiveStorageSize returns the volume size of each local instance.
func (s *TenantDatabaseSpec) EffectiveStorageSize() string {
	if s == nil || s.Local == nil || s.Local.StorageSize == "" {
		return DefaultTenantDatabaseStorageSize
	}
	return s.Local.StorageSize
}

// EffectiveStorageClassName returns the storage class of the local volumes,
// empty for the default class.
func (s *TenantDatabaseSpec) EffectiveStorageClassName() string {
	if s == nil || s.Local == nil {
		return ""
	}
	return s.Local.StorageClassName
}

// EffectivePort returns the port of an external database.
func (s *TenantExternalDatabaseSpec) EffectivePort() int32 {
	if s == nil || s.Port == 0 {
		return DefaultTenantExternalDatabasePort
	}
	return s.Port
}

// IsLocal tells whether the operator runs the object store.
func (s *TenantObjectStorageSpec) IsLocal() bool {
	return s != nil && s.Provider == TenantStorageLocal
}

// EffectiveStorageSize returns the volume size of a local object store.
func (s *TenantObjectStorageSpec) EffectiveStorageSize() string {
	if s == nil || s.Local == nil || s.Local.StorageSize == "" {
		return DefaultTenantObjectStorageSize
	}
	return s.Local.StorageSize
}

// EffectiveStorageClassName returns the storage class of the local volume,
// empty for the default class.
func (s *TenantObjectStorageSpec) EffectiveStorageClassName() string {
	if s == nil || s.Local == nil {
		return ""
	}
	return s.Local.StorageClassName
}

// EffectiveRegion returns the region of an external object store.
func (s *TenantExternalObjectStorageSpec) EffectiveRegion() string {
	if s == nil || s.Region == "" {
		return DefaultTenantObjectStorageRegion
	}
	return s.Region
}
