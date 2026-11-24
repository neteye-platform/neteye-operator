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
	// change, except while the tenant is at the previous version: then, as for
	// the NetEye resource, the tenant accepts no change until it upgrades.
	// Resources derived from the tenant use metadata.name, which never changes. It has at least one letter or digit, so a name made
	// only of blank characters is refused; the Hangul fillers, which are
	// letters that show nothing, do not count. The length counts characters,
	// not bytes. Only one tenant holds a display name: the webhook refuses a
	// name that another tenant uses, and when two writes pass the webhook at
	// the same time, the operator gives the name to one tenant and reports the
	// other one as Failed until it gets another display name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == self.trim()",message="must not contain surrounding whitespace"
	// +kubebuilder:validation:XValidation:rule="!self.matches('[\\\\p{Cc}\\\\p{Cf}\\\\p{Zl}\\\\p{Zp}]')",message="must not contain control or format characters, or line or paragraph separators"
	// +kubebuilder:validation:XValidation:rule="self.matches('[^\\\\P{L}\\\\x{115F}\\\\x{1160}\\\\x{3164}\\\\x{FFA0}]|\\\\p{N}')",message="must contain at least one letter or digit"
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

	// Alyvix enables the Alyvix feature module for the tenant. The images
	// come from the release data of the NetEye resource.
	// +kubebuilder:validation:Optional
	Alyvix *NetEyeAlyvixSpec `json:"alyvix,omitempty"`
}

// NetEyeAlyvixSpec configures the Alyvix feature module of a tenant. While it
// is enabled, the tenant runs the RDP access to the Alyvix sessions: an Apache
// Guacamole web application and proxy daemon (guacd) in the tenant namespace,
// which the Gateway of the tenant exposes on the hostname of the tenant. The Guacamole
// JSON authentication key of the tenant is generated once, in a Secret of the
// tenant namespace.
type NetEyeAlyvixSpec struct {
	// Enabled enables the Alyvix feature module for the tenant.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// RDP configures the RDP access to the Alyvix sessions.
	// +kubebuilder:validation:Optional
	RDP *NetEyeAlyvixRDPSpec `json:"rdp,omitempty"`
}

// NetEyeAlyvixRDPSpec configures the RDP access to the Alyvix sessions of a
// tenant.
type NetEyeAlyvixRDPSpec struct {
	// Ports lists the TCP ports of the RDP service on the Alyvix nodes. The
	// proxy daemon may open connections to these ports on any address. The
	// default is 3389.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	// +listType=set
	Ports []int32 `json:"ports,omitempty"`
}

// DefaultAlyvixRDPPort is the TCP port of the RDP service on the Alyvix nodes
// when spec.alyvix.rdp.ports is unset.
const DefaultAlyvixRDPPort int32 = 3389

// AlyvixEnabled reports whether the Alyvix feature module is enabled for the
// tenant.
func (s *NetEyeTenantSpec) AlyvixEnabled() bool {
	return s != nil && s.Alyvix != nil && s.Alyvix.Enabled
}

// EffectiveRDPPorts returns the RDP ports, applying the default for an unset
// list.
func (s *NetEyeAlyvixSpec) EffectiveRDPPorts() []int32 {
	if s == nil || s.RDP == nil || len(s.RDP.Ports) == 0 {
		return []int32{DefaultAlyvixRDPPort}
	}
	return append([]int32(nil), s.RDP.Ports...)
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
