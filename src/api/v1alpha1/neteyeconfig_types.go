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
	// Full image reference for the PermissionSync container.
	PermissionSyncImage string
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
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=keycloak
	// +kubebuilder:example="keycloak"
	DBName string `json:"dbName,omitempty"`

	// UsernameSecret references the Secret key containing the database username.
	// The Secret must exist in the shared Keycloak workload namespace.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={name:keycloak-db-credentials,key:username}
	UsernameSecret *NetEyeSecretKeySelector `json:"usernameSecret,omitempty"`

	// PasswordSecret references the Secret key containing the database password.
	// The Secret must exist in the shared Keycloak workload namespace.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={name:keycloak-db-credentials,key:password}
	PasswordSecret *NetEyeSecretKeySelector `json:"passwordSecret,omitempty"`
}

const (
	DefaultKeycloakDatabaseName              = "keycloak"
	DefaultKeycloakDatabaseCredentialsName   = "keycloak-db-credentials"
	DefaultKeycloakDatabaseUsernameSecretKey = "username"
	DefaultKeycloakDatabasePasswordSecretKey = "password"
)

// EffectiveDBName returns the Keycloak database name for objects that bypassed
// admission defaulting.
func (s *NetEyeDBConnectionSpec) EffectiveDBName() string {
	if s == nil || s.DBName == "" {
		return DefaultKeycloakDatabaseName
	}
	return s.DBName
}

// EffectiveUsernameSecret returns the Keycloak database username selector for
// objects that bypassed admission defaulting.
func (s *NetEyeDBConnectionSpec) EffectiveUsernameSecret() NetEyeSecretKeySelector {
	if s == nil || s.UsernameSecret == nil {
		return NetEyeSecretKeySelector{Name: DefaultKeycloakDatabaseCredentialsName, Key: DefaultKeycloakDatabaseUsernameSecretKey}
	}
	return *s.UsernameSecret
}

// EffectivePasswordSecret returns the Keycloak database password selector for
// objects that bypassed admission defaulting.
func (s *NetEyeDBConnectionSpec) EffectivePasswordSecret() NetEyeSecretKeySelector {
	if s == nil || s.PasswordSecret == nil {
		return NetEyeSecretKeySelector{Name: DefaultKeycloakDatabaseCredentialsName, Key: DefaultKeycloakDatabasePasswordSecretKey}
	}
	return *s.PasswordSecret
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

	// EnabledFeatures lists additional Keycloak features to enable, in the
	// Keycloak feature-name form optionally suffixed with a version
	// (for example "token-exchange" or "admin-fine-grained-authz:v2").
	// Features managed by the operator cannot be requested here; the telemetry
	// features are enabled through Telemetry instead.
	// +kubebuilder:validation:Optional
	// +listType=set
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:Pattern=`^[a-z][a-z0-9-]*(:v[0-9]+)?$`
	// +kubebuilder:example={"token-exchange"}
	EnabledFeatures []string `json:"enabledFeatures,omitempty"`

	// Telemetry configures opt-in identity service telemetry export to the shared
	// EDOT Gateway. Each signal is disabled by default.
	// +kubebuilder:validation:Optional
	Telemetry *NetEyeIdentityTelemetrySpec `json:"telemetry,omitempty"`

	// DBConnection configures the MariaDB database used by identity services.
	// Credential Secrets must exist in the shared Keycloak workload namespace.
	// +kubebuilder:validation:Required
	DBConnection NetEyeDBConnectionSpec `json:"dbConnection"`
}

// NetEyeIdentityTelemetrySpec configures opt-in Keycloak telemetry signals.
type NetEyeIdentityTelemetrySpec struct {
	// LogsEnabled exports Keycloak logs through the EDOT Gateway.
	// +kubebuilder:default=false
	LogsEnabled bool `json:"logsEnabled,omitempty"`

	// MetricsEnabled exports Keycloak metrics through the EDOT Gateway.
	// +kubebuilder:default=false
	MetricsEnabled bool `json:"metricsEnabled,omitempty"`

	// ResourceAttributes adds OpenTelemetry resource attributes to Keycloak
	// telemetry. When either signal is enabled, attributes are merged over the
	// default data_stream.namespace=neteye_system_internal. The service.name
	// attribute is forbidden because the operator owns the Keycloak service name
	// (neteye-keycloak).
	// +kubebuilder:validation:Optional
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
}

// NetEyeElasticStackSpec configures the shared Elastic Stack telemetry pipeline.
type NetEyeElasticStackSpec struct {
	// Enabled enables the shared telemetry pipeline. Enabling it reconciles both
	// the OTel Collector and EDOT Gateway.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// ElasticsearchEndpoints is the explicitly configured list of HTTPS
	// Elasticsearch endpoints consumed by both telemetry components. Each
	// endpoint host must be an IP address literal or a DNS name.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	ElasticsearchEndpoints []string `json:"elasticsearchEndpoints,omitempty"`

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
	// Elasticsearch. Its API-key Secret and trusted CA belong here; the image
	// is resolved from release data.
	// +kubebuilder:validation:Optional
	EDOTGateway *NetEyeEDOTGatewaySpec `json:"edotGateway,omitempty"`
}

const (
	DefaultOTelCollectorReplicas         int32 = 1
	DefaultOTelCollectorBasicAuthName          = "otel-collector-basicauth"
	DefaultOTelCollectorAPIKeySecretName       = "otel-collector-icinga-api-key-secret"
	DefaultOTelCollectorAPIKeySecretKey        = "api_key"
	DefaultOTelCollectorRootCAName             = "neteye-root-ca"
	DefaultEDOTGatewayReplicas           int32 = 1
	DefaultEDOTGatewayAPIKeySecretName         = "otel-collector-apm-api-key-secret"
	DefaultEDOTGatewayAPIKeySecretKey          = "api_key"
	DefaultEDOTGatewayRootCAName               = "neteye-root-ca"
)

// NetEyeEDOTGatewaySpec configures the EDOT Gateway used to export telemetry
// to Elasticsearch.
type NetEyeEDOTGatewaySpec struct {
	// Replicas is the number of EDOT Gateway replicas to deploy.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// APIKeySecret selects the Secret key containing the Elasticsearch API key.
	// +kubebuilder:validation:Optional
	// The default is otel-collector-apm-api-key-secret/api_key.
	// +kubebuilder:default={name:otel-collector-apm-api-key-secret,key:api_key}
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

// EffectiveAPIKeySecret returns the collector API-key Secret selector.
func (s *NetEyeOtelCollectorSpec) EffectiveAPIKeySecret() NetEyeSecretKeySelector {
	if s == nil || s.APIKeySecret == nil {
		return NetEyeSecretKeySelector{Name: DefaultOTelCollectorAPIKeySecretName, Key: DefaultOTelCollectorAPIKeySecretKey}
	}
	return *s.APIKeySecret
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

	// APIKeySecret selects the Secret key exposed to the collector as the
	// ELASTICSEARCH_API_KEY environment variable. The default is
	// otel-collector-icinga-api-key-secret/api_key.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={name:otel-collector-icinga-api-key-secret,key:api_key}
	APIKeySecret *NetEyeSecretKeySelector `json:"apiKeySecret,omitempty"`
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

// PermissionSync defaults. Every value PermissionSync accepts as a deployment
// bound is exposed with the value the operator would otherwise have fixed, so
// a NetEye resource describes the whole runtime instead of hiding part of it.
// The relations between them are validated, because PermissionSync aborts
// startup on an impossible combination rather than degrading one component.
const (
	DefaultPermissionSyncReplicas         int32 = 2
	DefaultPermissionSyncLogLevel               = "info"
	DefaultPermissionSyncRootCAName             = "neteye-root-ca"
	DefaultPermissionSyncProviderEndpoint       = "https://httpd.neteyelocal/neteye/api/v2/permission-provider/desired-state"
	DefaultPermissionSyncGLPIEndpoint           = "https://glpi.neteyelocal/apirest.php"
	DefaultPermissionSyncGLPISecretName         = "permissionsync-glpi-credentials"
	DefaultPermissionSyncGLPIAppTokenKey        = "app_token"  // #nosec G101 -- Secret key name, not a credential
	DefaultPermissionSyncGLPIUserTokenKey       = "user_token" // #nosec G101 -- Secret key name, not a credential

	DefaultPermissionSyncOverallDeadlineMilliseconds   int64 = 10000
	DefaultPermissionSyncInboundAdmissionLimit         int64 = 64
	DefaultPermissionSyncSynchronizationCapacity       int64 = 16
	DefaultPermissionSyncShutdownGraceMilliseconds     int64 = 20000
	DefaultPermissionSyncMetadataTimeoutMilliseconds   int64 = 3000
	DefaultPermissionSyncCacheFreshnessMilliseconds    int64 = 300000
	DefaultPermissionSyncCacheStaleIfErrorMilliseconds int64 = 600000
	DefaultPermissionSyncClockSkewMilliseconds         int64 = 30000
	DefaultPermissionSyncOperationTimeoutMilliseconds  int64 = 5000
	// MaxPermissionSyncSynchronizationCapacity is PermissionSync's own product
	// ceiling; a higher value is a startup abort, not a clamp.
	MaxPermissionSyncSynchronizationCapacity int64 = 1024
	// MaxPermissionSyncClockSkewMilliseconds is PermissionSync's bounded skew
	// allowance.
	MaxPermissionSyncClockSkewMilliseconds int64 = 300000

	// PermissionSyncGLPIAdapter is the only Target Adapter identifier
	// PermissionSync compiles in today.
	PermissionSyncGLPIAdapter = "glpi"
)

// NetEyePermissionSyncSpec configures the PermissionSync service. The
// operator also provisions the Keycloak objects a technical caller needs for
// every declared target; see the PermissionSync ADR for what stays outside the
// operator's boundary.
type NetEyePermissionSyncSpec struct {
	// Replicas is the number of PermissionSync replicas to deploy. The service
	// is stateless, so any healthy replica can serve any request.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	Replicas int32 `json:"replicas,omitempty"`

	// LogLevel is the bounded log-level threshold of PermissionSync's
	// structured JSON log output. The format and its redaction rules are fixed.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=error;warn;info;debug;trace
	// +kubebuilder:default=info
	LogLevel string `json:"logLevel,omitempty"`

	// RootCASecretName selects the Secret whose tls.crt is trusted, in addition
	// to the system roots, when PermissionSync connects to the identity
	// service, the Permission Provider, and GLPI. Set it to the empty string to
	// trust only the system roots.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=neteye-root-ca
	RootCASecretName *string `json:"rootCASecretName,omitempty"`

	// Provider configures the process-wide Permission Provider PermissionSync
	// reads a user's desired permissions from. It is always the NetEye
	// deployment's own API, so the section defaults to it and an installation
	// never has to name it.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Provider NetEyePermissionSyncProviderSpec `json:"provider,omitempty"`

	// GLPI configures the one process-wide GLPI backend. It is required as soon
	// as a target selects the glpi adapter; without it such a target stays
	// recognized but unavailable.
	// +kubebuilder:validation:Optional
	GLPI *NetEyePermissionSyncGLPISpec `json:"glpi,omitempty"`

	// Targets declares the logical target routes PermissionSync serves. A
	// logical target is matched exactly against the suffix of the caller's
	// permissionsync:<target> scope, and the login-sync authenticator derives
	// that target from the Keycloak client the user logs in to, so each entry
	// must be named after that client.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=logicalTarget
	Targets []NetEyePermissionSyncTarget `json:"targets,omitempty"`

	// Request bounds one synchronization request and the inbound capacity
	// around it.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Request NetEyePermissionSyncRequestSpec `json:"request,omitempty"`

	// Shutdown bounds the graceful shutdown of in-flight requests.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Shutdown NetEyePermissionSyncShutdownSpec `json:"shutdown,omitempty"`

	// Authentication bounds technical-caller token verification. The issuer,
	// the audience, and the signing-algorithm allowlist are not configurable:
	// they are the integration contract, derived from the identity service.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Authentication NetEyePermissionSyncAuthenticationSpec `json:"authentication,omitempty"`

	// OperationTimeoutMilliseconds bounds one Permission Provider or GLPI
	// operation. It must be positive and no greater than the overall request
	// deadline.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=5000
	OperationTimeoutMilliseconds int64 `json:"operationTimeoutMilliseconds,omitempty"`
}

// NetEyePermissionSyncRequestSpec bounds one synchronization request. Like the
// Keycloak realm's always-enforced blocks, every field defaults to the value
// the operator would otherwise have fixed, so an omitted section is still
// fully configured.
type NetEyePermissionSyncRequestSpec struct {
	// OverallDeadlineMilliseconds is the one absolute deadline for a
	// synchronization request. It starts when the request is accepted and
	// covers admission waiting, body collection, authentication, validation,
	// routing, capacity, Provider, and Adapter work. Configure it below the
	// caller's own HTTP timeout.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10000
	OverallDeadlineMilliseconds int64 `json:"overallDeadlineMilliseconds,omitempty"`

	// InboundAdmissionLimit bounds both how many synchronization requests may
	// be admitted at once and how many more may wait for admission. A request
	// arriving when both bounds are full is refused at the transport boundary
	// and receives no response, so saturation adds no caller-facing status.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=64
	InboundAdmissionLimit int64 `json:"inboundAdmissionLimit,omitempty"`

	// SynchronizationCapacity bounds how many selected-target
	// synchronizations run concurrently. It bounds resource use only: it is
	// not an ordering or exclusion primitive, and concurrent requests for the
	// same user are allowed and unordered.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:default=16
	SynchronizationCapacity int64 `json:"synchronizationCapacity,omitempty"`
}

// NetEyePermissionSyncShutdownSpec bounds graceful shutdown.
type NetEyePermissionSyncShutdownSpec struct {
	// GraceMilliseconds is how long already admitted requests may finish after
	// shutdown begins. It must be at least the overall request deadline, so
	// every compliant request accepted before shutdown has time to return. The
	// pod's termination grace period covers this plus PermissionSync's fixed
	// post-grace phases and is not configurable here.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=20000
	GraceMilliseconds int64 `json:"graceMilliseconds,omitempty"`
}

// NetEyePermissionSyncAuthenticationSpec bounds token verification.
type NetEyePermissionSyncAuthenticationSpec struct {
	// MetadataOperationTimeoutMilliseconds bounds one JWKS or OIDC discovery
	// retrieval. It must be positive and no greater than the overall request
	// deadline.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3000
	MetadataOperationTimeoutMilliseconds int64 `json:"metadataOperationTimeoutMilliseconds,omitempty"`

	// CacheFreshnessMilliseconds is how long retained verification material
	// counts as fresh.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=300000
	CacheFreshnessMilliseconds int64 `json:"cacheFreshnessMilliseconds,omitempty"`

	// CacheStaleIfErrorMilliseconds is the additional bounded window in which
	// still-usable material may verify a token while the metadata source is
	// unavailable. Readiness stays true for that whole window. Zero disables
	// the grace, which makes readiness depend on the metadata source being
	// reachable within the freshness window.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=600000
	CacheStaleIfErrorMilliseconds *int64 `json:"cacheStaleIfErrorMilliseconds,omitempty"`

	// ClockSkewMilliseconds is the bounded clock skew allowance applied to
	// token time claims.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=300000
	// +kubebuilder:default=30000
	ClockSkewMilliseconds *int64 `json:"clockSkewMilliseconds,omitempty"`
}

// NetEyePermissionSyncProviderSpec configures the Generic REST Permission
// Provider, the only Provider implementation PermissionSync supports.
type NetEyePermissionSyncProviderSpec struct {
	// Endpoint is one complete HTTPS request URI, with no query or fragment.
	// PermissionSync appends nothing to it. It defaults to the NetEye
	// deployment's own permission API, which is where every installation reads
	// a user's desired permissions from; override it only for a deployment
	// that serves that API somewhere else.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https://[^\s?#]+$`
	// +kubebuilder:default="https://httpd.neteyelocal/neteye/api/v2/permission-provider/desired-state"
	Endpoint string `json:"endpoint,omitempty"`
}

// NetEyePermissionSyncGLPISpec configures the GLPI Target Adapter's backend.
type NetEyePermissionSyncGLPISpec struct {
	// Endpoint is the GLPI REST API entry point. It defaults to the GLPI the
	// NetEye deployment provides.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https://[^\s?#]+$`
	// +kubebuilder:default="https://glpi.neteyelocal/apirest.php"
	Endpoint string `json:"endpoint,omitempty"`

	// CredentialsSecret selects the user-managed Secret holding the GLPI app
	// token and the service account's user token. The Secret must exist in the
	// shared NetEye namespace.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={name:permissionsync-glpi-credentials,appTokenKey:app_token,userTokenKey:user_token}
	CredentialsSecret *NetEyePermissionSyncGLPICredentials `json:"credentialsSecret,omitempty"`

	// AuthenticationSource is the GLPI authentication source used when creating
	// a missing GLPI user. When omitted, GLPI's own defaults apply.
	// +kubebuilder:validation:Optional
	AuthenticationSource *NetEyePermissionSyncGLPIAuthenticationSource `json:"authenticationSource,omitempty"`
}

// NetEyePermissionSyncGLPICredentials identifies the two keys inside the
// user-managed GLPI credentials Secret.
type NetEyePermissionSyncGLPICredentials struct {
	// Name is the name of the Secret carrying both tokens.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// AppTokenKey is the key holding the GLPI application token.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=app_token
	AppTokenKey string `json:"appTokenKey,omitempty"`

	// UserTokenKey is the key holding the GLPI service account's user token.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=user_token
	UserTokenKey string `json:"userTokenKey,omitempty"`
}

// NetEyePermissionSyncGLPIAuthenticationSource selects the GLPI authentication
// source for users the adapter creates. Both fields form one coherent
// selection and are therefore required together.
//
// They are pointers so that omitting one is rejected instead of being sent to
// GLPI as an explicit zero: a non-pointer integer always serializes, which
// would satisfy the schema's required check with a value nobody chose.
type NetEyePermissionSyncGLPIAuthenticationSource struct {
	// AuthType is the GLPI authtype value.
	// +kubebuilder:validation:Required
	// +kubebuilder:example=3
	AuthType *int64 `json:"authType"`

	// AuthsID is the GLPI auths_id value.
	// +kubebuilder:validation:Required
	// +kubebuilder:example=2
	AuthsID *int64 `json:"authsId"`
}

// NetEyePermissionSyncTarget routes one logical target to one Target Adapter.
type NetEyePermissionSyncTarget struct {
	// LogicalTarget is the logical target identifier, which must satisfy
	// PermissionSync's target grammar and name the Keycloak login client the
	// login-sync authenticator derives the target from.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`
	// +kubebuilder:example="glpi"
	LogicalTarget string `json:"logicalTarget"`

	// Adapter is the Target Adapter serving this logical target.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=glpi
	Adapter string `json:"adapter"`
}

// EffectiveReplicas returns the PermissionSync replica count for objects that
// bypassed admission defaulting.
func (s *NetEyePermissionSyncSpec) EffectiveReplicas() int32 {
	if s == nil || s.Replicas == 0 {
		return DefaultPermissionSyncReplicas
	}
	return s.Replicas
}

// EffectiveLogLevel returns the PermissionSync log level for objects that
// bypassed admission defaulting.
func (s *NetEyePermissionSyncSpec) EffectiveLogLevel() string {
	if s == nil || s.LogLevel == "" {
		return DefaultPermissionSyncLogLevel
	}
	return s.LogLevel
}

// EffectiveRootCASecretName returns the trusted-CA Secret name. An explicitly
// empty value keeps the system roots as the only trust anchors, while an
// absent one falls back to the default Secret.
func (s *NetEyePermissionSyncSpec) EffectiveRootCASecretName() string {
	if s == nil || s.RootCASecretName == nil {
		return DefaultPermissionSyncRootCAName
	}
	return *s.RootCASecretName
}

// EffectiveCredentialsSecret returns the GLPI credentials Secret selector for
// objects that bypassed admission defaulting.
func (s *NetEyePermissionSyncGLPISpec) EffectiveCredentialsSecret() NetEyePermissionSyncGLPICredentials {
	credentials := NetEyePermissionSyncGLPICredentials{Name: DefaultPermissionSyncGLPISecretName}
	if s != nil && s.CredentialsSecret != nil {
		credentials = *s.CredentialsSecret
	}
	if credentials.AppTokenKey == "" {
		credentials.AppTokenKey = DefaultPermissionSyncGLPIAppTokenKey
	}
	if credentials.UserTokenKey == "" {
		credentials.UserTokenKey = DefaultPermissionSyncGLPIUserTokenKey
	}
	return credentials
}

// EffectiveEndpoint returns the Permission Provider endpoint for objects that
// bypassed admission defaulting.
func (s *NetEyePermissionSyncProviderSpec) EffectiveEndpoint() string {
	if s == nil || s.Endpoint == "" {
		return DefaultPermissionSyncProviderEndpoint
	}
	return s.Endpoint
}

// EffectiveEndpoint returns the GLPI endpoint for objects that bypassed
// admission defaulting.
func (s *NetEyePermissionSyncGLPISpec) EffectiveEndpoint() string {
	if s == nil || s.Endpoint == "" {
		return DefaultPermissionSyncGLPIEndpoint
	}
	return s.Endpoint
}

// EffectiveOverallDeadlineMilliseconds returns the request deadline.
func (s *NetEyePermissionSyncRequestSpec) EffectiveOverallDeadlineMilliseconds() int64 {
	if s == nil || s.OverallDeadlineMilliseconds == 0 {
		return DefaultPermissionSyncOverallDeadlineMilliseconds
	}
	return s.OverallDeadlineMilliseconds
}

// EffectiveInboundAdmissionLimit returns the inbound admission limit.
func (s *NetEyePermissionSyncRequestSpec) EffectiveInboundAdmissionLimit() int64 {
	if s == nil || s.InboundAdmissionLimit == 0 {
		return DefaultPermissionSyncInboundAdmissionLimit
	}
	return s.InboundAdmissionLimit
}

// EffectiveSynchronizationCapacity returns the synchronization capacity.
func (s *NetEyePermissionSyncRequestSpec) EffectiveSynchronizationCapacity() int64 {
	if s == nil || s.SynchronizationCapacity == 0 {
		return DefaultPermissionSyncSynchronizationCapacity
	}
	return s.SynchronizationCapacity
}

// EffectiveGraceMilliseconds returns the shutdown grace period.
func (s *NetEyePermissionSyncShutdownSpec) EffectiveGraceMilliseconds() int64 {
	if s == nil || s.GraceMilliseconds == 0 {
		return DefaultPermissionSyncShutdownGraceMilliseconds
	}
	return s.GraceMilliseconds
}

// EffectiveMetadataOperationTimeoutMilliseconds returns the metadata timeout.
func (s *NetEyePermissionSyncAuthenticationSpec) EffectiveMetadataOperationTimeoutMilliseconds() int64 {
	if s == nil || s.MetadataOperationTimeoutMilliseconds == 0 {
		return DefaultPermissionSyncMetadataTimeoutMilliseconds
	}
	return s.MetadataOperationTimeoutMilliseconds
}

// EffectiveCacheFreshnessMilliseconds returns the cache freshness window.
func (s *NetEyePermissionSyncAuthenticationSpec) EffectiveCacheFreshnessMilliseconds() int64 {
	if s == nil || s.CacheFreshnessMilliseconds == 0 {
		return DefaultPermissionSyncCacheFreshnessMilliseconds
	}
	return s.CacheFreshnessMilliseconds
}

// EffectiveCacheStaleIfErrorMilliseconds returns the stale-if-error window. It
// is a pointer field because zero is a meaningful value, not an unset one.
func (s *NetEyePermissionSyncAuthenticationSpec) EffectiveCacheStaleIfErrorMilliseconds() int64 {
	if s == nil || s.CacheStaleIfErrorMilliseconds == nil {
		return DefaultPermissionSyncCacheStaleIfErrorMilliseconds
	}
	return *s.CacheStaleIfErrorMilliseconds
}

// EffectiveClockSkewMilliseconds returns the clock skew allowance. It is a
// pointer field because zero means "no allowance", not "unset".
func (s *NetEyePermissionSyncAuthenticationSpec) EffectiveClockSkewMilliseconds() int64 {
	if s == nil || s.ClockSkewMilliseconds == nil {
		return DefaultPermissionSyncClockSkewMilliseconds
	}
	return *s.ClockSkewMilliseconds
}

// EffectiveOperationTimeoutMilliseconds returns the Provider and GLPI
// operation timeout.
func (s *NetEyePermissionSyncSpec) EffectiveOperationTimeoutMilliseconds() int64 {
	if s == nil || s.OperationTimeoutMilliseconds == 0 {
		return DefaultPermissionSyncOperationTimeoutMilliseconds
	}
	return s.OperationTimeoutMilliseconds
}

// LogicalTargets returns the declared logical targets in spec order.
func (s *NetEyePermissionSyncSpec) LogicalTargets() []string {
	if s == nil {
		return nil
	}
	targets := make([]string, 0, len(s.Targets))
	for _, target := range s.Targets {
		targets = append(targets, target.LogicalTarget)
	}
	return targets
}

// permissionSyncImage is the PermissionSync release this operator line ships,
// pinned by immutable digest as ADR-0003 requires. The mutable "latest" tag is
// never referenced here. RELATED_IMAGE_PERMISSIONSYNC overrides it for
// development bundles.
const permissionSyncImage = "ghcr.io/neteye-platform/permissionsync:0.1.0@sha256:d52ca3dc298291d79edb7ba7b5ea67726ef9ff1dd2b636da7a9ad05c4b90563a"

// netEyeVersionMap maps a NetEye version string to its component image set.
// Add new entries here when a NetEye release ships a new Keycloak (or other)
// image version.
var netEyeVersionMap = map[string]NetEyeComponents{
	CurrentNetEyeVersion: {KeycloakImage: "ghcr.io/neteye-platform/neteye-keycloak:1.0.6@sha256:e58681c26f89d305d87a38ce20b81bfb8dfd47fc5d6068ebec2a609bf89c3ce2", OTelCollectorImage: "docker.io/otel/opentelemetry-collector-contrib:0.161.0@sha256:fd328de2552466ad78385e1b1289c3f2402b1c45f265b252aab1955b42845ac1", EDOTGatewayImage: "docker.elastic.co/elastic-agent/elastic-otel-collector:9.5.4@sha256:0597fe7cad118fcaee15a8691088eff7b60fdd1501bd84fc91b90066d79c9afd", CABundleImage: "docker.io/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6", PermissionSyncImage: permissionSyncImage},
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
	// RelatedImagePermissionSyncEnv overrides the PermissionSync image packaged with the operator.
	RelatedImagePermissionSyncEnv = "RELATED_IMAGE_PERMISSIONSYNC"
	CurrentNetEyeVersion          = "4.51"
	PreviousNetEyeVersion         = "4.50"
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
	if image := strings.TrimSpace(os.Getenv(RelatedImagePermissionSyncEnv)); image != "" {
		c.PermissionSyncImage = image
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
	// Version is the NetEye product version string, e.g. "4.51".
	// It is used to resolve the correct component images (Keycloak, etc.).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+$`
	// +kubebuilder:example="4.51"
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

	// PermissionSync configures the PermissionSync service, which reconciles a
	// user's desired permissions with a selected target on login. The service
	// is part of every NetEye deployment and cannot be switched off, so this
	// section is required; its image is resolved from release data.
	// +kubebuilder:validation:Required
	PermissionSync NetEyePermissionSyncSpec `json:"permissionSync"`
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

	// PermissionSync reports the observed state of the PermissionSync service.
	PermissionSync *NetEyeServiceStatus `json:"permissionSync,omitempty"`
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
