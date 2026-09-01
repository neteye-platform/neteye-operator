// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KeycloakProtocolMapper declares one protocol mapper attached to a Keycloak
// client. Mappers are matched by name: the operator creates missing ones and
// reconciles the configuration of existing ones.
type KeycloakProtocolMapper struct {
	// Name identifies the mapper inside the client.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="groups membership"
	Name string `json:"name"`

	// Protocol is the Keycloak protocol the mapper applies to.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=openid-connect;saml
	// +kubebuilder:default="openid-connect"
	Protocol string `json:"protocol,omitempty"`

	// ProtocolMapper is the Keycloak mapper implementation identifier.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="oidc-group-membership-mapper"
	ProtocolMapper string `json:"protocolMapper"`

	// Config holds the mapper configuration exactly as Keycloak expects it.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example={"claim.name":"groups","full.path":"true"}
	Config map[string]string `json:"config,omitempty"`
}

// KeycloakServiceAccountSpec configures the client service account.
type KeycloakServiceAccountSpec struct {
	// Enabled turns the client service account on, enabling the client
	// credentials grant.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
}

// KeycloakClientSpec declares the desired state of one OpenID Connect client in
// the identity service. The operator reconciles it continuously against the
// Keycloak Admin API, so changes made outside the cluster are reverted.
type KeycloakClientSpec struct {
	// Realm is the Keycloak realm owning the client. The realm must already
	// exist; the operator does not create realms.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default="master"
	Realm string `json:"realm,omitempty"`

	// ClientID is the Keycloak client identifier, unique within the realm.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="neteye"
	ClientID string `json:"clientId"`

	// Enabled enables the client in Keycloak.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Name is the human-readable client name shown in the admin console.
	// +kubebuilder:validation:Optional
	Name string `json:"name,omitempty"`

	// Description is the client description shown in the admin console.
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// RootURL is the base URL relative redirect URIs and web origins resolve against.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example="https://neteye.example.com"
	RootURL string `json:"rootUrl,omitempty"`

	// RedirectUris lists the valid redirect URIs. Entries may be relative to RootURL.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example={"/neteye/*"}
	RedirectUris []string `json:"redirectUris,omitempty"`

	// WebOrigins lists the allowed CORS origins.
	// +kubebuilder:validation:Optional
	WebOrigins []string `json:"webOrigins,omitempty"`

	// PublicClient marks the client as public, meaning it authenticates without
	// a client secret.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	PublicClient bool `json:"publicClient,omitempty"`

	// StandardFlow enables the OpenID Connect authorization code flow.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	StandardFlow *bool `json:"standardFlow,omitempty"`

	// DirectAccess enables the resource owner password credentials grant.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	DirectAccess bool `json:"directAccess,omitempty"`

	// ServiceAccount configures the client service account.
	// +kubebuilder:validation:Optional
	ServiceAccount *KeycloakServiceAccountSpec `json:"serviceAccount,omitempty"`

	// SecretRef references the Secret key holding the client secret, which must
	// exist in the same namespace as this resource. When omitted on a
	// confidential client the secret stays whatever Keycloak generated and the
	// operator never touches it, so no Pod can mount it from the cluster.
	// +kubebuilder:validation:Optional
	SecretRef *NetEyeSecretKeySelector `json:"secretRef,omitempty"`

	// ProtocolMappers lists the protocol mappers reconciled on the client.
	// +kubebuilder:validation:Optional
	ProtocolMappers []KeycloakProtocolMapper `json:"protocolMappers,omitempty"`

	// DefaultClientScopes lists the client scopes always applied to tokens.
	// +kubebuilder:validation:Optional
	DefaultClientScopes []string `json:"defaultClientScopes,omitempty"`

	// OptionalClientScopes lists the client scopes applied only when requested.
	// +kubebuilder:validation:Optional
	OptionalClientScopes []string `json:"optionalClientScopes,omitempty"`
}

// KeycloakClientStatus defines the observed state of a KeycloakClient.
type KeycloakClientStatus struct {
	// Status is the observed state of the Keycloak client.
	Status ServiceState `json:"status,omitempty"`

	// Message is a human-readable status message.
	Message string `json:"message,omitempty"`

	// ClientUUID is the internal Keycloak identifier of the reconciled client.
	ClientUUID string `json:"clientUUID,omitempty"`

	// ObservedGeneration is the generation of the most recently observed
	// KeycloakClient.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=keycloakclients,shortName=kcc
// +kubebuilder:printcolumn:name="ClientID",type=string,JSONPath=`.spec.clientId`
// +kubebuilder:printcolumn:name="Realm",type=string,JSONPath=`.spec.realm`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KeycloakClient is the Schema for the keycloakclients API. It describes one
// OpenID Connect client the operator keeps reconciled in the identity service.
type KeycloakClient struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeycloakClientSpec   `json:"spec,omitempty"`
	Status KeycloakClientStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KeycloakClientList contains a list of KeycloakClient.
type KeycloakClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakClient `json:"items"`
}
