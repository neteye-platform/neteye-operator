/*
Copyright (c) 2026 Würth IT Italy S.r.l.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetEyeSpec defines the desired state of NetEye.
type NetEyeSpec struct {
	// NetEyeVersion is the NetEye product version to deploy, e.g. "4.36".
	// It resolves the component images of every managed service: the admin
	// declares a product version, never an image tag.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="4.36"
	NetEyeVersion string `json:"neteyeVersion"`

	// TargetNamespace is the namespace the managed services are deployed
	// into. It may differ from the namespace the NetEye CR itself lives in.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default="neteye-system"
	// +kubebuilder:example="neteye-system"
	TargetNamespace string `json:"targetNamespace"`

	// Services selects which managed services this NetEye instance is made of
	// and carries their per-service tunables. A service left unset is not
	// deployed; a service that was set and is then removed is torn down.
	// +kubebuilder:validation:Optional
	Services NetEyeServices `json:"services,omitempty"`
}

// NetEyeServices is the set of services a NetEye instance can be composed of.
// Each field is a template for the corresponding managed-service CR, which the
// NetEye controller creates and keeps in sync — an admin edits NetEye, not the
// service CRs, but can still inspect each service's own status independently.
//
// Adding a service means adding one field here plus its own CRD, deliberately:
// a service that can fail on its own gets a status an admin can read on its
// own (ADR-0001, reopened).
type NetEyeServices struct {
	// Keycloak deploys the NetEye identity provider.
	// +kubebuilder:validation:Optional
	Keycloak *KeycloakTemplate `json:"keycloak,omitempty"`
}

// KeycloakTemplate is the subset of KeycloakSpec an admin sets from NetEye.
// The rest of KeycloakSpec — images above all — is derived by the operator
// from spec.neteyeVersion and must not be settable here.
type KeycloakTemplate struct {
	// Hostname the Keycloak instance is served on. Defaults to a name derived
	// from the target namespace when empty.
	// +kubebuilder:validation:Optional
	Hostname string `json:"hostname,omitempty"`

	// IssuerRef selects the cert-manager issuer signing the instance's
	// certificate. Defaults to a self-signed Issuer the operator provisions.
	// +kubebuilder:validation:Optional
	IssuerRef *IssuerReference `json:"issuerRef,omitempty"`

	// AdditionalOptions overrides the operator's compiled-in defaults for
	// individual enforced Keycloak settings.
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	AdditionalOptions []ServiceOption `json:"additionalOptions,omitempty"`
}

// NetEyeStatus defines the observed state of NetEye.
type NetEyeStatus struct {
	// Phase is the rolled-up lifecycle state across every managed service:
	// the least-advanced service's phase, so that a NetEye reads Ready only
	// when all of it is.
	Phase Phase `json:"phase,omitempty"`

	// Message is a human-readable elaboration of Phase.
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the generation of the most recently reconciled
	// NetEye spec.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Services caches the state of each managed service CR this NetEye owns.
	// +listType=map
	// +listMapKey=kind
	Services []ServiceReference `json:"services,omitempty"`

	// Conditions report aspects that can degrade independently of the deploy.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ne
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.neteyeVersion`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetNamespace`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NetEye is the Schema for the neteyes API. It is the single object an admin
// writes: it declares which NetEye product version to run, where, and which
// managed services it is composed of. The operator fans it out into one CR per
// managed service.
type NetEye struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetEyeSpec   `json:"spec,omitempty"`
	Status NetEyeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetEyeList contains a list of NetEye.
type NetEyeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetEye `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetEye{}, &NetEyeList{})
}
