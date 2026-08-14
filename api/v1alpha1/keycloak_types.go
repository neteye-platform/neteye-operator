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

// KeycloakSpec defines the desired state of Keycloak.
//
// It is written by the NetEye controller, not by an admin: the image fields in
// particular are derived from NetEye.spec.neteyeVersion, and hand-editing them
// is reverted on the next reconcile. Admins express intent on the owning
// NetEye CR; this CR exists so each service has a status of its own.
type KeycloakSpec struct {
	// Image is the Keycloak container image, resolved from the owning NetEye's
	// product version.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// ConfigImage is the ansible-runner image running the one-shot
	// configuration Job (realm, clients, themes, auth flows).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ConfigImage string `json:"configImage"`

	// Hostname the instance is served on, and the name its certificate is
	// issued for.
	// +kubebuilder:validation:Optional
	Hostname string `json:"hostname,omitempty"`

	// IssuerRef selects the cert-manager issuer that signs the instance's
	// certificate. When empty the operator provisions a self-signed Issuer of
	// its own, which is enough for an instance only this operator talks to;
	// point it at a real CA to make the certificate trusted by anything else.
	// +kubebuilder:validation:Optional
	IssuerRef *IssuerReference `json:"issuerRef,omitempty"`

	// AdditionalOptions overrides the operator's compiled-in defaults for
	// individual enforced settings, e.g. name "loginTheme".
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	AdditionalOptions []ServiceOption `json:"additionalOptions,omitempty"`
}

// IssuerReference selects a cert-manager issuer.
type IssuerReference struct {
	// Name of the issuer.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind of the issuer. A namespaced Issuer must live in the same namespace
	// as the instance; a ClusterIssuer is cluster-wide.
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	// +kubebuilder:default=Issuer
	Kind string `json:"kind,omitempty"`
}

// KeycloakStatus defines the observed state of Keycloak.
type KeycloakStatus struct {
	// Phase is the lifecycle state of this Keycloak instance.
	Phase Phase `json:"phase,omitempty"`

	// Message is a human-readable elaboration of Phase.
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the generation of the most recently reconciled
	// spec.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// BootstrapConfigHash fingerprints the inputs of the configuration Job
	// that last succeeded. It is what makes bootstrap a once-per-input event
	// rather than a once-per-Job one: the Job is garbage-collected an hour
	// after it finishes, and without this the instance would fall back to
	// Bootstrapping — and stop enforcing settings — every time that happened.
	BootstrapConfigHash string `json:"bootstrapConfigHash,omitempty"`

	// Endpoint is the in-cluster base URL of the instance, including the
	// relative path baked into the NetEye Keycloak image.
	Endpoint string `json:"endpoint,omitempty"`

	// Conditions report aspects that can degrade independently of the deploy,
	// notably whether enforced settings are still being re-asserted. A failing
	// condition deliberately does not move Phase out of Ready: a setting that
	// failed to re-assert is a degraded instance, not a failed deploy.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nekc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Keycloak is the Schema for the keycloaks API: one NetEye-managed Keycloak
// instance, owned by a NetEye CR.
//
// The kind name is shared with the upstream Keycloak Operator's
// k8s.keycloak.org/Keycloak, which this operator drives underneath. They are
// different groups and never conflict on the wire, but `kubectl get keycloak`
// is ambiguous on a cluster running both — use `keycloaks.neteye.com`, or the
// `nekc` short name, for this one.
type Keycloak struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeycloakSpec   `json:"spec,omitempty"`
	Status KeycloakStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KeycloakList contains a list of Keycloak.
type KeycloakList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Keycloak `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Keycloak{}, &KeycloakList{})
}
