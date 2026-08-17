/*
Copyright 2026 Wuerth IT | Italy.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetEyeComponents holds resolved image references for a given NetEye version.
type NetEyeComponents struct {
	// Full image reference for the Keycloak container, e.g. quay.io/keycloak/keycloak:27.0.0
	KeycloakImage string
}

// NetEyeSecretKeySelector identifies one key inside a Secret in the NetEye CR
// namespace.
type NetEyeSecretKeySelector struct {
	// Name is the name of the Secret containing the database credential.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key inside the Secret containing the database credential value.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// NetEyeDBConnectionSpec defines the database connection used by a
// NetEye component.
type NetEyeDBConnectionSpec struct {
	// Host is the DNS name or IP address of the external service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="mariadb.example.com"
	Host string `json:"host"`

	// Port is the service port.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=3306
	Port int32 `json:"port,omitempty"`

	// DBName is the database name used by Keycloak.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="keycloak"
	DBName string `json:"dbName"`

	// UsernameSecret references the Secret key containing the database username.
	// The Secret must exist in the NetEye CR namespace.
	// +kubebuilder:validation:Required
	UsernameSecret NetEyeSecretKeySelector `json:"usernameSecret"`

	// PasswordSecret references the Secret key containing the database password.
	// The Secret must exist in the NetEye CR namespace.
	// +kubebuilder:validation:Required
	PasswordSecret NetEyeSecretKeySelector `json:"passwordSecret"`
}

// NetEyeIdentitySpec defines the identity service deployment options.
type NetEyeIdentitySpec struct {
	// Replicas is the number of identity service replicas to deploy.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Hostname is the public hostname configured for the identity service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:example="keycloak.example.com"
	Hostname string `json:"hostname"`

	// PodExtraEnvVars lists extra environment variables for identity pods. Entries
	// can be plain names, such as "KC_FEATURES", or name/value pairs, such as
	// "JAVA_OPTS_APPEND=-Djava.net.preferIPv6Addresses=true". Plain names are
	// passed to the identity service with an empty value.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z_][A-Za-z0-9_]*(=.*)?$`
	PodExtraEnvVars []string `json:"podExtraEnvVars,omitempty"`

	// DBConnection configures the MariaDB database used by identity services.
	// Credential Secrets must exist in the NetEye CR namespace.
	// +kubebuilder:validation:Required
	DBConnection NetEyeDBConnectionSpec `json:"dbConnection"`
}

// NetEyeGatewaySpec defines the Gateway API resources managed by NetEye.
type NetEyeGatewaySpec struct {
	// Name is the Gateway name in the NetEye CR namespace. If it already exists,
	// the operator adopts and reconciles it; otherwise the operator creates it.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:example="neteye"
	Name string `json:"name"`

	// ClassName is the GatewayClass used when the operator creates or reconciles
	// the Gateway.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="cilium"
	ClassName string `json:"className"`

	// Annotations are written to spec.infrastructure.annotations on the Gateway.
	// Use this for implementation-specific settings such as Cilium LB IPAM.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example={"lbipam.cilium.io/ips":"192.0.2.10"}
	Annotations map[string]string `json:"annotations,omitempty"`

	// TLSSecretName is the Secret written by the operator-managed Gateway
	// wildcard Certificate and used by the Gateway HTTPS listener for TLS
	// termination.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="neteyelocal-wildcard-tls"
	TLSSecretName string `json:"tlsSecretName"`
}

// netEyeVersionMap maps a NetEye version string to its component image set.
// Add new entries here when a NetEye release ships a new Keycloak (or other)
// image version.
var netEyeVersionMap = map[string]NetEyeComponents{
	"4.50": {KeycloakImage: "ghcr.io/neteye-platform/neteye-keycloak:1.0.1"},
}

// ComponentsForVersion returns the component image set for the given NetEye
// version. If the version is not found in the map the second return value is
// false.
func ComponentsForVersion(version string) (NetEyeComponents, bool) {
	c, ok := netEyeVersionMap[version]
	return c, ok
}

// NetEyeSpec defines the desired state of NetEyeConfig.
type NetEyeSpec struct {
	// Version is the NetEye product version string, e.g. "4.36".
	// It is used to resolve the correct component images (Keycloak, etc.).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="4.36"
	Version string `json:"version"`

	// EnabledModules declares which NetEye feature modules are available for tenants.
	// The field is part of the desired API contract; module-specific reconcilers
	// will consume it as they are implemented.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:items:MinLength=1
	EnabledModules []string `json:"enabledModules,omitempty"`

	// Gateway configures the Gateway API Gateway and default routes managed by
	// NetEye.
	// +kubebuilder:validation:Required
	Gateway NetEyeGatewaySpec `json:"gateway"`

	// InternalCertificateIssuerRef is the cert-manager Issuer name used for TLS
	// certificates consumed by common NetEye components. The Issuer must already
	// exist in the NetEye CR namespace and is managed by the user.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="neteye-internal-issuer"
	InternalCertificateIssuerRef string `json:"internalCertificateIssuerRef"`

	// Identity configures identity services such as Keycloak.
	// +kubebuilder:validation:Required
	Identity NetEyeIdentitySpec `json:"identity"`
}

// NetEyeServiceStatus defines the observed state of one NetEye service.
type NetEyeServiceStatus struct {
	// Status is the service state, for example Pending, Ready, or Failed.
	Status string `json:"status,omitempty"`

	// Message is a human-readable status message for this service.
	Message string `json:"message,omitempty"`

	// ResolvedImage is the container image resolved for this service.
	ResolvedImage string `json:"resolvedImage,omitempty"`
}

// NetEyeServicesStatus groups observed state by NetEye service/component.
type NetEyeServicesStatus struct {
	// Identity reports the observed state of identity services such as Keycloak.
	Identity *NetEyeServiceStatus `json:"identity,omitempty"`
}

// NetEyeStatus defines the observed state of NetEyeConfig.
type NetEyeStatus struct {
	// Phase is the aggregate NetEye state: Pending, Ready, or Failed.
	Phase string `json:"phase,omitempty"`

	// Message is a human-readable aggregate status message.
	Message string `json:"message,omitempty"`

	// ServicesStatus reports observed state for each managed NetEye service.
	ServicesStatus NetEyeServicesStatus `json:"servicesStatus,omitempty"`

	// ObservedGeneration is the generation of the most recently observed
	// NetEye.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=neteyes,shortName=ne
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.metadata.namespace`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NetEye is the Schema for the neteyes API.
// It declares which NetEye product version is being deployed and in which
// Kubernetes namespace, driving the selection of component images (e.g. Keycloak 27).
type NetEye struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetEyeSpec   `json:"spec,omitempty"`
	Status NetEyeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetEyeList contains a list of NetEye.
type NetEyeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetEye `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetEye{}, &NetEyeList{})
}
