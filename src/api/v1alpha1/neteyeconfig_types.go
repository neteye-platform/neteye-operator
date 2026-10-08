// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"fmt"
	"os"
	"sort"
	"strconv"
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

// netEyeVersionMap maps a NetEye version string to its component image set.
// Add new entries here when a NetEye release ships a new Keycloak (or other)
// image version.
var netEyeVersionMap = map[string]NetEyeComponents{
	PreviousNetEyeVersion: {KeycloakImage: "ghcr.io/neteye-platform/neteye-keycloak:1.0.5@sha256:1ca9daaa85414c135259c15042462899ec5d804c93cdf16bd94ccb51de2e0c66", OTelCollectorImage: "docker.io/otel/opentelemetry-collector-contrib:0.161.0@sha256:fd328de2552466ad78385e1b1289c3f2402b1c45f265b252aab1955b42845ac1", EDOTGatewayImage: "docker.elastic.co/elastic-agent/elastic-otel-collector:9.5.4@sha256:0597fe7cad118fcaee15a8691088eff7b60fdd1501bd84fc91b90066d79c9afd", CABundleImage: "docker.io/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"},
	CurrentNetEyeVersion:  {KeycloakImage: "ghcr.io/neteye-platform/neteye-keycloak:1.0.6@sha256:e58681c26f89d305d87a38ce20b81bfb8dfd47fc5d6068ebec2a609bf89c3ce2", OTelCollectorImage: "docker.io/otel/opentelemetry-collector-contrib:0.161.0@sha256:fd328de2552466ad78385e1b1289c3f2402b1c45f265b252aab1955b42845ac1", EDOTGatewayImage: "docker.elastic.co/elastic-agent/elastic-otel-collector:9.5.4@sha256:0597fe7cad118fcaee15a8691088eff7b60fdd1501bd84fc91b90066d79c9afd", CABundleImage: "docker.io/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"},
}

// netEyeUpgradeGraph is the explicit forward-upgrade graph required by
// ADR-0003. Upgrade authorization is read from this table rather than inferred
// by comparing version numbers, so an unlisted transition is rejected even
// when it looks like a forward move.
var netEyeUpgradeGraph = map[string][]string{
	PreviousNetEyeVersion: {CurrentNetEyeVersion},
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
	CurrentNetEyeVersion    = "4.51"
	PreviousNetEyeVersion   = "4.50"
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

// IsPreviousVersion returns true if the given NetEye version is the previous
// supported release, which is the upgrade source of this operator line.
func IsPreviousVersion(version string) bool {
	return version == PreviousNetEyeVersion
}

// IsLatestVersion returns true if the given NetEye version is the latest supported version.
func IsLatestVersion(version string) bool {
	return version == CurrentNetEyeVersion
}

// IsSupportedUpgrade reports whether the operator declares an explicit forward
// upgrade from one NetEye release line to another.
func IsSupportedUpgrade(from, to string) bool {
	for _, target := range netEyeUpgradeGraph[from] {
		if target == to {
			return true
		}
	}
	return false
}

// UpgradeTargets returns the release lines reachable from the given release.
func UpgradeTargets(from string) []string {
	targets := append([]string(nil), netEyeUpgradeGraph[from]...)
	sort.Strings(targets)
	return targets
}

// ValidateReleaseData checks the release data embedded in this operator build.
// ADR-0003 requires that the operator never becomes ready with invalid
// embedded release data, so main calls this before starting the manager and the
// unit tests assert it for every build.
func ValidateReleaseData() error {
	var problems []string
	// The support window is the target release plus the immediately previous
	// release, which must stay fully manageable while a product upgrade waits.
	for _, version := range []string{PreviousNetEyeVersion, CurrentNetEyeVersion} {
		components, ok := netEyeVersionMap[version]
		if !ok {
			problems = append(problems, fmt.Sprintf("supported NetEye release %q has no component image set", version))
			continue
		}
		for name, image := range map[string]string{
			"keycloak":       components.KeycloakImage,
			"otel-collector": components.OTelCollectorImage,
			"edot-gateway":   components.EDOTGatewayImage,
			"ca-bundle":      components.CABundleImage,
		} {
			if image == "" {
				problems = append(problems, fmt.Sprintf("NetEye release %q has no %s image", version, name))
				continue
			}
			if !strings.Contains(image, "@sha256:") {
				problems = append(problems, fmt.Sprintf("NetEye release %q %s image %q is not pinned by digest", version, name, image))
			}
		}
	}
	// Upgrade edges must move forward and reference releases that exist.
	for from, targets := range netEyeUpgradeGraph {
		if _, ok := netEyeVersionMap[from]; !ok {
			problems = append(problems, fmt.Sprintf("upgrade edge source %q is not a known NetEye release", from))
		}
		for _, to := range targets {
			if _, ok := netEyeVersionMap[to]; !ok {
				problems = append(problems, fmt.Sprintf("upgrade edge %q -> %q targets an unknown NetEye release", from, to))
				continue
			}
			if !isForwardVersion(from, to) {
				problems = append(problems, fmt.Sprintf("upgrade edge %q -> %q does not move forward", from, to))
			}
		}
	}
	// The window's forward transition has to be declared, or a staged operator
	// update could never authorize the product upgrade it exists to perform.
	if PreviousNetEyeVersion != CurrentNetEyeVersion && !IsSupportedUpgrade(PreviousNetEyeVersion, CurrentNetEyeVersion) {
		problems = append(problems, fmt.Sprintf("no upgrade edge declared from %q to %q", PreviousNetEyeVersion, CurrentNetEyeVersion))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("invalid embedded NetEye release data: %s", strings.Join(problems, "; "))
	}
	return nil
}

// isForwardVersion compares two "major.minor" release lines.
func isForwardVersion(from, to string) bool {
	fromMajor, fromMinor, okFrom := splitVersion(from)
	toMajor, toMinor, okTo := splitVersion(to)
	if !okFrom || !okTo {
		return false
	}
	if toMajor != fromMajor {
		return toMajor > fromMajor
	}
	return toMinor > fromMinor
}

func splitVersion(version string) (int, int, bool) {
	major, minor, found := strings.Cut(version, ".")
	if !found {
		return 0, 0, false
	}
	majorValue, err := strconv.Atoi(major)
	if err != nil {
		return 0, 0, false
	}
	minorValue, err := strconv.Atoi(minor)
	if err != nil {
		return 0, 0, false
	}
	return majorValue, minorValue, true
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

// Condition types reported in NetEyeStatus.Conditions. Ready is the stable
// readiness interface for scripts and users; consumers must also compare its
// observedGeneration with metadata.generation before treating it as current.
const (
	// ConditionReady reports readiness of the NetEye resource as a whole. An
	// available product upgrade does not make an installation unready.
	ConditionReady = "Ready"
	// ConditionProgressing reports that at least one branch is converging. It
	// can be true at the same time as Degraded, when an independent branch can
	// still make progress.
	ConditionProgressing = "Progressing"
	// ConditionDegraded reports that at least one component has an observed
	// failure. A component merely waiting for a healthy prerequisite does not
	// make the installation degraded.
	ConditionDegraded = "Degraded"
	// ConditionUpgradeAvailable reports that the installation is managed by an
	// operator that supports a newer NetEye release. It is orthogonal to
	// readiness, which is why it is a separate condition rather than a phase.
	ConditionUpgradeAvailable = "UpgradeAvailable"
)

// Stable, machine-readable condition reasons. Messages are concise
// explanations for humans and are not a machine-readable API.
const (
	ReasonAllComponentsReady = "AllComponentsReady"
	ReasonComponentsNotReady = "ComponentsNotReady"
	ReasonComponentsDegraded = "ComponentsDegraded"
	ReasonNoFailures         = "NoFailures"
	ReasonConverging         = "Converging"
	ReasonConverged          = "Converged"
	ReasonUpgradeAvailable   = "UpgradeAvailable"
	ReasonNoUpgradeAvailable = "NoUpgradeAvailable"
	ReasonUnsupportedVersion = "UnsupportedVersion"
	ReasonDeletionInProgress = "DeletionInProgress"
	ReasonDeletionFailed     = "DeletionFailed"
	ReasonDependencyNotReady = "DependencyNotReady"
)

// ComponentState is the readiness state reported for one logical component.
type ComponentState string

const (
	// ComponentStateReady means the component reached its desired state.
	ComponentStateReady ComponentState = "Ready"
	// ComponentStateProgressing means the component is converging.
	ComponentStateProgressing ComponentState = "Progressing"
	// ComponentStateDegraded means the component's own reconciliation failed.
	ComponentStateDegraded ComponentState = "Degraded"
	// ComponentStateBlocked means a prerequisite component is not ready. It is
	// distinct from Degraded so automation can tell an intrinsic failure from
	// waiting on a healthy dependency.
	ComponentStateBlocked ComponentState = "Blocked"
	// ComponentStateDisabled means the component is not part of the desired
	// state. A disabled component does not keep the installation from becoming
	// ready.
	ComponentStateDisabled ComponentState = "Disabled"
)

// NetEyeResolvedImage is one container image selected by the operator for a
// component. Name is a stable logical identifier within that component, and
// Image is a complete OCI reference pinned by digest.
type NetEyeResolvedImage struct {
	// Name is the stable logical image name within its component.
	Name string `json:"name"`

	// Image is the exact, digest-pinned image reference.
	Image string `json:"image"`
}

// NetEyeComponentStatus reports the observed state of one logical component.
type NetEyeComponentStatus struct {
	// Status is the component's readiness state.
	Status ComponentState `json:"status,omitempty"`

	// ObservedGeneration is the NetEye generation this entry describes.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Reason is a stable, machine-readable identifier for the current state.
	Reason string `json:"reason,omitempty"`

	// Message is a concise explanation for humans and is not a
	// machine-readable API.
	Message string `json:"message,omitempty"`

	// BlockingDependencies lists the prerequisite components that are not
	// ready. It is only set when Status is Blocked.
	// +kubebuilder:validation:Optional
	// +listType=set
	BlockingDependencies []string `json:"blockingDependencies,omitempty"`

	// ResolvedImages is the complete set of container images the operator
	// selected for this component. A component with no container images
	// reports an empty list. While the component is progressing this is the
	// target set; once it is ready, the operator has confirmed its required
	// workloads use that set.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	ResolvedImages []NetEyeResolvedImage `json:"resolvedImages,omitempty"`
}

// NetEyeStatus defines the observed state of NetEyeConfig.
type NetEyeStatus struct {
	// Phase is the human-readable aggregate lifecycle state. It is a summary
	// for operators reading the resource; automation that needs readiness uses
	// the Ready condition, which can represent independent facts that a single
	// phase value cannot hold at the same time.
	Phase NetEyePhase `json:"phase,omitempty"`

	// Message is a human-readable aggregate status message.
	Message string `json:"message,omitempty"`

	// Conditions is the stable machine-readable status interface. It uses the
	// Kubernetes standard condition schema. Callers must tolerate condition
	// types they do not recognize.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Components reports observed state keyed by stable logical component
	// name. The map is dynamic because components are added as NetEye moves to
	// Kubernetes: adding a key is backward compatible, and callers must
	// tolerate component keys they do not recognize.
	// +kubebuilder:validation:Optional
	Components map[string]NetEyeComponentStatus `json:"components,omitempty"`

	// CurrentVersion is the NetEye product release the operator has
	// successfully applied. During an upgrade it can differ from spec.version,
	// and it only advances once every desired component is ready.
	CurrentVersion string `json:"currentVersion,omitempty"`

	// ServicesStatus reports observed state for each managed NetEye service.
	//
	// DEPRECATED: use Components instead. Components is keyed by stable
	// logical component name, so new components can be added without a schema
	// change, and it carries the per-component observedGeneration, reason, and
	// resolvedImages that this field cannot express. ServicesStatus is still
	// written for compatibility and will be removed in a future API version.
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
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Current",type=string,JSONPath=`.status.currentVersion`
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
