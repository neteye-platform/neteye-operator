// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package keycloakconfig defines Keycloak configuration owned by the NetEye operator.
package keycloakconfig

const (
	HTTPRelativePath      = "/auth"
	InfinispanClusterName = "neteye-k8s-ispn"
)

// ManagedOption describes a Keycloak option controlled by the NetEye operator.
type ManagedOption struct {
	Name               string
	Value              string
	EmitAsServerOption bool
}

var managedOptions = [...]ManagedOption{
	{Name: "http-relative-path", Value: HTTPRelativePath, EmitAsServerOption: true},
	{Name: "spi-cache-embedded--default--cluster-name", Value: InfinispanClusterName, EmitAsServerOption: true},
	{Name: "proxy-headers"},
}

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
