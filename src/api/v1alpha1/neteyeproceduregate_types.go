// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ProcedureGateProducer identifies who completed the step a gate records.
// +kubebuilder:validation:Enum=Ansible;Operator
type ProcedureGateProducer string

const (
	ProcedureGateProducerAnsible  ProcedureGateProducer = "Ansible"
	ProcedureGateProducerOperator ProcedureGateProducer = "Operator"
)

// NetEyeProcedureGateSpec records one completed install or upgrade step.
type NetEyeProcedureGateSpec struct {
	// Version is the NetEye release being installed or upgraded to, e.g. "4.52".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+$`
	// +kubebuilder:example="4.52"
	Version string `json:"version"`

	// Gate is the step key agreed between the operator and the NetEye Ansible
	// procedure.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:example="keycloak-db-configured"
	Gate string `json:"gate"`

	// Producer is who completed the step. It is informational: a gate
	// satisfies its consumers whoever created it, so an administrator can
	// create one by hand to unblock an install or upgrade.
	// +kubebuilder:validation:Required
	Producer ProcedureGateProducer `json:"producer"`

	// Message is an optional human-readable note about the step.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=neteyeproceduregates,shortName=nepg
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Gate",type=string,JSONPath=`.spec.gate`
// +kubebuilder:printcolumn:name="Producer",type=string,JSONPath=`.spec.producer`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NetEyeProcedureGate records that a step of a NetEye install or upgrade has
// been completed. Its existence is the signal: the operator and the NetEye
// Ansible procedure create gates for each other in the namespace of the NetEye
// resource and wait for the gates they require.
type NetEyeProcedureGate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
	Spec NetEyeProcedureGateSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// NetEyeProcedureGateList contains a list of NetEyeProcedureGate.
type NetEyeProcedureGateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetEyeProcedureGate `json:"items"`
}
