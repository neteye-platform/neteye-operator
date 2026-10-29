// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// TenantNamePrefix starts the name of every tenant. The namespace and the
	// NetEyeTenant of a tenant share one name, TenantNamePrefix followed by a
	// random id.
	TenantNamePrefix = "neteye-tenant-"

	// TenantLabel marks the namespace of a tenant. Its value is the name of the
	// tenant, which is also the name of the namespace. Scripts select the tenant
	// namespaces with this label rather than with the name prefix, because the
	// shared namespace starts with the same prefix.
	TenantLabel = "neteye.cloud/tenant"

	// TenantDisplayNameAnnotation shows the spec.name of a tenant on its
	// namespace. The operator owns it: an admission policy refuses changes made
	// by anyone else. An annotation is used instead of a label because spec.name
	// is free text, which a label value cannot hold.
	TenantDisplayNameAnnotation = "neteye.cloud/tenant-display-name"
)

// NetEyeTenantSpec defines the desired state of a NetEye tenant.
type NetEyeTenantSpec struct {
	// Name is the name of the tenant that people see and use. It can change at
	// any time; resources derived from the tenant use metadata.name, which never
	// changes.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
}

// Condition types reported in NetEyeTenantStatus.Conditions.
const (
	// TenantConditionReady reports readiness of the tenant as a whole.
	// Consumers must also compare its observedGeneration with
	// metadata.generation before treating it as current.
	TenantConditionReady = "Ready"
)

// Stable, machine-readable reasons used in the tenant status. Messages are
// concise explanations for humans and are not a machine-readable API.
const (
	TenantReasonAllComponentsReady = "AllComponentsReady"
	TenantReasonComponentsNotReady = "ComponentsNotReady"
	TenantReasonComponentsDegraded = "ComponentsDegraded"
)

// TenantComponentState is the readiness state of one tenant component.
type TenantComponentState string

const (
	// TenantComponentStateReady means the component reached its desired state.
	TenantComponentStateReady TenantComponentState = "Ready"
	// TenantComponentStateProgressing means the component is converging.
	TenantComponentStateProgressing TenantComponentState = "Progressing"
	// TenantComponentStateDegraded means the component's own reconciliation
	// failed.
	TenantComponentStateDegraded TenantComponentState = "Degraded"
	// TenantComponentStateBlocked means a prerequisite outside the tenant, such
	// as the shared identity service, is not ready.
	TenantComponentStateBlocked TenantComponentState = "Blocked"
)

// TenantResolvedImage is one container image selected by the operator for a
// tenant component.
type TenantResolvedImage struct {
	// Name is the stable logical image name within its component.
	Name string `json:"name"`

	// Image is the exact, digest-pinned image reference.
	Image string `json:"image"`
}

// TenantComponentStatus reports the observed state of one tenant component.
type TenantComponentStatus struct {
	// Status is the component's readiness state.
	Status TenantComponentState `json:"status,omitempty"`

	// ObservedGeneration is the NetEyeTenant generation this entry describes.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Reason is a stable, machine-readable identifier for the current state.
	Reason string `json:"reason,omitempty"`

	// Message is a concise explanation for humans and is not a
	// machine-readable API.
	Message string `json:"message,omitempty"`

	// ResolvedImages is the complete set of container images the operator
	// selected for this component. A component with no container images reports
	// an empty list.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	ResolvedImages []TenantResolvedImage `json:"resolvedImages,omitempty"`
}

// NetEyeTenantStatus defines the observed state of a NetEye tenant.
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
	Components map[string]TenantComponentStatus `json:"components,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=neteyetenants,shortName=nt
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NetEyeTenant is the Schema for the neteyetenants API. It represents one
// NetEye tenant and its dedicated resources. It lives in the namespace of the
// tenant and has the same name as that namespace.
type NetEyeTenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetEyeTenantSpec   `json:"spec,omitempty"`
	Status NetEyeTenantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetEyeTenantList contains a list of NetEyeTenant.
type NetEyeTenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetEyeTenant `json:"items"`
}
