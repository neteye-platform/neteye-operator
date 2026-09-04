// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KeycloakAuthFlowRequirement defines the requirement of a Keycloak
// authentication execution or subflow.
type KeycloakAuthFlowRequirement string

const (
	KeycloakAuthFlowRequirementRequired    KeycloakAuthFlowRequirement = "REQUIRED"
	KeycloakAuthFlowRequirementAlternative  KeycloakAuthFlowRequirement = "ALTERNATIVE"
	KeycloakAuthFlowRequirementDisabled     KeycloakAuthFlowRequirement = "DISABLED"
	KeycloakAuthFlowRequirementConditional  KeycloakAuthFlowRequirement = "CONDITIONAL"
)

// KeycloakAuthFlowProvider defines the provider type of a nested
// Keycloak authentication subflow.
type KeycloakAuthFlowProvider string

const (
	KeycloakAuthFlowProviderBasic KeycloakAuthFlowProvider = "basic-flow"
	KeycloakAuthFlowProviderForm  KeycloakAuthFlowProvider = "form-flow"
)

// KeycloakAuthFlowConfig defines the configuration of a Keycloak authenticator.
type KeycloakAuthFlowConfig struct {
	// Alias is the alias of the authenticator configuration.
	// +kubebuilder:validation:Optional
	Alias string `json:"alias,omitempty"`

	// Values contains the authenticator configuration values.
	// +kubebuilder:validation:Optional
	Values map[string]string `json:"values,omitempty"`
}

// KeycloakAuthFlowExecution defines a Keycloak authentication execution or
// nested subflow.
type KeycloakAuthFlowExecution struct {
	// Requirement defines how this execution or subflow is evaluated by Keycloak.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=REQUIRED;ALTERNATIVE;DISABLED;CONDITIONAL
	Requirement KeycloakAuthFlowRequirement `json:"requirement"`

	// Authenticator is the Keycloak authenticator provider ID.
	// +kubebuilder:validation:Optional
	Authenticator string `json:"authenticator,omitempty"`

	// Alias is the alias of the execution.
	// +kubebuilder:validation:Optional
	Alias string `json:"alias,omitempty"`

	// Config contains the authenticator configuration.
	// +kubebuilder:validation:Optional
	Config *KeycloakAuthFlowConfig `json:"config,omitempty"`

	// Flow defines a nested authentication subflow.
	// +kubebuilder:validation:Optional
	Flow *KeycloakAuthFlowSubflow `json:"flow,omitempty"`
}

// KeycloakAuthFlowSubflow defines a nested Keycloak authentication flow.
type KeycloakAuthFlowSubflow struct {
	// Alias is the alias of the nested authentication flow.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`

	// Provider is the provider type of the nested authentication flow.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=basic-flow;form-flow
	// +kubebuilder:default=basic-flow
	Provider KeycloakAuthFlowProvider `json:"provider,omitempty"`

	// Executions contains the executions and nested subflows belonging to this flow.
	// +kubebuilder:validation:Required
	Executions []KeycloakAuthFlowExecution `json:"executions"`
}

// KeycloakAuthFlowSpec defines the desired Keycloak authentication flow.
type KeycloakAuthFlowSpec struct {
	// Alias is the alias of the authentication flow.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`

	// Realm is the Keycloak realm where the flow is created.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Realm string `json:"realm"`

	// Executions contains the executions and nested subflows belonging to the flow.
	// +kubebuilder:validation:Required
	Executions []KeycloakAuthFlowExecution `json:"executions"`
}

// KeycloakAuthFlowStatus defines the observed state of KeycloakAuthFlow.
type KeycloakAuthFlowStatus struct {
	// ID is the Keycloak internal ID of the authentication flow.
	// +kubebuilder:validation:Optional
	ID string `json:"id,omitempty"`

	// Value is the reconciliation status.
	// +kubebuilder:validation:Optional
	Value string `json:"value,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=keycloakauthflows,shortName=kcflow
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.value`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KeycloakAuthFlow is the Schema for the keycloakauthflows API.
type KeycloakAuthFlow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeycloakAuthFlowSpec   `json:"spec,omitempty"`
	Status KeycloakAuthFlowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KeycloakAuthFlowList contains a list of KeycloakAuthFlow.
type KeycloakAuthFlowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakAuthFlow `json:"items"`
}