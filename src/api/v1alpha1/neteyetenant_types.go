// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// TenantNamePrefix starts the name of every tenant. The namespace and the
	// NetEyeTenant of a tenant share one name: TenantNamePrefix followed by the
	// tenant id.
	TenantNamePrefix = "neteye-tenant-"

	// TenantNamePattern is the only accepted tenant name. The id has 8
	// lowercase letters and digits, without the characters that look alike
	// (0, o, 1, l, i). A fixed length keeps derived names short enough for the
	// 63 character limit of a DNS label, and it excludes the shared namespace,
	// which has the same prefix. The CRD upgrade check of OLM
	// (crdUpgradeSafety Strict) refuses any change of a CEL rule, so this
	// rule, like the rules of spec.displayName, cannot change in a later
	// release of this API version. The installer generates the id:
	// metadata.generateName cannot produce it, because the API server adds 5
	// characters.
	TenantNamePattern = `^neteye-tenant-[a-hjkmnp-z2-9]{8}$`

	// TenantLabel marks the namespace of a tenant. Its value is the name of the
	// tenant, which is also the name of the namespace. Scripts select the tenant
	// namespaces with this label rather than with the name prefix, because the
	// shared namespace starts with the same prefix. Only the operator sets it.
	TenantLabel = "neteye.cloud/tenant"

	// TenantDisplayNameAnnotation shows the display name of a tenant on its
	// namespace. Only the operator sets it. An annotation is used instead of a
	// label because the display name is free text, which a label value cannot
	// hold.
	TenantDisplayNameAnnotation = "neteye.cloud/tenant-display-name"
)

// NetEyeTenantSpec defines the desired state of a NetEye tenant.
type NetEyeTenantSpec struct {
	// Version is the NetEye product version string of the tenant, e.g. "4.51".
	// The rules are the same as for the version of the NetEye resource: a new
	// tenant uses the version that this operator installs, and an existing
	// tenant can only keep it or upgrade to it from the previous version. A
	// tenant cannot use a newer version than the NetEye resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+$`
	// +kubebuilder:example="4.51"
	Version string `json:"version"`

	// DisplayName is the name of the tenant that people see and use. It can
	// change at any time; resources derived from the tenant use metadata.name,
	// which never changes. It has at least one letter or digit, so a name made
	// only of blank characters is refused. The length counts characters, not
	// bytes. No two tenants have the same display name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == self.trim()",message="must not contain surrounding whitespace"
	// +kubebuilder:validation:XValidation:rule="!self.matches('[\\\\p{Cc}\\\\p{Cf}\\\\p{Zl}\\\\p{Zp}]')",message="must not contain control or format characters, or line or paragraph separators"
	// +kubebuilder:validation:XValidation:rule="self.matches('[\\\\p{L}\\\\p{N}]')",message="must contain at least one letter or digit"
	DisplayName string `json:"displayName"`

	// CABundle selects the optional ConfigMap with extra PEM CA certificates
	// that the components of the tenant trust in addition to their built-in
	// roots. The ConfigMap lives in the namespace of the tenant and may be
	// absent. The default is neteye-ca-bundle/ca-bundle.pem.
	// +kubebuilder:validation:Optional
	CABundle *NetEyeConfigMapKeySelector `json:"caBundle,omitempty"`

	// UseProcedureGates makes the operator wait for and create
	// NetEyeProcedureGates in the namespace of the tenant during its installs
	// and upgrades. Disable it where no NetEye Ansible procedure runs and an
	// administrator performs those steps.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	UseProcedureGates *bool `json:"useProcedureGates,omitempty"`

	// PermissionSync configures the PermissionSync instance of the tenant.
	// PermissionSync runs in every tenant, also without this section: every
	// field is optional and has a default. Keycloak does not call it yet.
	// +kubebuilder:validation:Optional
	PermissionSync NetEyeTenantPermissionSyncSpec `json:"permissionSync,omitzero"`
}

// Default values of the PermissionSync instance of a tenant.
const (
	// DefaultPermissionSyncReplicas runs two Pods, so that one Pod can be
	// rescheduled while the other serves.
	DefaultPermissionSyncReplicas int32 = 2
	// DefaultPermissionSyncGLPIAppTokenKey and DefaultPermissionSyncGLPIUserTokenKey
	// are the keys of the GLPI credentials Secret.
	DefaultPermissionSyncGLPIAppTokenKey  = "app_token"  // #nosec G101 -- Secret key name, not a credential
	DefaultPermissionSyncGLPIUserTokenKey = "user_token" // #nosec G101 -- Secret key name, not a credential
	// PermissionSyncGLPIAdapter is the only PermissionSync adapter.
	PermissionSyncGLPIAdapter = "glpi"
)

// NetEyeTenantPermissionSyncSpec configures the PermissionSync instance of a
// tenant. The operator reads these fields itself: the realm for the trusted
// issuer, the endpoints for the network policy, and the Secret for the
// credentials. Tuning values that only PermissionSync reads go in
// PodExtraEnvVars.
type NetEyeTenantPermissionSyncSpec struct {
	// Realm is the Keycloak realm whose tokens PermissionSync trusts. The
	// default is the realm of the tenant, which has the name of the tenant.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Realm string `json:"realm,omitempty"`

	// Replicas is the number of PermissionSync Pods. The default is 2.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Replicas *int32 `json:"replicas,omitempty"`

	// Provider is the Permission Provider that PermissionSync reads the desired
	// permissions from. Without it, PermissionSync starts and answers only
	// requests without a target.
	// +kubebuilder:validation:Optional
	Provider *NetEyeTenantPermissionSyncProviderSpec `json:"provider,omitempty"`

	// GLPI is the GLPI instance of the glpi adapter. A target with the glpi
	// adapter requires it.
	// +kubebuilder:validation:Optional
	GLPI *NetEyeTenantPermissionSyncGLPISpec `json:"glpi,omitempty"`

	// Targets are the logical targets that PermissionSync serves. Without
	// targets, PermissionSync starts and is ready, and a request for any
	// target is refused.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=logicalTarget
	Targets []NetEyeTenantPermissionSyncTarget `json:"targets,omitempty"`

	// PodExtraEnvVars are copied to the PermissionSync container as they are,
	// for example PERMISSIONSYNC_OBSERVABILITY__LOG_LEVEL. The variables that
	// the operator sets itself are refused.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=name
	PodExtraEnvVars []NetEyeEnvVar `json:"podExtraEnvVars,omitempty"`
}

// NetEyeTenantPermissionSyncProviderSpec identifies a Permission Provider.
type NetEyeTenantPermissionSyncProviderSpec struct {
	// Endpoint is the absolute HTTPS URL of the desired-state API of the
	// provider, without credentials, query or fragment.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`
}

// NetEyeTenantPermissionSyncGLPISpec identifies a GLPI instance and the
// Secret with its API credentials.
type NetEyeTenantPermissionSyncGLPISpec struct {
	// Endpoint is the absolute HTTPS URL of the GLPI REST API, without
	// credentials, query or fragment.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`

	// CredentialsSecretRef is the Secret, in the namespace of the tenant, with
	// the application token and the user token of the GLPI API.
	// +kubebuilder:validation:Required
	CredentialsSecretRef NetEyeTenantPermissionSyncGLPICredentials `json:"credentialsSecretRef"`
}

// NetEyeTenantPermissionSyncGLPICredentials identifies the keys of the GLPI
// credentials Secret.
type NetEyeTenantPermissionSyncGLPICredentials struct {
	// Name is the name of the Secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// AppTokenKey is the key of the application token. The default is
	// app_token.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=253
	AppTokenKey string `json:"appTokenKey,omitempty"`

	// UserTokenKey is the key of the user token. The default is user_token.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=253
	UserTokenKey string `json:"userTokenKey,omitempty"`
}

// NetEyeTenantPermissionSyncTarget routes one logical target to an adapter.
type NetEyeTenantPermissionSyncTarget struct {
	// LogicalTarget is the suffix of the permissionsync:<target> scope of the
	// caller token.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	LogicalTarget string `json:"logicalTarget"`

	// Adapter selects the adapter. glpi is the only adapter.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=glpi
	Adapter string `json:"adapter"`
}

// EffectiveRealm returns the realm that PermissionSync trusts for tenant.
func (s *NetEyeTenantPermissionSyncSpec) EffectiveRealm(tenantName string) string {
	if s.Realm != "" {
		return s.Realm
	}
	return tenantName
}

// EffectiveReplicas returns the number of PermissionSync Pods.
func (s *NetEyeTenantPermissionSyncSpec) EffectiveReplicas() int32 {
	if s.Replicas != nil {
		return *s.Replicas
	}
	return DefaultPermissionSyncReplicas
}

// EffectiveAppTokenKey returns the key of the GLPI application token.
func (c NetEyeTenantPermissionSyncGLPICredentials) EffectiveAppTokenKey() string {
	if c.AppTokenKey != "" {
		return c.AppTokenKey
	}
	return DefaultPermissionSyncGLPIAppTokenKey
}

// EffectiveUserTokenKey returns the key of the GLPI user token.
func (c NetEyeTenantPermissionSyncGLPICredentials) EffectiveUserTokenKey() string {
	if c.UserTokenKey != "" {
		return c.UserTokenKey
	}
	return DefaultPermissionSyncGLPIUserTokenKey
}

// EffectiveUseProcedureGates reports whether procedure gates are honored,
// defaulting to true for objects that bypassed admission defaulting.
func (s *NetEyeTenantSpec) EffectiveUseProcedureGates() bool {
	return s == nil || s.UseProcedureGates == nil || *s.UseProcedureGates
}

// EffectiveCABundle returns the CA bundle ConfigMap selector, applying the
// defaults for objects that bypassed admission defaulting.
func (s *NetEyeTenantSpec) EffectiveCABundle() NetEyeConfigMapKeySelector {
	if s == nil {
		return effectiveCABundle(nil)
	}
	return effectiveCABundle(s.CABundle)
}

// NetEyeTenantStatus defines the observed state of a NetEye tenant. It has the
// same fields as the status of the NetEye resource, apart from the services.
type NetEyeTenantStatus struct {
	// Phase is the aggregate lifecycle state of the tenant.
	Phase NetEyePhase `json:"phase,omitempty"`

	// Message is a human-readable aggregate status message.
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the generation of the most recently observed
	// NetEyeTenant.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// CurrentVersion is the NetEye release whose install or upgrade of the
	// tenant has completed. While it differs from spec.version the operator is
	// installing (when empty) or upgrading the tenant, and waits for the
	// required NetEyeProcedureGates. The NetEye resource can upgrade only when
	// every tenant has completed its version.
	CurrentVersion string `json:"currentVersion,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=neteyetenants,shortName=netenant
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^neteye-tenant-[a-hjkmnp-z2-9]{8}$')",message="metadata.name must be neteye-tenant- followed by an id of 8 characters from abcdefghjkmnpqrstuvwxyz23456789",reason=FieldValueInvalid
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Current",type=string,JSONPath=`.status.currentVersion`,priority=1
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NetEyeTenant is the Schema for the neteyetenants API. It represents one
// NetEye tenant and its dedicated resources. It lives in the namespace of the
// tenant and has the same name as that namespace.
type NetEyeTenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   NetEyeTenantSpec   `json:"spec"`
	Status NetEyeTenantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetEyeTenantList contains a list of NetEyeTenant.
type NetEyeTenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetEyeTenant `json:"items"`
}
