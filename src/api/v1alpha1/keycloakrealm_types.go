// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KeycloakRealmSpec declares the desired state of one Keycloak realm.
type KeycloakRealmSpec struct {
	// Realm is the Keycloak realm name, which is immutable after creation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Realm string `json:"realm"`

	// DisplayName is the human-readable realm name shown in the Keycloak admin console.
	// +kubebuilder:validation:Optional
	DisplayName string `json:"displayName,omitempty"`

	// Enabled enables the realm in Keycloak.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// DeletionPolicy decides whether deleting this resource removes the remote realm.
	// +kubebuilder:validation:Optional
	DeletionPolicy KeycloakDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// KeycloakRealmStatus defines the observed state of a KeycloakRealm.
type KeycloakRealmStatus struct {
	Status             ServiceState `json:"status,omitempty"`
	Message            string       `json:"message,omitempty"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=keycloakrealms,shortName=kcr
// +kubebuilder:printcolumn:name="Realm",type=string,JSONPath=`.spec.realm`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type KeycloakRealm struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KeycloakRealmSpec   `json:"spec,omitempty"`
	Status            KeycloakRealmStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KeycloakRealmList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakRealm `json:"items"`
}
