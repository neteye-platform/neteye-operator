/*
Copyright 2024 Wuerth Phoenix.

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

// NetEyeComponents holds resolved image references for a given NetEye version.
type NetEyeComponents struct {
	// Full image reference for the Keycloak container, e.g. quay.io/keycloak/keycloak:27.0.0
	KeycloakImage string
}

// netEyeVersionMap maps a NetEye version string to its component image set.
// Add new entries here when a NetEye release ships a new Keycloak (or other)
// image version.
var netEyeVersionMap = map[string]NetEyeComponents{
	"4.36": {KeycloakImage: "quay.io/keycloak/keycloak:27.0.0"},
	"4.37": {KeycloakImage: "quay.io/keycloak/keycloak:27.1.0"},
}

// ComponentsForVersion returns the component image set for the given NetEye
// version. If the version is not found in the map the second return value is
// false.
func ComponentsForVersion(neteyeVersion string) (NetEyeComponents, bool) {
	c, ok := netEyeVersionMap[neteyeVersion]
	return c, ok
}

// NetEyeSpec defines the desired state of NetEyeConfig.
type NetEyeSpec struct {
	// NetEyeVersion is the NetEye product version string, e.g. "4.36".
	// It is used to resolve the correct component images (Keycloak, etc.).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="4.36"
	NetEyeVersion string `json:"neteyeVersion"`

	// TargetNamespace is the Kubernetes namespace where NetEye components
	// (Keycloak, …) will be deployed.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="neteye-system"
	TargetNamespace string `json:"targetNamespace"`
}

// NetEyeStatus defines the observed state of NetEyeConfig.
type NetEyeStatus struct {
	// Phase of the NetEyeConfig: Pending, Ready, Failed.
	Phase string `json:"phase,omitempty"`

	// ResolvedKeycloakImage is the Keycloak container image resolved from
	// the declared NetEyeVersion.
	ResolvedKeycloakImage string `json:"resolvedKeycloakImage,omitempty"`

	// Message is a human-readable status message.
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the generation of the most recently observed
	// NetEye.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ne;plural=neteyes
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.neteyeVersion`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.targetNamespace`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NetEye is the Schema for the neteyes API.
// It declares which NetEye product version is being deployed and in which
// Kubernetes namespace, driving the selection of component images (e.g. Keycloak 27).
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
