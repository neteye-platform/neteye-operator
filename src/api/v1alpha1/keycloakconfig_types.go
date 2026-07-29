/*
Copyright 2024 Wuerth Phoenix.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ConditionTypeReady represents if the KeycloakConfig is ready
	ConditionTypeReady = "Ready"
	// ConditionTypeCertificateReady represents if TLS certificate is ready
	ConditionTypeCertificateReady = "CertificateReady"
	// ConditionTypeDatabaseReady represents if database is ready
	ConditionTypeDatabaseReady = "DatabaseReady"
	// ConditionTypeKeycloakOperatorReady represents if Keycloak Operator is ready
	ConditionTypeKeycloakOperatorReady = "KeycloakOperatorReady"
	// ConditionTypeReconciling represents ongoing reconciliation
	ConditionTypeReconciling = "Reconciling"
	// ConditionTypeError represents an error condition
	ConditionTypeError = "Error"

	// PhaseTypePending represents pending phase
	PhaseTypePending = "Pending"
	// PhaseTypeProvisioning represents provisioning phase
	PhaseTypeProvisioning = "Provisioning"
	// PhaseTypeReady represents ready phase
	PhaseTypeReady = "Ready"
	// PhaseTypeFailed represents failed phase
	PhaseTypeFailed = "Failed"
)

// KeycloakConfigSpec defines the desired state of KeycloakConfig
type KeycloakConfigSpec struct {
	// Namespace where Keycloak will be deployed
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Hostname/FQDN for Keycloak
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`

	// Number of Keycloak replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Instances *int32 `json:"instances,omitempty"`

	// TLS certificate configuration
	Certificate *CertificateConfig `json:"certificate,omitempty"`

	// Database configuration
	// +kubebuilder:validation:Required
	Database DatabaseConfig `json:"database"`

	// Ingress configuration
	Ingress *IngressConfig `json:"ingress,omitempty"`

	// Resource requests and limits
	Resources *ResourceConfig `json:"resources,omitempty"`

	// Retry policy for reconciliation
	RetryPolicy *RetryPolicyConfig `json:"retryPolicy,omitempty"`
}

// CertificateConfig defines TLS certificate configuration
type CertificateConfig struct {
	// Generate self-signed certificate if true
	// +kubebuilder:default=true
	Generate *bool `json:"generate,omitempty"`

	// Certificate validity in days (for generated certificates)
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=365
	ValidityDays *int32 `json:"validityDays,omitempty"`

	// Reference to existing TLS secret (format: namespace/secretname)
	ExistingSecret string `json:"existingSecret,omitempty"`
}

// DatabaseConfig defines database configuration
type DatabaseConfig struct {
	// Database vendor: postgres, mysql, mariadb
	// +kubebuilder:validation:Enum=postgres;mysql;mariadb
	// +kubebuilder:validation:Required
	Vendor string `json:"vendor"`

	// Database host
	Host string `json:"host,omitempty"`

	// Database port
	// +kubebuilder:default=5432
	Port *int32 `json:"port,omitempty"`

	// Database name
	// +kubebuilder:default=keycloak
	Database string `json:"database,omitempty"`

	// Database username (for plaintext credentials, alternative to CredentialsSecret)
	Username string `json:"username,omitempty"`

	// Database password (for plaintext credentials, alternative to CredentialsSecret)
	Password string `json:"password,omitempty"`

	// Secret containing database username and password
	// Keys: username, password
	CredentialsSecret string `json:"credentialsSecret,omitempty"`

	// Deploy PostgreSQL database (for dev/test)
	// +kubebuilder:default=false
	CreatePostgres *bool `json:"createPostgres,omitempty"`
}

// IngressConfig defines ingress configuration
type IngressConfig struct {
	// Enable ingress
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Ingress class name
	ClassName string `json:"className,omitempty"`

	// TLS termination mode: passthrough, edge, reencrypt
	// +kubebuilder:validation:Enum=passthrough;edge;reencrypt
	// +kubebuilder:default=passthrough
	TLSMode string `json:"tlsMode,omitempty"`
}

// ResourceConfig defines resource requests and limits
type ResourceConfig struct {
	// Resource requests
	Requests *ResourceRequirements `json:"requests,omitempty"`

	// Resource limits
	Limits *ResourceRequirements `json:"limits,omitempty"`
}

// ResourceRequirements defines CPU and memory requirements
type ResourceRequirements struct {
	// CPU request/limit
	CPU string `json:"cpu,omitempty"`

	// Memory request/limit
	Memory string `json:"memory,omitempty"`
}

// RetryPolicyConfig defines retry behavior
type RetryPolicyConfig struct {
	// Maximum number of retry attempts
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	MaxRetries *int32 `json:"maxRetries,omitempty"`

	// Initial retry delay in seconds
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=5
	InitialDelay *int32 `json:"initialDelay,omitempty"`

	// Maximum retry delay in seconds
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=300
	MaxDelay *int32 `json:"maxDelay,omitempty"`

	// Exponential backoff multiplier
	// +kubebuilder:default=2.0
	BackoffMultiplier *float64 `json:"backoffMultiplier,omitempty"`
}

// Condition represents a condition on the KeycloakConfig
type Condition struct {
	// Type of condition: Ready, CertificateReady, DatabaseReady, KeycloakOperatorReady, Reconciling, Error
	Type string `json:"type"`

	// Status of condition: True, False, Unknown
	Status corev1.ConditionStatus `json:"status"`

	// Reason for the condition
	Reason string `json:"reason,omitempty"`

	// Human-readable message about the condition
	Message string `json:"message,omitempty"`

	// Last transition time
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// KeycloakConfigStatus defines the observed state of KeycloakConfig
type KeycloakConfigStatus struct {
	// Phase of the reconciliation: Pending, Provisioning, Ready, Failed
	Phase string `json:"phase,omitempty"`

	// Conditions represent the latest available observations
	Conditions []Condition `json:"conditions,omitempty"`

	// Current retry attempt count
	RetryCount int32 `json:"retryCount,omitempty"`

	// Last error encountered during reconciliation
	LastError string `json:"lastError,omitempty"`

	// Generation of the most recently observed KeycloakConfig
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Timestamp of last reconciliation attempt
	LastReconciliation *metav1.Time `json:"lastReconciliation,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kc;plural=keycloakconfigs
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="Retries",type=integer,JSONPath=`.status.retryCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KeycloakConfig is the Schema for the keycloakconfigs API
type KeycloakConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeycloakConfigSpec   `json:"spec,omitempty"`
	Status KeycloakConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KeycloakConfigList contains a list of KeycloakConfig
type KeycloakConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KeycloakConfig{}, &KeycloakConfigList{})
}
