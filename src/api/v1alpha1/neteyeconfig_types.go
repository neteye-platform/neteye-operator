// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"os"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetEyeComponents holds resolved image references for a given NetEye version.
type NetEyeComponents struct {
	// Full image reference for the Keycloak container, e.g. quay.io/keycloak/keycloak:27.0.0
	KeycloakImage string
	// Full image reference for the OpenTelemetry Collector container.
	OTelCollectorImage string
	// Full image reference for the EDOT Gateway container.
	EDOTGatewayImage string
	// Full image reference for the CA-bundle init container shared by the OTel
	// Collector and EDOT Gateway deployments.
	CABundleImage string
}

// NetEyeSecretKeySelector identifies one key inside a Secret in the NetEye CR
// namespace.
type NetEyeSecretKeySelector struct {
	// Name is the name of the Secret containing the referenced value.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key inside the Secret containing the referenced value.
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
	// The Secret must exist in the shared Keycloak workload namespace.
	// +kubebuilder:validation:Required
	UsernameSecret NetEyeSecretKeySelector `json:"usernameSecret"`

	// PasswordSecret references the Secret key containing the database password.
	// The Secret must exist in the shared Keycloak workload namespace.
	// +kubebuilder:validation:Required
	PasswordSecret NetEyeSecretKeySelector `json:"passwordSecret"`
}

// NetEyeEnvVar defines an environment variable.
type NetEyeEnvVar struct {
	// Name is the environment variable name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// Value is the environment variable value. It is stored in plaintext and
	// must not contain secrets.
	// +kubebuilder:validation:Required
	Value string `json:"value"`
}

// NetEyeKeycloakOption defines an additional Keycloak server option.
type NetEyeKeycloakOption struct {
	// Name is the Keycloak server option name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*$`
	Name string `json:"name"`

	// Value is the Keycloak server option value. It is stored in plaintext in the
	// NetEye and Keycloak resources and must not contain secrets.
	// +kubebuilder:validation:Required
	Value string `json:"value"`
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

	// PodExtraEnvVars lists extra non-sensitive environment variables for
	// Keycloak pods.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	PodExtraEnvVars []NetEyeEnvVar `json:"podExtraEnvVars,omitempty"`

	// AdditionalOptions lists non-sensitive Keycloak server options. Options
	// managed by the operator cannot be configured here.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	AdditionalOptions []NetEyeKeycloakOption `json:"additionalOptions,omitempty"`

	// DBConnection configures the MariaDB database used by identity services.
	// Credential Secrets must exist in the shared Keycloak workload namespace.
	// +kubebuilder:validation:Required
	DBConnection NetEyeDBConnectionSpec `json:"dbConnection"`
}

// NetEyeElasticStackSpec configures the shared Elastic Stack telemetry pipeline.
type NetEyeElasticStackSpec struct {
	// Enabled enables the shared telemetry pipeline. Enabling it reconciles both
	// the OTel Collector and EDOT Gateway.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Telemetry groups the OTel Collector and EDOT Gateway configuration; it is
	// not independently enabled and is required when Enabled is true.
	// +kubebuilder:validation:Optional
	Telemetry *NetEyeTelemetrySpec `json:"telemetry,omitempty"`
}

// NetEyeTelemetrySpec configures the shared telemetry components. Both
// components are reconciled whenever NetEyeElasticStackSpec.Enabled is true.
type NetEyeTelemetrySpec struct {
	// OTelCollector configures the shared OpenTelemetry Collector.
	// +kubebuilder:validation:Optional
	OTelCollector *NetEyeOtelCollectorSpec `json:"otelCollector,omitempty"`

	// EDOTGateway configures the EDOT Gateway that exports telemetry to
	// Elasticsearch. Its endpoints, API-key Secret, and trusted CA belong here;
	// the image is resolved from release data.
	// +kubebuilder:validation:Optional
	EDOTGateway *NetEyeEDOTGatewaySpec `json:"edotGateway,omitempty"`
}

const (
	DefaultOTelCollectorReplicas       int32 = 1
	DefaultOTelCollectorBasicAuthName        = "otel-collector-basicauth"
	DefaultOTelCollectorRootCAName           = "neteye-root-ca"
	DefaultEDOTGatewayReplicas         int32 = 1
	DefaultEDOTGatewayAPIKeySecretName       = "otel-collector-api-key"
	DefaultEDOTGatewayAPIKeySecretKey        = "api_key"
	DefaultEDOTGatewayRootCAName             = "neteye-root-ca"
)

// NetEyeEDOTGatewaySpec configures the EDOT Gateway used to export telemetry
// to Elasticsearch. Elasticsearch configuration belongs here rather than on
// the OTel Collector.
type NetEyeEDOTGatewaySpec struct {
	// Replicas is the number of EDOT Gateway replicas to deploy.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// ElasticsearchEndpoints is the explicitly configured list of HTTPS
	// Elasticsearch endpoints consumed by the EDOT Gateway. Each endpoint host
	// must be an IP address literal, and at least one absolute HTTPS endpoint is
	// required.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	ElasticsearchEndpoints []string `json:"elasticsearchEndpoints"`

	// APIKeySecret selects the Secret key containing the Elasticsearch API key.
	// +kubebuilder:validation:Optional
	// The default is otel-collector-api-key/api_key.
	// +kubebuilder:default={name:otel-collector-api-key,key:api_key}
	APIKeySecret *NetEyeSecretKeySelector `json:"apiKeySecret,omitempty"`

	// RootCASecretName selects the trusted CA for Elasticsearch.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=neteye-root-ca
	RootCASecretName string `json:"rootCASecretName,omitempty"`
}

// EffectiveReplicas returns the collector default for objects that bypassed
// admission defaulting.
func (s *NetEyeOtelCollectorSpec) EffectiveReplicas() int32 {
	if s == nil || s.Replicas == 0 {
		return DefaultOTelCollectorReplicas
	}
	return s.Replicas
}

// EffectiveReplicas returns the gateway default for objects that bypassed
// admission defaulting.
func (s *NetEyeEDOTGatewaySpec) EffectiveReplicas() int32 {
	if s == nil || s.Replicas == 0 {
		return DefaultEDOTGatewayReplicas
	}
	return s.Replicas
}

// EffectiveBasicAuthSecretName returns the collector basic-auth Secret name.
func (s *NetEyeOtelCollectorSpec) EffectiveBasicAuthSecretName() string {
	if s == nil || s.BasicAuthSecretName == "" {
		return DefaultOTelCollectorBasicAuthName
	}
	return s.BasicAuthSecretName
}

// EffectiveRootCASecretName returns the collector trusted-CA Secret name.
func (s *NetEyeOtelCollectorSpec) EffectiveRootCASecretName() string {
	if s == nil || s.RootCASecretName == "" {
		return DefaultOTelCollectorRootCAName
	}
	return s.RootCASecretName
}

// EffectiveAPIKeySecret returns the gateway API-key Secret selector.
func (s *NetEyeEDOTGatewaySpec) EffectiveAPIKeySecret() NetEyeSecretKeySelector {
	if s == nil || s.APIKeySecret == nil {
		return NetEyeSecretKeySelector{Name: DefaultEDOTGatewayAPIKeySecretName, Key: DefaultEDOTGatewayAPIKeySecretKey}
	}
	return *s.APIKeySecret
}

// EffectiveRootCASecretName returns the gateway trusted-CA Secret name.
func (s *NetEyeEDOTGatewaySpec) EffectiveRootCASecretName() string {
	if s == nil || s.RootCASecretName == "" {
		return DefaultEDOTGatewayRootCAName
	}
	return s.RootCASecretName
}

// NetEyeOtelCollectorSpec configures the shared OpenTelemetry Collector.
type NetEyeOtelCollectorSpec struct {
	// Replicas is the number of OTel Collector replicas to deploy.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// BasicAuthSecretName selects the Secret containing the collector's ingress
	// basic-auth data. The default is otel-collector-basicauth.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=otel-collector-basicauth
	BasicAuthSecretName string `json:"basicAuthSecretName,omitempty"`

	// RootCASecretName selects the trusted CA for the collector. The default is
	// neteye-root-ca. The OIDC issuer is derived from identity.hostname using
	// the fixed master realm and is not configurable.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=neteye-root-ca
	RootCASecretName string `json:"rootCASecretName,omitempty"`
}

// NetEyeGatewaySpec defines the Gateway API resources managed by NetEye.
type NetEyeGatewaySpec struct {
	// Name is the Gateway name in the shared NetEye namespace. If it already exists,
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
}

// netEyeVersionMap maps a NetEye version string to its component image set.
// Add new entries here when a NetEye release ships a new Keycloak (or other)
// image version.
var netEyeVersionMap = map[string]NetEyeComponents{
	CurrentNetEyeVersion: {KeycloakImage: "ghcr.io/neteye-platform/neteye-keycloak:1.0.4", OTelCollectorImage: "docker.io/otel/opentelemetry-collector-contrib:0.156.0", EDOTGatewayImage: "docker.elastic.co/elastic-agent/elastic-otel-collector:9.5.3", CABundleImage: "docker.io/alpine:3.23.5@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40"},
}

const (
	// RelatedImageKeycloakEnv overrides the Keycloak image packaged with the operator.
	RelatedImageKeycloakEnv = "RELATED_IMAGE_KEYCLOAK"
	// RelatedImageOTelCollectorEnv overrides the OpenTelemetry Collector image packaged with the operator.
	RelatedImageOTelCollectorEnv = "RELATED_IMAGE_OTEL_COLLECTOR"
	// RelatedImageEDOTGatewayEnv overrides the EDOT Gateway image packaged with the operator.
	RelatedImageEDOTGatewayEnv = "RELATED_IMAGE_EDOT_GATEWAY"
	// RelatedImageCABundleEnv overrides the CA-bundle init-container image packaged with the operator.
	RelatedImageCABundleEnv = "RELATED_IMAGE_CA_BUNDLE"
	CurrentNetEyeVersion    = "4.50"
	PreviousNetEyeVersion   = "4.49"
)

// ComponentsForVersion returns the component image set for the given NetEye
// version. If the version is not found in the map the second return value is
// false.
func ComponentsForVersion(version string) (NetEyeComponents, bool) {
	c, ok := netEyeVersionMap[version]
	if !ok {
		return NetEyeComponents{}, false
	}
	if image := strings.TrimSpace(os.Getenv(RelatedImageKeycloakEnv)); image != "" {
		c.KeycloakImage = image
	}
	if image := strings.TrimSpace(os.Getenv(RelatedImageOTelCollectorEnv)); image != "" {
		c.OTelCollectorImage = image
	}
	if image := strings.TrimSpace(os.Getenv(RelatedImageEDOTGatewayEnv)); image != "" {
		c.EDOTGatewayImage = image
	}
	if image := strings.TrimSpace(os.Getenv(RelatedImageCABundleEnv)); image != "" {
		c.CABundleImage = image
	}
	return c, ok
}

// SupportedVersions returns the NetEye versions supported by this operator.
func SupportedVersions() []string {
	versions := make([]string, 0, len(netEyeVersionMap))
	for version := range netEyeVersionMap {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}

// IsSupportedVersion returns true if the given NetEye version is supported by this operator.
func IsSupportedVersion(version string) bool {
	_, ok := netEyeVersionMap[version]
	return ok
}

// IsPreviousVersion returns true if given NetEye version is the latest supported version.
func IsPreviousVersion(version string) bool {
	return version == PreviousNetEyeVersion
}

// IsLatestVersion returns true if the given NetEye version is the latest supported version.
func IsLatestVersion(version string) bool {
	return version == CurrentNetEyeVersion
}

// NetEyeSpec defines the desired state of NetEyeConfig.
type NetEyeSpec struct {
	// Version is the NetEye product version string, e.g. "4.50".
	// It is used to resolve the correct component images (Keycloak, etc.).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+$`
	// +kubebuilder:example="4.50"
	Version string `json:"version"`

	// Gateway configures the Gateway API Gateway and default routes managed by
	// NetEye.
	// +kubebuilder:validation:Required
	Gateway NetEyeGatewaySpec `json:"gateway"`

	// InternalCertificateIssuerRef is the cert-manager Issuer name used for TLS
	// certificates consumed by common NetEye components. The Issuer must already
	// exist in the shared NetEye namespace and is managed by the user.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="neteye-internal-issuer"
	InternalCertificateIssuerRef string `json:"internalCertificateIssuerRef"`

	// Identity configures identity services such as Keycloak.
	// +kubebuilder:validation:Required
	Identity NetEyeIdentitySpec `json:"identity"`

	// ElasticStack configures the shared telemetry pipeline. Its telemetry group
	// is not independently enabled, and enabling Elastic Stack deploys both the
	// OTel Collector and EDOT Gateway. Images are resolved from release data.
	// +kubebuilder:validation:Optional
	ElasticStack *NetEyeElasticStackSpec `json:"elasticStack,omitempty"`
}

// ServiceState is the per-service state reported in NetEyeServiceStatus.Status.
type ServiceState string

const (
	ServiceStateUnknown  ServiceState = "Unknown"
	ServiceStateNotReady ServiceState = "NotReady"
	ServiceStateReady    ServiceState = "Ready"
	ServiceStateFailed   ServiceState = "Failed"
	ServiceStateDisabled ServiceState = "Disabled"
)

// NetEyePhase is the aggregate lifecycle state reported in NetEyeStatus.Phase.
type NetEyePhase string

const (
	PhasePendingUpgrades NetEyePhase = "PendingUpgrades"
	PhaseNotReady        NetEyePhase = "NotReady"
	PhaseReady           NetEyePhase = "Ready"
	PhaseFailed          NetEyePhase = "Failed"
)

// NetEyeServiceStatus defines the observed state of one NetEye service.
type NetEyeServiceStatus struct {
	Status ServiceState `json:"status,omitempty"`

	// Message is a human-readable status message for this service.
	Message string `json:"message,omitempty"`

	// ResolvedImage is the container image resolved for this service.
	ResolvedImage string `json:"resolvedImage,omitempty"`
}

// NetEyeElasticStackStatus reports observed state for Elastic Stack components.
type NetEyeElasticStackStatus struct {
	// Status is the observed state of the Elastic Stack feature module.
	Status ServiceState `json:"status,omitempty"`

	// Message is a human-readable status message for the Elastic Stack feature module.
	Message string `json:"message,omitempty"`

	// OTelCollector reports the observed state of the shared OpenTelemetry Collector.
	OTelCollector *NetEyeServiceStatus `json:"otelCollector,omitempty"`

	// EDOTGateway reports the observed state of the EDOT Gateway.
	EDOTGateway *NetEyeServiceStatus `json:"edotGateway,omitempty"`
}

// NetEyeServicesStatus groups observed state by NetEye service/component.
type NetEyeServicesStatus struct {
	// Identity reports the observed state of identity services such as Keycloak.
	Identity     *NetEyeServiceStatus      `json:"identity,omitempty"`
	ElasticStack *NetEyeElasticStackStatus `json:"elasticStack,omitempty"`
}

// NetEyeStatus defines the observed state of NetEyeConfig.
type NetEyeStatus struct {
	Phase NetEyePhase `json:"phase,omitempty"`

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
