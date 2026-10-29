// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

// The status contract of ADR-0002, shared by every resource that reports
// components: a Ready condition, Progressing and Degraded conditions, and a
// map of component states. The names match the NetEye status contract, so the
// NetEye and the NetEyeTenant status use one set of types.

// Condition types reported in the status of a resource with components. Ready
// is the stable readiness interface for scripts and users; consumers must also
// compare its observedGeneration with metadata.generation before treating it
// as current.
const (
	// ConditionReady reports readiness of the resource as a whole.
	ConditionReady = "Ready"
	// ConditionProgressing reports that at least one component is converging.
	// It can be true at the same time as Degraded, when an independent
	// component can still make progress.
	ConditionProgressing = "Progressing"
	// ConditionDegraded reports that at least one component has an observed
	// failure. A component merely waiting for a healthy prerequisite does not
	// make the resource degraded.
	ConditionDegraded = "Degraded"
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
	ReasonDependencyNotReady = "DependencyNotReady"
)

// ComponentState is the readiness state reported for one logical component.
// +kubebuilder:validation:Enum=Ready;Progressing;Degraded;Blocked;Disabled
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
	// state. A disabled component does not keep the resource from becoming
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

	// ObservedGeneration is the generation of the resource this entry
	// describes.
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
	// reports an empty list, so the field is always present. While the
	// component is progressing this is the target set; once it is ready, the
	// operator has confirmed its required workloads use that set.
	// +listType=map
	// +listMapKey=name
	ResolvedImages []NetEyeResolvedImage `json:"resolvedImages"`
}
