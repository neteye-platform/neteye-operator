// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

// PermissionSync loads exactly one YAML document, whose path comes only from
// PERMISSIONSYNC_CONFIG_FILE. Unknown fields are rejected rather than ignored,
// so this renderer owns the complete document and the operator is the only
// writer of the Secret carrying it.
const (
	// ConfigSecretName holds the rendered configuration document. It is a
	// Secret rather than a ConfigMap because the single document carries the
	// GLPI credentials and the private trust material inline; PermissionSync
	// itself neither requires nor understands any secret backend.
	ConfigSecretName = "permissionsync-config"
	// ConfigFileName is the key inside that Secret and the mounted file name.
	ConfigFileName = "permissionsync.yaml"
	// ConfigMountPath is the read-only directory the document is mounted at.
	ConfigMountPath = "/etc/permissionsync"
	// ConfigFileEnvVar is the one environment value the image needs.
	ConfigFileEnvVar = "PERMISSIONSYNC_CONFIG_FILE"
	// ListenerPort is the single listener port. PermissionSync bakes in no
	// port, and TLS termination in front of the listener is a deployment
	// concern outside this component.
	ListenerPort = permissionsyncconfig.ListenerPort

	listenerAddress = "0.0.0.0"
	// identityRealm is the realm NetEye's single-tenant setup uses, the same
	// one the OTel Collector derives its OIDC issuer from.
	identityRealm = "master"
	// signingAlgorithm keeps the allowlist as small as the deployment issues.
	signingAlgorithm = "RS256"
)

// document is the complete configuration document. Field names are the wire
// contract: PermissionSync rejects an unknown or misspelled field instead of
// ignoring it.
type document struct {
	Listener       listenerSection       `json:"listener"`
	Request        requestSection        `json:"request"`
	Shutdown       shutdownSection       `json:"shutdown"`
	Authentication authenticationSection `json:"authentication"`
	Observability  observabilitySection  `json:"observability"`
	Provider       *providerSection      `json:"provider,omitempty"`
	GLPI           *glpiSection          `json:"glpi,omitempty"`
	// Targets is always rendered, including as an empty list: the section is
	// required, and a null value would abort startup.
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
	OIDCDiscoveryURI string `json:"oidc_discovery_uri"`
}

type cacheSection struct {
	FreshnessMilliseconds    int64 `json:"freshness_milliseconds"`
	StaleIfErrorMilliseconds int64 `json:"stale_if_error_milliseconds"`
}

type observabilitySection struct {
	// LogLevel is the only configurable part of the log output. Trace export
	// stays absent: PermissionSync exports spans only to an absolute HTTPS
	// OTLP endpoint, which the in-cluster EDOT Gateway is not.
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

type glpiSection struct {
	Endpoint                     string                           `json:"endpoint"`
	AppToken                     string                           `json:"app_token"`
	UserToken                    string                           `json:"user_token"`
	OperationTimeoutMilliseconds int64                            `json:"operation_timeout_milliseconds"`
	AdditionalTrustAnchorsPEM    []string                         `json:"additional_trust_anchors_pem,omitempty"`
	AuthenticationSource         *glpiAuthenticationSourceSection `json:"authentication_source,omitempty"`
}

type glpiAuthenticationSourceSection struct {
	AuthType int64 `json:"authtype"`
	AuthsID  int64 `json:"auths_id"`
}

type targetSection struct {
	LogicalTarget string `json:"logical_target"`
	Adapter       string `json:"adapter"`
}

// configurationInput is the resolved material one rendering needs: the spec,
// the identity hostname the issuer is derived from, and the credentials and
// trust material read from the user-managed Secrets.
type configurationInput struct {
	spec             *neteye.NetEyePermissionSyncSpec
	identityHostname string
	trustAnchorsPEM  []string
	glpiAppToken     string
	glpiUserToken    string
}

// renderConfiguration renders the complete document. Validation of the spec
// happens before this point, so a failure here is an encoding defect.
func renderConfiguration(in configurationInput) ([]byte, error) {
	issuer := identityIssuer(in.identityHostname)
	request, shutdown, authentication := &in.spec.Request, &in.spec.Shutdown, &in.spec.Authentication
	doc := document{
		Listener: listenerSection{Address: listenerAddress, Port: ListenerPort},
		Request: requestSection{
			OverallDeadlineMilliseconds: request.EffectiveOverallDeadlineMilliseconds(),
			InboundAdmissionLimit:       request.EffectiveInboundAdmissionLimit(),
			SynchronizationCapacity:     request.EffectiveSynchronizationCapacity(),
		},
		Shutdown: shutdownSection{GraceMilliseconds: shutdown.EffectiveGraceMilliseconds()},
		Authentication: authenticationSection{
			Issuer:     issuer,
			Audience:   permissionsyncconfig.Audience,
			Algorithms: []string{signingAlgorithm},
			Source:     sourceSection{OIDCDiscoveryURI: issuer + "/.well-known/openid-configuration"},
			Cache: cacheSection{
				FreshnessMilliseconds:    authentication.EffectiveCacheFreshnessMilliseconds(),
				StaleIfErrorMilliseconds: authentication.EffectiveCacheStaleIfErrorMilliseconds(),
			},
			MetadataOperationTimeoutMilliseconds: authentication.EffectiveMetadataOperationTimeoutMilliseconds(),
			ClockSkewMilliseconds:                authentication.EffectiveClockSkewMilliseconds(),
			AdditionalTrustAnchorsPEM:            in.trustAnchorsPEM,
		},
		Observability: observabilitySection{LogLevel: in.spec.EffectiveLogLevel()},
		Targets:       make([]targetSection, 0, len(in.spec.Targets)),
	}
	operationTimeout := in.spec.EffectiveOperationTimeoutMilliseconds()
	// The Provider section is always rendered: the component reads desired
	// state from the NetEye deployment's own API, so there is no installation
	// without one.
	doc.Provider = &providerSection{GenericRest: genericRestSection{
		Endpoint:                     in.spec.Provider.EffectiveEndpoint(),
		OperationTimeoutMilliseconds: operationTimeout,
		AdditionalTrustAnchorsPEM:    in.trustAnchorsPEM,
	}}
	if glpi := in.spec.GLPI; glpi != nil {
		doc.GLPI = &glpiSection{
			Endpoint:                     glpi.EffectiveEndpoint(),
			AppToken:                     in.glpiAppToken,
			UserToken:                    in.glpiUserToken,
			OperationTimeoutMilliseconds: operationTimeout,
			AdditionalTrustAnchorsPEM:    in.trustAnchorsPEM,
		}
		if source := glpi.AuthenticationSource; source != nil {
			doc.GLPI.AuthenticationSource = &glpiAuthenticationSourceSection{AuthType: *source.AuthType, AuthsID: *source.AuthsID}
		}
	}
	for _, target := range in.spec.Targets {
		doc.Targets = append(doc.Targets, targetSection{LogicalTarget: target.LogicalTarget, Adapter: target.Adapter})
	}
	rendered, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("render permissionsync configuration: %w", err)
	}
	return rendered, nil
}

// identityIssuer derives the trusted issuer from the identity hostname, the
// same way the telemetry pipeline derives its OIDC issuer. PermissionSync
// matches it exactly against the token, so it must be the hostname Keycloak
// issues tokens under rather than an in-cluster Service URL.
func identityIssuer(hostname string) string {
	return "https://" + hostname + keycloakconfig.HTTPRelativePath + "/realms/" + identityRealm
}

// endpointTarget is one external host and port the workload must be allowed to
// reach.
type endpointTarget struct{ host, port string }

// validateSpec rejects a configuration the operator must not render, and
// returns the egress targets the declared endpoints imply. Admission validates
// the same rules, but a webhook can be bypassed or unavailable, and the egress
// policy needs the parsed hosts either way.
func validateSpec(spec *neteye.NetEyePermissionSyncSpec, identityHostname string) ([]endpointTarget, error) {
	if spec == nil {
		return nil, fmt.Errorf("permissionsync configuration is required")
	}
	// A zero value means "unset" throughout this API and is resolved by
	// EffectiveReplicas, so only a negative count is refusable here.
	if spec.Replicas < 0 {
		return nil, fmt.Errorf("permissionsync replicas must not be negative")
	}
	if err := validateDNSName(identityHostname, "identity hostname"); err != nil {
		return nil, err
	}
	if err := validateLogLevel(spec.EffectiveLogLevel()); err != nil {
		return nil, err
	}
	if err := validateBounds(spec); err != nil {
		return nil, err
	}
	targets := map[string]endpointTarget{}
	providerTarget, err := endpointTargetFor(spec.Provider.EffectiveEndpoint(), "provider endpoint")
	if err != nil {
		return nil, err
	}
	targets[providerTarget.host+":"+providerTarget.port] = providerTarget
	if spec.GLPI != nil {
		target, err := endpointTargetFor(spec.GLPI.EffectiveEndpoint(), "glpi endpoint")
		if err != nil {
			return nil, err
		}
		targets[target.host+":"+target.port] = target
		if source := spec.GLPI.AuthenticationSource; source != nil && (source.AuthType == nil || source.AuthsID == nil) {
			return nil, fmt.Errorf("glpi authentication source requires both authType and authsId")
		}
	}
	if err := validateTargets(spec); err != nil {
		return nil, err
	}
	return sortedTargets(targets), nil
}

// validateBounds enforces the relations PermissionSync validates at startup.
// Violating one aborts the process instead of degrading a component, so the
// operator refuses the configuration rather than rendering it.
func validateBounds(spec *neteye.NetEyePermissionSyncSpec) error {
	deadline := spec.Request.EffectiveOverallDeadlineMilliseconds()
	if deadline <= 0 {
		return fmt.Errorf("request overall deadline must be positive")
	}
	if limit := spec.Request.EffectiveInboundAdmissionLimit(); limit <= 0 {
		return fmt.Errorf("inbound admission limit must be positive")
	}
	capacity := spec.Request.EffectiveSynchronizationCapacity()
	if capacity <= 0 || capacity > neteye.MaxPermissionSyncSynchronizationCapacity {
		return fmt.Errorf("synchronization capacity must be between 1 and %d", neteye.MaxPermissionSyncSynchronizationCapacity)
	}
	if grace := spec.Shutdown.EffectiveGraceMilliseconds(); grace < deadline {
		return fmt.Errorf("shutdown grace must be at least the overall request deadline of %d ms", deadline)
	}
	metadata := spec.Authentication.EffectiveMetadataOperationTimeoutMilliseconds()
	if metadata <= 0 || metadata > deadline {
		return fmt.Errorf("authentication metadata timeout must be positive and at most the overall request deadline of %d ms", deadline)
	}
	if freshness := spec.Authentication.EffectiveCacheFreshnessMilliseconds(); freshness <= 0 {
		return fmt.Errorf("authentication cache freshness must be positive")
	}
	if stale := spec.Authentication.EffectiveCacheStaleIfErrorMilliseconds(); stale < 0 {
		return fmt.Errorf("authentication cache stale-if-error must not be negative")
	}
	skew := spec.Authentication.EffectiveClockSkewMilliseconds()
	if skew < 0 || skew > neteye.MaxPermissionSyncClockSkewMilliseconds {
		return fmt.Errorf("authentication clock skew must be between 0 and %d ms", neteye.MaxPermissionSyncClockSkewMilliseconds)
	}
	operation := spec.EffectiveOperationTimeoutMilliseconds()
	if operation <= 0 || operation > deadline {
		return fmt.Errorf("operation timeout must be positive and at most the overall request deadline of %d ms", deadline)
	}
	return nil
}

// validateTargets enforces the routes' own grammar and the component
// dependencies a route implies. A route whose backend is unconfigured stays
// recognized but unavailable inside PermissionSync, which is a silent failure
// for the logins it serves, so it is refused here instead.
func validateTargets(spec *neteye.NetEyePermissionSyncSpec) error {
	seen := make(map[string]struct{}, len(spec.Targets))
	for _, target := range spec.Targets {
		if err := validateLogicalTarget(target.LogicalTarget); err != nil {
			return err
		}
		if _, exists := seen[target.LogicalTarget]; exists {
			return fmt.Errorf("duplicate logical target %q", target.LogicalTarget)
		}
		seen[target.LogicalTarget] = struct{}{}
		if target.Adapter != neteye.PermissionSyncGLPIAdapter {
			return fmt.Errorf("unsupported adapter %q for logical target %q", target.Adapter, target.LogicalTarget)
		}
		if spec.GLPI == nil {
			return fmt.Errorf("logical target %q selects the glpi adapter, which requires the glpi configuration", target.LogicalTarget)
		}
	}
	return nil
}

// validateLogicalTarget mirrors PermissionSync's target grammar: a lowercase
// letter or digit at each edge, and dots, underscores, or hyphens allowed only
// inside, with at most 64 characters.
func validateLogicalTarget(value string) error {
	if value == "" || len(value) > 64 {
		return fmt.Errorf("logical target %q must be between 1 and 64 characters", value)
	}
	for i := range len(value) {
		character := value[i]
		edge := i == 0 || i == len(value)-1
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
		case !edge && (character == '.' || character == '_' || character == '-'):
		default:
			return fmt.Errorf("logical target %q does not satisfy the PermissionSync target grammar", value)
		}
	}
	return nil
}

func validateLogLevel(value string) error {
	switch value {
	case "error", "warn", "info", "debug", "trace":
		return nil
	default:
		return fmt.Errorf("unsupported log level %q", value)
	}
}

// endpointTargetFor validates one configured endpoint and returns the host and
// port it resolves to. PermissionSync requires absolute HTTPS URIs with no
// query or fragment, with certificate and hostname validation always enabled.
func endpointTargetFor(value, field string) (endpointTarget, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return endpointTarget{}, fmt.Errorf("%s must be an absolute https URI without credentials, query, or fragment", field)
	}
	if net.ParseIP(parsed.Hostname()) == nil {
		if err := validateDNSName(parsed.Hostname(), field); err != nil {
			return endpointTarget{}, err
		}
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	} else {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return endpointTarget{}, fmt.Errorf("%s has an invalid port", field)
		}
	}
	return endpointTarget{host: parsed.Hostname(), port: port}, nil
}

func sortedTargets(unique map[string]endpointTarget) []endpointTarget {
	targets := make([]endpointTarget, 0, len(unique))
	for _, target := range unique {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].host+":"+targets[i].port < targets[j].host+":"+targets[j].port
	})
	return targets
}

func validateDNSName(value, field string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 253 {
		return fmt.Errorf("%s must be a non-empty DNS name", field)
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("%s must be a non-empty DNS name", field)
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return fmt.Errorf("%s must be a non-empty DNS name", field)
			}
		}
	}
	return nil
}
