// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package permissionsync deploys the PermissionSync instance of a tenant: the
// configuration file, the workload, its Service and its network policy, all in
// the namespace of the tenant.
package permissionsync

import (
	"fmt"

	"sigs.k8s.io/yaml"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
)

// PermissionSync reads one YAML file, whose path comes from
// PERMISSIONSYNC_CONFIG_FILE, and replaces single values of it from the
// environment. The file that the operator writes has no secret: the GLPI
// credentials reach the Pod as environment variables from a Secret, so the
// file is a ConfigMap.
const (
	// ConfigMapName holds the configuration file.
	ConfigMapName = "permissionsync-config"
	// ConfigFileName is the key in the ConfigMap and the name of the mounted
	// file.
	ConfigFileName = "permissionsync.yaml"
	// ConfigMountPath is the read-only directory of the mounted file.
	ConfigMountPath = "/etc/permissionsync"

	// ListenerPort is the only port of PermissionSync. It serves plain HTTP:
	// TLS in front of it is a deployment concern.
	ListenerPort = int32(8443)
	// Audience is the audience that PermissionSync requires in a caller
	// token.
	Audience = "permissionsync"

	listenerAddress  = "0.0.0.0"
	signingAlgorithm = "RS256"
	logLevel         = "info"

	// The request deadline and the shutdown grace are fixed: the termination
	// grace period of the Pod follows the shutdown grace, and PermissionSync
	// requires the grace to be at least the deadline.
	overallDeadlineMilliseconds = int64(10000)
	shutdownGraceMilliseconds   = int64(20000)

	// Tuning defaults. podExtraEnvVars can replace each of them.
	inboundAdmissionLimit            = int64(64)
	synchronizationCapacity          = int64(16)
	cacheFreshnessMilliseconds       = int64(300000)
	cacheStaleIfErrorMilliseconds    = int64(600000)
	metadataOperationTimeoutMillis   = int64(3000)
	clockSkewMilliseconds            = int64(30000)
	downstreamOperationTimeoutMillis = int64(5000)
	glpiAppTokenEnvVar               = neteye.PermissionSyncEnvVarPrefix + "GLPI__APP_TOKEN"  // #nosec G101 -- variable name, not a credential
	glpiUserTokenEnvVar              = neteye.PermissionSyncEnvVarPrefix + "GLPI__USER_TOKEN" // #nosec G101 -- variable name, not a credential
	configFileEnvVar                 = neteye.PermissionSyncEnvVarPrefix + "CONFIG_FILE"
)

// document is the configuration file. The field names are the contract of
// PermissionSync, which refuses an unknown field.
type document struct {
	Listener       listenerSection       `json:"listener"`
	Request        requestSection        `json:"request"`
	Shutdown       shutdownSection       `json:"shutdown"`
	Authentication authenticationSection `json:"authentication"`
	Observability  observabilitySection  `json:"observability"`
	Provider       *providerSection      `json:"provider,omitempty"`
	GLPI           *glpiSection          `json:"glpi,omitempty"`
	// Targets is required, also when it is empty.
	Targets []targetSection `json:"targets"`
}

type listenerSection struct {
	Address string `json:"address"`
	Port    int32  `json:"port"`
}

type requestSection struct {
	OverallDeadlineMilliseconds int64 `json:"overall_deadline_milliseconds"`
	InboundAdmissionLimit       int64 `json:"inbound_admission_limit"`
	SynchronizationCapacity     int64 `json:"synchronization_capacity"`
}

type shutdownSection struct {
	GraceMilliseconds int64 `json:"grace_milliseconds"`
}

type authenticationSection struct {
	Issuer                               string        `json:"issuer"`
	Audience                             string        `json:"audience"`
	Algorithms                           []string      `json:"algorithms"`
	Source                               sourceSection `json:"source"`
	Cache                                cacheSection  `json:"cache"`
	MetadataOperationTimeoutMilliseconds int64         `json:"metadata_operation_timeout_milliseconds"`
	ClockSkewMilliseconds                int64         `json:"clock_skew_milliseconds"`
	AdditionalTrustAnchorsPEM            []string      `json:"additional_trust_anchors_pem,omitempty"`
}

type sourceSection struct {
	JWKSURI string `json:"jwks_uri"`
}

type cacheSection struct {
	FreshnessMilliseconds    int64 `json:"freshness_milliseconds"`
	StaleIfErrorMilliseconds int64 `json:"stale_if_error_milliseconds"`
}

type observabilitySection struct {
	LogLevel string `json:"log_level"`
}

type providerSection struct {
	GenericRest genericRestSection `json:"generic_rest"`
}

type genericRestSection struct {
	Endpoint                     string   `json:"endpoint"`
	OperationTimeoutMilliseconds int64    `json:"operation_timeout_milliseconds"`
	AdditionalTrustAnchorsPEM    []string `json:"additional_trust_anchors_pem,omitempty"`
}

// glpiSection has no token fields: the tokens come from the environment.
type glpiSection struct {
	Endpoint                     string   `json:"endpoint"`
	OperationTimeoutMilliseconds int64    `json:"operation_timeout_milliseconds"`
	AdditionalTrustAnchorsPEM    []string `json:"additional_trust_anchors_pem,omitempty"`
}

type targetSection struct {
	LogicalTarget string `json:"logical_target"`
	Adapter       string `json:"adapter"`
}

// Issuer returns the issuer that PermissionSync trusts: the URL under which
// Keycloak issues the tokens of the realm, the same that the tokens carry.
func Issuer(identityHostname, realm string) string {
	return "https://" + identityHostname + keycloakconfig.HTTPRelativePath + "/realms/" + realm
}

// JWKSURI returns the URL from which PermissionSync reads the keys of the
// realm. It uses the in-cluster name of Keycloak on the shared Gateway, not the
// public hostname: the keys are the same, and the network policy of the
// workload allows only the path through the Gateway to the Keycloak Pods.
func JWKSURI(realm string) string {
	return "https://" + keycloak.RouteHostname + keycloakconfig.HTTPRelativePath + "/realms/" + realm + "/protocol/openid-connect/certs"
}

// renderConfiguration writes the configuration file of a tenant. The JWKS is
// read directly from the realm, so no discovery request is needed. The root CA
// of NetEye signs the certificates of Keycloak, of the provider and of GLPI, so
// it is the only additional trust anchor.
func renderConfiguration(spec *neteye.NetEyeTenantPermissionSyncSpec, identityHostname, realm, rootCAPEM string) ([]byte, error) {
	trust := []string{rootCAPEM}
	issuer := Issuer(identityHostname, realm)
	doc := document{
		Listener: listenerSection{Address: listenerAddress, Port: ListenerPort},
		Request: requestSection{
			OverallDeadlineMilliseconds: overallDeadlineMilliseconds,
			InboundAdmissionLimit:       inboundAdmissionLimit,
			SynchronizationCapacity:     synchronizationCapacity,
		},
		Shutdown: shutdownSection{GraceMilliseconds: shutdownGraceMilliseconds},
		Authentication: authenticationSection{
			Issuer:                               issuer,
			Audience:                             Audience,
			Algorithms:                           []string{signingAlgorithm},
			Source:                               sourceSection{JWKSURI: JWKSURI(realm)},
			Cache:                                cacheSection{FreshnessMilliseconds: cacheFreshnessMilliseconds, StaleIfErrorMilliseconds: cacheStaleIfErrorMilliseconds},
			MetadataOperationTimeoutMilliseconds: metadataOperationTimeoutMillis,
			ClockSkewMilliseconds:                clockSkewMilliseconds,
			AdditionalTrustAnchorsPEM:            trust,
		},
		Observability: observabilitySection{LogLevel: logLevel},
		Targets:       make([]targetSection, 0, len(spec.Targets)),
	}
	if spec.Provider != nil {
		doc.Provider = &providerSection{GenericRest: genericRestSection{
			Endpoint:                     spec.Provider.Endpoint,
			OperationTimeoutMilliseconds: downstreamOperationTimeoutMillis,
			AdditionalTrustAnchorsPEM:    trust,
		}}
	}
	if spec.GLPI != nil {
		doc.GLPI = &glpiSection{
			Endpoint:                     spec.GLPI.Endpoint,
			OperationTimeoutMilliseconds: downstreamOperationTimeoutMillis,
			AdditionalTrustAnchorsPEM:    trust,
		}
	}
	for _, target := range spec.Targets {
		doc.Targets = append(doc.Targets, targetSection{LogicalTarget: target.LogicalTarget, Adapter: target.Adapter})
	}
	rendered, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("render the PermissionSync configuration: %w", err)
	}
	return rendered, nil
}
