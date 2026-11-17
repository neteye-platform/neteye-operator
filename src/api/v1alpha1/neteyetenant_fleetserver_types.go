// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

// TenantDatabaseType says where the PostgreSQL of the tenant lives.
// +kubebuilder:validation:Enum=local;external
type TenantDatabaseType string

const (
	// TenantDatabaseLocal is a CloudNativePG cluster that the operator creates in
	// the tenant namespace.
	TenantDatabaseLocal TenantDatabaseType = "local"
	// TenantDatabaseExternal is a PostgreSQL that the operator does not manage
	// (for example Aurora). No database is created in the tenant namespace.
	TenantDatabaseExternal TenantDatabaseType = "external"
)

// TenantDatabaseSpec says where the PostgreSQL of the tenant lives. The type
// cannot change once set: moving the records between a local and an external
// database is a migration, not an edit.
// +kubebuilder:validation:XValidation:rule="self.type != 'local' || (!has(self.hostname) && !has(self.port))",message="hostname and port must not be set when type is local"
// +kubebuilder:validation:XValidation:rule="self.type != 'external' || has(self.hostname)",message="hostname is required when type is external"
// +kubebuilder:validation:XValidation:rule="self.type == oldSelf.type",message="type is immutable"
type TenantDatabaseSpec struct {
	// Type is local for a CloudNativePG cluster of the tenant, or external for a
	// PostgreSQL that someone else runs.
	// +kubebuilder:validation:Required
	Type TenantDatabaseType `json:"type"`

	// Hostname is the host of the external PostgreSQL. Only for type external.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Hostname string `json:"hostname,omitempty"`

	// Port is the port of the external PostgreSQL. Only for type external.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// IssuerReference names the cert-manager issuer that signs the internal
// certificates of the tenant. The issuer lives in the tenant namespace unless
// its kind is ClusterIssuer.
type IssuerReference struct {
	// Name of the issuer.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Kind of the issuer.
	// +kubebuilder:default=Issuer
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	Kind string `json:"kind,omitempty"`

	// Group of the issuer.
	// +kubebuilder:default=cert-manager.io
	// +kubebuilder:validation:MaxLength=253
	Group string `json:"group,omitempty"`
}

// ConfigMapKeyReference points to a key of a ConfigMap of the tenant namespace.
type ConfigMapKeyReference struct {
	// Name of the ConfigMap.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Key of the ConfigMap that holds the PEM bundle.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// NetEyeFleetServerSpec defines the Classis fleet server of a tenant: one
// instance, with its records in PostgreSQL and its package artifacts in an S3
// bucket, so that the pod writes nothing to its filesystem.
type NetEyeFleetServerSpec struct {
	// Enabled runs the server. When false, the operator removes the Deployment
	// and leaves the database, the Service and the policies in place.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Image is the container image of the Classis server.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Image string `json:"image"`

	// Database tunes the database of the server. Where it lives is the
	// tenant's spec.database.
	// +kubebuilder:validation:Optional
	Database *FleetServerDatabaseSpec `json:"database,omitempty"`

	// Blobs is the S3 bucket that holds the package artifacts.
	// +kubebuilder:validation:Required
	Blobs FleetServerBlobsSpec `json:"blobs"`

	// AdminUsersSecretName names a Secret whose basic-users key holds the file
	// of the operator plane users, with their Argon2id password hashes.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AdminUsersSecretName string `json:"adminUsersSecretName"`

	// LogLevel is the RUST_LOG filter of the server.
	// +kubebuilder:default=info
	// +kubebuilder:validation:MaxLength=256
	LogLevel string `json:"logLevel,omitempty"`

	// LogFormat is the format of the server log: json for a log collector, text
	// for a person.
	// +kubebuilder:default=json
	// +kubebuilder:validation:Enum=json;text
	LogFormat string `json:"logFormat,omitempty"`

	// Network narrows the network policies of the server.
	// +kubebuilder:validation:Optional
	Network FleetServerNetworkSpec `json:"network,omitempty"`
}

// FleetServerDatabaseSpec tunes the database of the Classis server. Which of the
// two applies follows spec.database.type of the tenant.
type FleetServerDatabaseSpec struct {
	// Internal tunes the CloudNativePG cluster of a local database.
	// +kubebuilder:validation:Optional
	Internal *FleetServerInternalDatabaseSpec `json:"internal,omitempty"`

	// External names the database and the credentials on an external
	// PostgreSQL. Required when spec.database.type is external.
	// +kubebuilder:validation:Optional
	External *FleetServerExternalDatabaseSpec `json:"external,omitempty"`
}

// FleetServerInternalDatabaseSpec tunes the CloudNativePG cluster of a tenant.
type FleetServerInternalDatabaseSpec struct {
	// Instances is the number of PostgreSQL instances. The instances never
	// share a node (required pod anti-affinity on the node name): with two
	// instances on a cluster that has a single worker node, the second instance
	// stays Pending, so set it to 1 there.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Instances int32 `json:"instances,omitempty"`

	// ImageName is the PostgreSQL image of the cluster.
	// +kubebuilder:default="ghcr.io/cloudnative-pg/postgresql:18"
	// +kubebuilder:validation:MaxLength=512
	ImageName string `json:"imageName,omitempty"`

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

// FleetServerExternalDatabaseSpec names the database and the credentials on the
// external PostgreSQL of spec.database. The server connects with TLS 1.3 and
// verifies the certificate.
type FleetServerExternalDatabaseSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	DBName string `json:"dbname"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	User string `json:"user"`

	// CredentialsSecretName names a Secret with the password key, and the ca.crt
	// key of the CA that signs the server certificate.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	CredentialsSecretName string `json:"credentialsSecretName"`
}

// FleetServerBlobsSpec is the S3 bucket of the package artifacts.
type FleetServerBlobsSpec struct {
	// Endpoint is the https URL of the S3 service. When a private CA signs its
	// certificate, the tenant's spec.caBundle holds that CA.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https://.+`
	// +kubebuilder:validation:MaxLength=512
	Endpoint string `json:"endpoint"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	Bucket string `json:"bucket"`

	// +kubebuilder:default=us-east-1
	// +kubebuilder:validation:MaxLength=63
	Region string `json:"region,omitempty"`

	// Prefix is the key prefix the server owns. At startup the server deletes
	// the objects under it that no package names, so no other writer may share
	// it.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=512
	Prefix string `json:"prefix,omitempty"`

	// CredentialsSecretName names a Secret with the access-key-id and
	// secret-access-key keys.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	CredentialsSecretName string `json:"credentialsSecretName"`
}

// FleetServerNetworkSpec narrows the network policies of the server. The
// policies are native (networking.k8s.io/v1) and follow the rule of the other
// components: where a native policy cannot name the peer (the Kubernetes API,
// the nodes, a service outside the cluster), the rule is open to any peer on
// its ports.
type FleetServerNetworkSpec struct {
	// AdminIngressCIDRs, when set, are the only sources of the operator plane
	// (4321). When empty, any source reaches it: a native policy cannot select
	// the nodes of the PCS proxy, so the proxy, TLS and the Basic credentials
	// of the plane limit who gets in.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	AdminIngressCIDRs []string `json:"adminIngressCIDRs,omitempty"`
}
