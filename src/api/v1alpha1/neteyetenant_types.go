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
	// which has the same prefix. Accepting more names later is an additive
	// change; accepting fewer is not.
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
	// DisplayName is the name of the tenant that people see and use. It can
	// change at any time; resources derived from the tenant use metadata.name,
	// which never changes.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == self.trim()",message="must not contain surrounding whitespace"
	// +kubebuilder:validation:XValidation:rule="!self.matches('[\\\\p{Cc}\\\\p{Cf}]')",message="must not contain control or format characters"
	DisplayName string `json:"displayName"`
}

// NetEyeTenantStatus defines the observed state of a NetEye tenant, following
// the status contract of ADR-0002.
type NetEyeTenantStatus struct {
	// ObservedGeneration is the generation of the most recently observed
	// NetEyeTenant.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is the stable machine-readable status interface. It uses the
	// Kubernetes standard condition schema. Callers must tolerate condition
	// types they do not recognize.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Components reports observed state keyed by stable logical component
	// name. Adding a key is backward compatible, and callers must tolerate
	// component keys they do not recognize.
	// +kubebuilder:validation:Optional
	Components map[string]NetEyeComponentStatus `json:"components,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=neteyetenants,shortName=netenant
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^neteye-tenant-[a-hjkmnp-z2-9]{8}$')",message="metadata.name must be neteye-tenant- followed by an id of 8 characters from abcdefghjkmnpqrstuvwxyz23456789"
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
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
