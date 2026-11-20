// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

const (
	// DefaultClassisServerDBName is the database of the Classis Server on the
	// PostgreSQL of the tenant, and the name of its role on a local one.
	DefaultClassisServerDBName = "classis"
	// DefaultClassisServerObjectStoragePrefix is the key prefix the Classis
	// Server owns in the bucket of the tenant.
	DefaultClassisServerObjectStoragePrefix = "classis/"
)

// NetEyeClassisServerSpec defines the Classis Server of a tenant: one
// instance, with its records in the PostgreSQL of the tenant and its package
// artifacts in the object store of the tenant, so that the pod writes nothing
// to its filesystem. The image is the one the operator ships for the NetEye
// version.
type NetEyeClassisServerSpec struct {
	// Enabled runs the server. When false, the operator removes the Deployment
	// and leaves the database, the Service and the policies in place.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// DBConnection is how the server connects to the PostgreSQL of the tenant.
	// With a local database the operator creates the database and its role,
	// and generates the credentials in the Secret classis-db-credentials, so
	// only dbName may be set. With an external database the database and the
	// role must exist, and usernameSecret and passwordSecret are required.
	// +kubebuilder:validation:Optional
	DBConnection *ClassisServerDBConnectionSpec `json:"dbConnection,omitempty"`

	// ObjectStorage is the part of the object store of the tenant that the
	// server owns.
	// +kubebuilder:validation:Optional
	ObjectStorage *ClassisServerObjectStorageSpec `json:"objectStorage,omitempty"`

	// AdminUsersSecretName names a Secret whose basic-users key holds the file
	// of the operator plane users, with their Argon2id password hashes. When it
	// is empty the operator generates one administrator, `admin`: the Secret
	// `classis-server-admin` holds its `username` and `password`, and the Secret
	// `classis-server-admin-users` holds the hash the server reads. Both are
	// created once, never overwritten and never rotated, and they survive the
	// deletion of the tenant resource.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AdminUsersSecretName string `json:"adminUsersSecretName,omitempty"`

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
	Network ClassisServerNetworkSpec `json:"network,omitempty"`
}

// ClassisServerDBConnectionSpec is how the Classis Server connects to the
// PostgreSQL of the tenant: the host, the port and the CA come from
// spec.database.
type ClassisServerDBConnectionSpec struct {
	// DBName is the database of the server. On a local database it is also the
	// name of the role the operator creates.
	// +kubebuilder:default=classis
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]*$`
	DBName string `json:"dbName,omitempty"`

	// UsernameSecret references the Secret key that holds the user name. Only
	// for an external database.
	// +kubebuilder:validation:Optional
	UsernameSecret *NetEyeSecretKeySelector `json:"usernameSecret,omitempty"`

	// PasswordSecret references the Secret key that holds the password. Only
	// for an external database.
	// +kubebuilder:validation:Optional
	PasswordSecret *NetEyeSecretKeySelector `json:"passwordSecret,omitempty"`
}

// ClassisServerObjectStorageSpec is the part of the bucket of the tenant that
// the Classis Server owns.
type ClassisServerObjectStorageSpec struct {
	// Prefix is the key prefix the server owns, ending in a slash. At startup
	// the server deletes the objects under it that no package names, so no
	// other writer may share it.
	// +kubebuilder:default="classis/"
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[^/].*/$`
	Prefix string `json:"prefix,omitempty"`
}

// ClassisServerNetworkSpec narrows the network policies of the server. The
// policies are native (networking.k8s.io/v1) and follow the rule of the other
// components: where a native policy cannot name the peer (the Kubernetes API,
// the nodes, a service outside the cluster), the rule is open to any peer on
// its ports.
type ClassisServerNetworkSpec struct {
	// AdminIngressCIDRs, when set, are the only sources of the operator plane
	// (4321). When empty, any source reaches it: a native policy cannot select
	// the nodes of the PCS proxy, so the proxy, TLS and the Basic credentials
	// of the plane limit who gets in.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	AdminIngressCIDRs []string `json:"adminIngressCIDRs,omitempty"`
}

// EffectiveDBName returns the database of the Classis Server.
func (s *NetEyeClassisServerSpec) EffectiveDBName() string {
	if s == nil || s.DBConnection == nil || s.DBConnection.DBName == "" {
		return DefaultClassisServerDBName
	}
	return s.DBConnection.DBName
}

// EffectiveObjectStoragePrefix returns the key prefix the Classis Server owns.
func (s *NetEyeClassisServerSpec) EffectiveObjectStoragePrefix() string {
	if s == nil || s.ObjectStorage == nil || s.ObjectStorage.Prefix == "" {
		return DefaultClassisServerObjectStoragePrefix
	}
	return s.ObjectStorage.Prefix
}
