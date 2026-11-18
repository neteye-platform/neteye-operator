// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// PermissionSyncEnvVarPrefix starts every PermissionSync environment variable.
const PermissionSyncEnvVarPrefix = "PERMISSIONSYNC_"

// permissionSyncManagedEnvVars are the PermissionSync variables that the
// operator sets itself. They follow the values that the operator needs for the
// network policy, the trusted issuer and the Pod lifecycle, so
// podExtraEnvVars cannot replace them.
var permissionSyncManagedEnvVars = map[string]struct{}{
	"PERMISSIONSYNC_CONFIG_FILE":                                {},
	"PERMISSIONSYNC_LISTENER__ADDRESS":                          {},
	"PERMISSIONSYNC_LISTENER__PORT":                             {},
	"PERMISSIONSYNC_REQUEST__OVERALL_DEADLINE_MILLISECONDS":     {},
	"PERMISSIONSYNC_SHUTDOWN__GRACE_MILLISECONDS":               {},
	"PERMISSIONSYNC_AUTHENTICATION__ISSUER":                     {},
	"PERMISSIONSYNC_AUTHENTICATION__AUDIENCE":                   {},
	"PERMISSIONSYNC_AUTHENTICATION__SOURCE__JWKS_URI":           {},
	"PERMISSIONSYNC_AUTHENTICATION__SOURCE__OIDC_DISCOVERY_URI": {},
	"PERMISSIONSYNC_PROVIDER__GENERIC_REST__ENDPOINT":           {},
	"PERMISSIONSYNC_GLPI__ENDPOINT":                             {},
	"PERMISSIONSYNC_GLPI__APP_TOKEN":                            {},
	"PERMISSIONSYNC_GLPI__USER_TOKEN":                           {},
}

// permissionSyncTracingEnvVarPrefix starts the variables of the trace export.
// Its headers carry credentials, which podExtraEnvVars must not hold, and the
// network policy of the workload allows no egress to an exporter, so trace
// export is not available through podExtraEnvVars.
const permissionSyncTracingEnvVarPrefix = "PERMISSIONSYNC_OBSERVABILITY__TRACING__"

// IsPermissionSyncManagedEnvVar reports whether the operator sets the variable
// itself, or whether podExtraEnvVars cannot set it.
func IsPermissionSyncManagedEnvVar(name string) bool {
	_, managed := permissionSyncManagedEnvVars[name]
	return managed || strings.HasPrefix(name, permissionSyncTracingEnvVarPrefix)
}

// PermissionSyncOverallDeadlineMilliseconds is the request deadline that the
// operator sets. PermissionSync refuses to start with an operation timeout
// longer than it.
const PermissionSyncOverallDeadlineMilliseconds = 10000

// permissionSyncBoundedEnvVars are the numeric tuning variables whose range
// PermissionSync checks at startup. A value out of range stops the startup, so
// the Pods would restart again and again; the webhook refuses it first.
var permissionSyncBoundedEnvVars = map[string]struct{ min, max int64 }{
	"PERMISSIONSYNC_REQUEST__INBOUND_ADMISSION_LIMIT":                        {1, 1 << 20},
	"PERMISSIONSYNC_REQUEST__SYNCHRONIZATION_CAPACITY":                       {1, 1024},
	"PERMISSIONSYNC_AUTHENTICATION__METADATA_OPERATION_TIMEOUT_MILLISECONDS": {1, PermissionSyncOverallDeadlineMilliseconds},
	"PERMISSIONSYNC_AUTHENTICATION__CACHE__FRESHNESS_MILLISECONDS":           {1, 1 << 40},
	"PERMISSIONSYNC_AUTHENTICATION__CACHE__STALE_IF_ERROR_MILLISECONDS":      {0, 1 << 40},
	"PERMISSIONSYNC_AUTHENTICATION__CLOCK_SKEW_MILLISECONDS":                 {0, 300000},
	"PERMISSIONSYNC_PROVIDER__GENERIC_REST__OPERATION_TIMEOUT_MILLISECONDS":  {1, PermissionSyncOverallDeadlineMilliseconds},
	"PERMISSIONSYNC_GLPI__OPERATION_TIMEOUT_MILLISECONDS":                    {1, PermissionSyncOverallDeadlineMilliseconds},
}

// permissionSyncSignedEnvVars are the signed integer variables of the GLPI
// authentication source. PermissionSync needs both or neither.
var permissionSyncSignedEnvVars = []string{
	"PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHTYPE",
	"PERMISSIONSYNC_GLPI__AUTHENTICATION_SOURCE__AUTHS_ID",
}

// permissionSyncUnsigned and permissionSyncSigned are the integer forms that
// PermissionSync reads from a variable: digits without a leading zero, and
// for a signed value an optional minus sign.
var (
	permissionSyncUnsigned = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	permissionSyncSigned   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
)

const permissionSyncLogLevelEnvVar = "PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL"

var permissionSyncLogLevels = []string{"error", "warn", "info", "debug", "trace"}

// The variables of the provider and GLPI sections. PermissionSync creates a
// missing section for such a variable, and that section has no endpoint, so
// the operator passes them only with their section. The installer can set
// them before the GLPI module adds the GLPI section.
const (
	permissionSyncProviderEnvVarPrefix = PermissionSyncEnvVarPrefix + "PROVIDER__"
	permissionSyncGLPIEnvVarPrefix     = PermissionSyncEnvVarPrefix + "GLPI__"
)

// PermissionSyncEnvVarApplies reports whether the operator passes a
// podExtraEnvVars variable to PermissionSync: a provider or GLPI variable
// only when its section is set.
func PermissionSyncEnvVarApplies(spec *NetEyeTenantPermissionSyncSpec, name string) bool {
	switch {
	case strings.HasPrefix(name, permissionSyncProviderEnvVarPrefix):
		return spec.Provider != nil
	case strings.HasPrefix(name, permissionSyncGLPIEnvVarPrefix):
		return spec.GLPI != nil
	default:
		return true
	}
}

// ValidatePermissionSync checks the rules of spec.permissionSync that the CRD
// schema does not check. The webhook and the controller use it, so a tenant
// that bypassed admission is not deployed with a configuration that
// PermissionSync refuses at startup.
func ValidatePermissionSync(spec *NetEyeTenantPermissionSyncSpec) field.ErrorList {
	path := field.NewPath("spec", "permissionSync")
	var errors field.ErrorList
	if spec.Realm != "" && url.PathEscape(spec.Realm) != spec.Realm {
		errors = append(errors, field.Invalid(path.Child("realm"), spec.Realm, "must not contain characters that a URL path escapes, such as spaces, slashes or percent signs"))
	}
	// An endpoint can carry credentials in its user information, so the
	// errors never repeat its value: the controller writes them to the status.
	if spec.Provider != nil {
		if _, err := PermissionSyncEndpointPort(spec.Provider.Endpoint); err != nil {
			errors = append(errors, field.Invalid(path.Child("provider", "endpoint"), redactedEndpoint, err.Error()))
		}
	}
	if spec.GLPI != nil {
		if _, err := PermissionSyncEndpointPort(spec.GLPI.Endpoint); err != nil {
			errors = append(errors, field.Invalid(path.Child("glpi", "endpoint"), redactedEndpoint, err.Error()))
		} else if !isGLPIRESTEndpoint(spec.GLPI.Endpoint) {
			errors = append(errors, field.Invalid(path.Child("glpi", "endpoint"), redactedEndpoint, "must be the GLPI REST API, whose path ends with /apirest.php"))
		}
		credentials := path.Child("glpi", "credentialsSecretRef")
		for _, message := range validation.IsDNS1123Subdomain(spec.GLPI.CredentialsSecretRef.Name) {
			errors = append(errors, field.Invalid(credentials.Child("name"), spec.GLPI.CredentialsSecretRef.Name, message))
		}
		for key, value := range map[string]string{"appTokenKey": spec.GLPI.CredentialsSecretRef.AppTokenKey, "userTokenKey": spec.GLPI.CredentialsSecretRef.UserTokenKey} {
			if value == "" {
				continue
			}
			for _, message := range validation.IsConfigMapKey(value) {
				errors = append(errors, field.Invalid(credentials.Child(key), value, message))
			}
		}
	}
	if len(spec.Targets) > 0 && spec.Provider == nil {
		errors = append(errors, field.Required(path.Child("provider"), "the targets need the provider, which gives the desired permissions"))
	}
	for i, target := range spec.Targets {
		targetPath := path.Child("targets").Index(i)
		if err := validateLogicalTarget(target.LogicalTarget); err != nil {
			errors = append(errors, field.Invalid(targetPath.Child("logicalTarget"), target.LogicalTarget, err.Error()))
		}
		if target.Adapter == PermissionSyncGLPIAdapter && spec.GLPI == nil {
			errors = append(errors, field.Required(path.Child("glpi"), fmt.Sprintf("the target %q uses the glpi adapter", target.LogicalTarget)))
		}
	}
	signed := 0
	for i, variable := range spec.PodExtraEnvVars {
		variablePath := path.Child("podExtraEnvVars").Index(i)
		if IsPermissionSyncManagedEnvVar(variable.Name) {
			errors = append(errors, field.Forbidden(variablePath.Child("name"), fmt.Sprintf("%s is set by the operator or not available in a tenant", variable.Name)))
			continue
		}
		if bounds, bounded := permissionSyncBoundedEnvVars[variable.Name]; bounded {
			value, err := strconv.ParseInt(variable.Value, 10, 64)
			if !permissionSyncUnsigned.MatchString(variable.Value) || err != nil || value < bounds.min || value > bounds.max {
				errors = append(errors, field.Invalid(variablePath.Child("value"), variable.Value, fmt.Sprintf("must be a whole number from %d to %d, without a sign or a leading zero", bounds.min, bounds.max)))
			}
		}
		if slices.Contains(permissionSyncSignedEnvVars, variable.Name) {
			if PermissionSyncEnvVarApplies(spec, variable.Name) {
				signed++
			}
			if _, err := strconv.ParseInt(variable.Value, 10, 64); !permissionSyncSigned.MatchString(variable.Value) || err != nil {
				errors = append(errors, field.Invalid(variablePath.Child("value"), variable.Value, "must be a 64-bit whole number without a plus sign or a leading zero"))
			}
		}
		if variable.Name == permissionSyncLogLevelEnvVar && !slices.Contains(permissionSyncLogLevels, variable.Value) {
			errors = append(errors, field.NotSupported(variablePath.Child("value"), variable.Value, permissionSyncLogLevels))
		}
	}
	if signed == 1 {
		errors = append(errors, field.Required(path.Child("podExtraEnvVars"), fmt.Sprintf("%s must be set together", strings.Join(permissionSyncSignedEnvVars, " and "))))
	}
	return errors
}

// redactedEndpoint replaces the value of an endpoint in an error.
const redactedEndpoint = "<endpoint>"

// isGLPIRESTEndpoint follows the GLPI adapter of PermissionSync: the last
// segment of the path is apirest.php, with an optional trailing slash.
func isGLPIRESTEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/apirest.php")
}

// PermissionSyncEndpointPort checks an endpoint the way PermissionSync does:
// an absolute HTTPS URL with a host and no credentials, query or fragment. It
// returns the port, which the network policy of the workload opens.
func PermissionSyncEndpointPort(endpoint string) (int32, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return 0, fmt.Errorf("must be an absolute https URL with a host and without credentials, query or fragment")
	}
	if net.ParseIP(parsed.Hostname()) == nil && !isDNSName(parsed.Hostname()) {
		return 0, fmt.Errorf("must have a valid host name")
	}
	if parsed.Port() == "" {
		return 443, nil
	}
	port, err := strconv.ParseInt(parsed.Port(), 10, 32)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("must have a port between 1 and 65535")
	}
	return int32(port), nil
}

// validateLogicalTarget follows the target grammar of PermissionSync: 1 to 64
// characters, lowercase letters and digits at both ends, and dots,
// underscores or hyphens only inside.
func validateLogicalTarget(value string) error {
	if value == "" || len(value) > 64 {
		return fmt.Errorf("must have 1 to 64 characters")
	}
	for i := range len(value) {
		character := value[i]
		edge := i == 0 || i == len(value)-1
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
		case !edge && (character == '.' || character == '_' || character == '-'):
		default:
			return fmt.Errorf("must use lowercase letters and digits, with dots, underscores or hyphens only inside")
		}
	}
	return nil
}

// isDNSName accepts a host name in any letter case, as PermissionSync does:
// DNS names are case-insensitive.
func isDNSName(value string) bool {
	if len(value) > 253 {
		return false
	}
	for label := range strings.SplitSeq(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}
