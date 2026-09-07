// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KeycloakAuthFlowConfig declares the configuration of an authenticator execution.
type KeycloakAuthFlowConfig struct {
	// Alias is the human-readable configuration name in Keycloak.
	// +kubebuilder:validation:Optional
	Alias string `json:"alias,omitempty"`
	// Values contains the authenticator configuration entries.
	// +kubebuilder:validation:Optional
	Values map[string]string `json:"values,omitempty"`
}

// KeycloakAuthFlowExecution declares an execution in an authentication flow.
// It is either an Authenticator execution or a nested Flow.
//
// Flow nesting is capped at 3 fixed levels (KeycloakAuthFlowExecution ->
// KeycloakAuthFlowExecutionL2 -> KeycloakAuthFlowExecutionL3) rather than
// modeled as a self-referential type: controller-gen cannot generate a valid
// CRD OpenAPI schema for a recursive Go type, since it truncates the schema
// at a fixed depth and emits an empty (typeless) items schema beyond it,
// which the API server rejects as a structural schema violation.
type KeycloakAuthFlowExecution struct {
	// Alias identifies an authenticator execution where Keycloak exposes an alias.
	// +kubebuilder:validation:Optional
	Alias string `json:"alias,omitempty"`
	// Requirement is the Keycloak execution requirement, such as REQUIRED or ALTERNATIVE.
	// +kubebuilder:validation:Optional
	Requirement string `json:"requirement,omitempty"`
	// Authenticator is the Keycloak authenticator provider identifier.
	// +kubebuilder:validation:Optional
	Authenticator string `json:"authenticator,omitempty"`
	// Config configures this authenticator.
	// +kubebuilder:validation:Optional
	Config *KeycloakAuthFlowConfig `json:"config,omitempty"`
	// Flow declares a nested subflow in place of an authenticator.
	// +kubebuilder:validation:Optional
	Flow *KeycloakAuthFlowExecutionSpecL2 `json:"flow,omitempty"`
}

// KeycloakAuthFlowExecutionSpecL2 declares a Keycloak authentication flow
// nested one level below the root flow. See KeycloakAuthFlowExecution for why
// nesting is capped at a fixed depth instead of being self-referential.
type KeycloakAuthFlowExecutionSpecL2 struct {
	// Alias is the Keycloak flow alias, unique within the realm.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`
	// Provider is the Keycloak flow provider identifier.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="basic-flow"
	Provider string `json:"provider,omitempty"`
	// Executions is the ordered, authoritative list of direct child executions.
	// +kubebuilder:validation:Optional
	Executions []KeycloakAuthFlowExecutionL2 `json:"executions,omitempty"`
}

// KeycloakAuthFlowExecutionL2 is KeycloakAuthFlowExecution one level deeper;
// its own nested Flow is the last allowed level (KeycloakAuthFlowExecutionL3).
type KeycloakAuthFlowExecutionL2 struct {
	// Alias identifies an authenticator execution where Keycloak exposes an alias.
	// +kubebuilder:validation:Optional
	Alias string `json:"alias,omitempty"`
	// Requirement is the Keycloak execution requirement, such as REQUIRED or ALTERNATIVE.
	// +kubebuilder:validation:Optional
	Requirement string `json:"requirement,omitempty"`
	// Authenticator is the Keycloak authenticator provider identifier.
	// +kubebuilder:validation:Optional
	Authenticator string `json:"authenticator,omitempty"`
	// Config configures this authenticator.
	// +kubebuilder:validation:Optional
	Config *KeycloakAuthFlowConfig `json:"config,omitempty"`
	// Flow declares a nested subflow in place of an authenticator.
	// +kubebuilder:validation:Optional
	Flow *KeycloakAuthFlowExecutionSpecL3 `json:"flow,omitempty"`
}

// KeycloakAuthFlowExecutionSpecL3 declares a Keycloak authentication flow
// nested two levels below the root flow, the deepest level allowed.
type KeycloakAuthFlowExecutionSpecL3 struct {
	// Alias is the Keycloak flow alias, unique within the realm.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`
	// Provider is the Keycloak flow provider identifier.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="basic-flow"
	Provider string `json:"provider,omitempty"`
	// Executions is the ordered, authoritative list of direct child executions.
	// These are leaf executions: a flow at this depth cannot nest a further
	// subflow.
	// +kubebuilder:validation:Optional
	Executions []KeycloakAuthFlowExecutionL3 `json:"executions,omitempty"`
}

// KeycloakAuthFlowExecutionL3 is a leaf execution at the deepest allowed
// nesting level: it can only be an Authenticator, never a further subflow.
type KeycloakAuthFlowExecutionL3 struct {
	// Alias identifies an authenticator execution where Keycloak exposes an alias.
	// +kubebuilder:validation:Optional
	Alias string `json:"alias,omitempty"`
	// Requirement is the Keycloak execution requirement, such as REQUIRED or ALTERNATIVE.
	// +kubebuilder:validation:Optional
	Requirement string `json:"requirement,omitempty"`
	// Authenticator is the Keycloak authenticator provider identifier.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Authenticator string `json:"authenticator"`
	// Config configures this authenticator.
	// +kubebuilder:validation:Optional
	Config *KeycloakAuthFlowConfig `json:"config,omitempty"`
}

// KeycloakAuthFlowSpec declares an authentication flow and its complete execution tree.
type KeycloakAuthFlowSpec struct {
	// Realm owns the flow and must already exist.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default="master"
	Realm string `json:"realm,omitempty"`
	// Alias is the Keycloak flow alias, unique within the realm.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Alias string `json:"alias"`
	// Provider is the Keycloak root-flow provider identifier.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="basic-flow"
	Provider string `json:"provider,omitempty"`
	// Executions is the ordered, authoritative list of direct child executions.
	// +kubebuilder:validation:Optional
	Executions []KeycloakAuthFlowExecution `json:"executions,omitempty"`
	// DeletionPolicy decides whether deleting this resource removes the remote flow.
	// +kubebuilder:validation:Optional
	DeletionPolicy KeycloakDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// KeycloakAuthFlowStatus defines the observed state of a KeycloakAuthFlow.
type KeycloakAuthFlowStatus struct {
	Status             ServiceState `json:"status,omitempty"`
	Message            string       `json:"message,omitempty"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=keycloakauthflows,shortName=kcaf
// +kubebuilder:printcolumn:name="Alias",type=string,JSONPath=`.spec.alias`
// +kubebuilder:printcolumn:name="Realm",type=string,JSONPath=`.spec.realm`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type KeycloakAuthFlow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KeycloakAuthFlowSpec   `json:"spec,omitempty"`
	Status            KeycloakAuthFlowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KeycloakAuthFlowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakAuthFlow `json:"items"`
}
