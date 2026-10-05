// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package keycloakconfig defines Keycloak configuration owned by the NetEye operator.
package keycloakconfig

const (
	HTTPRelativePath      = "/auth"
	InfinispanClusterName = "neteye-k8s-ispn"
	InstanceName          = "neteye-kc"

	// FeatureOpenTelemetryLogs and FeatureOpenTelemetryMetrics are the Keycloak
	// features backing the opt-in identity telemetry signals.
	FeatureOpenTelemetryLogs    = "opentelemetry-logs"
	FeatureOpenTelemetryMetrics = "opentelemetry-metrics"
)

// ManagedOption describes a Keycloak option controlled by the NetEye operator.
type ManagedOption struct {
	Name               string
	Value              string
	EmitAsServerOption bool
}

var managedOptions = [...]ManagedOption{
	{Name: "http-relative-path", Value: HTTPRelativePath, EmitAsServerOption: true},
	{Name: "cache-embedded-cluster-name", Value: InfinispanClusterName, EmitAsServerOption: true},
	{Name: "proxy-headers"},
	{Name: "telemetry-logs-enabled"},
	{Name: "telemetry-metrics-enabled"},
	{Name: "metrics-enabled"},
	{Name: "telemetry-endpoint"},
	{Name: "telemetry-protocol"},
	{Name: "telemetry-service-name"},
	{Name: "telemetry-logs-endpoint"},
	{Name: "telemetry-logs-protocol"},
	{Name: "telemetry-metrics-endpoint"},
	{Name: "telemetry-metrics-protocol"},
}

// managedFeatures lists the Keycloak features the operator derives from the
// NetEye spec. They are enabled through their own spec fields, not by name.
var managedFeatures = [...]string{FeatureOpenTelemetryLogs, FeatureOpenTelemetryMetrics}

// ManagedOptions returns the Keycloak options controlled by the NetEye operator.
func ManagedOptions() []ManagedOption {
	options := make([]ManagedOption, len(managedOptions))
	copy(options, managedOptions[:])
	return options
}

// IsManagedOption reports whether name identifies an operator-managed option.
func IsManagedOption(name string) bool {
	for _, option := range managedOptions {
		if option.Name == name {
			return true
		}
	}
	return false
}

// IsManagedFeature reports whether name identifies an operator-managed feature.
func IsManagedFeature(name string) bool {
	for _, feature := range managedFeatures {
		if feature == name {
			return true
		}
	}
	return false
}
