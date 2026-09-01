// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KeycloakUserDeletionPolicy decides what happens to the Keycloak account when
// the resource declaring it is deleted.
// +kubebuilder:validation:Enum=Orphan;Delete
type KeycloakUserDeletionPolicy string

const (
	// KeycloakUserDeletionPolicyOrphan leaves the account in Keycloak.
	KeycloakUserDeletionPolicyOrphan KeycloakUserDeletionPolicy = "Orphan"

	// KeycloakUserDeletionPolicyDelete removes the account from Keycloak.
	KeycloakUserDeletionPolicyDelete KeycloakUserDeletionPolicy = "Delete"
)

// KeycloakUserCredentialSpec declares how the account password is managed. The
// credential is create-only: it is written when the account is created and is
// only rewritten when RotationToken changes, so a password owned by a person is
// never silently overwritten.
type KeycloakUserCredentialSpec struct {
	// SecretRef references the Secret key holding the password. The Secret lives
	// in the same namespace as this resource.
	// +kubebuilder:validation:Required
	SecretRef NetEyeSecretKeySelector `json:"secretRef"`

	// Generate makes the operator generate a random password and store it in the
	// Secret referenced by SecretRef, which it creates if missing. When false the
	// password is read from that Secret instead, which must already exist.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	Generate bool `json:"generate,omitempty"`

	// Temporary forces the user to choose a new password at the next login. Use
	// it for accounts a person will own, so the operator does not stay the
	// keeper of a human credential.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	Temporary bool `json:"temporary,omitempty"`

	// RotationToken requests a password rotation. Any change to its value makes
	// the operator reset the password once; leaving it untouched keeps the
	// credential create-only. The value itself is opaque and is only compared
	// against status.credentialRotation.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example="2026-08-31"
	RotationToken string `json:"rotationToken,omitempty"`
}

// KeycloakUserSpec declares one Keycloak account the platform owns, such as the
// administrative user the operator authenticates as. It is not meant for end
// users: only the fields declared here are reconciled, so anything a person
// changes on their own account outside this spec survives untouched.
type KeycloakUserSpec struct {
	// Realm is the Keycloak realm owning the account. The realm must already
	// exist; the operator does not create realms.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default="master"
	Realm string `json:"realm,omitempty"`

	// Username is the Keycloak username, unique within the realm.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="neteye-internal-keycloak-admin"
	Username string `json:"username"`

	// Enabled enables the account in Keycloak.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Email is the account email address. It is reconciled only when set.
	// +kubebuilder:validation:Optional
	Email string `json:"email,omitempty"`

	// EmailVerified marks the email address as already verified.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	EmailVerified bool `json:"emailVerified,omitempty"`

	// FirstName is the given name shown in the admin console. It is reconciled
	// only when set.
	// +kubebuilder:validation:Optional
	FirstName string `json:"firstName,omitempty"`

	// LastName is the family name shown in the admin console. It is reconciled
	// only when set.
	// +kubebuilder:validation:Optional
	LastName string `json:"lastName,omitempty"`

	// RealmRoles lists the realm roles assigned to the account. A nil list means
	// the role mappings are not managed here and are left alone; a declared list
	// is authoritative and roles outside it are unassigned. The roles must
	// already exist in the realm.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example={"admin"}
	RealmRoles []string `json:"realmRoles,omitempty"`

	// Groups lists the group paths the account belongs to, following the same
	// nil-versus-empty contract as RealmRoles. The groups must already exist.
	// +kubebuilder:validation:Optional
	// +kubebuilder:example={"/neteye-admins"}
	Groups []string `json:"groups,omitempty"`

	// Credential declares the account password. When omitted the operator never
	// touches credentials, which is what adopting an existing account needs.
	// +kubebuilder:validation:Optional
	Credential *KeycloakUserCredentialSpec `json:"credential,omitempty"`

	// DeletionPolicy decides what happens to the Keycloak account when this
	// resource is deleted.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="Orphan"
	DeletionPolicy KeycloakUserDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// KeycloakUserStatus defines the observed state of a KeycloakUser.
type KeycloakUserStatus struct {
	// Status is the observed state of the Keycloak account.
	Status ServiceState `json:"status,omitempty"`

	// Message is a human-readable status message.
	Message string `json:"message,omitempty"`

	// UserID is the internal Keycloak identifier of the reconciled account.
	UserID string `json:"userID,omitempty"`

	// Adopted reports that the account already existed when the operator first
	// reconciled it, so its credential was left as it was found.
	Adopted bool `json:"adopted,omitempty"`

	// CredentialRotation echoes the spec.credential.rotationToken the operator
	// last acted on.
	CredentialRotation string `json:"credentialRotation,omitempty"`

	// ObservedGeneration is the generation of the most recently observed
	// KeycloakUser.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=keycloakusers,shortName=kcu
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=`.spec.username`
// +kubebuilder:printcolumn:name="Realm",type=string,JSONPath=`.spec.realm`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KeycloakUser is the Schema for the keycloakusers API. It describes one
// Keycloak account the platform owns and keeps reconciled in the identity
// service.
type KeycloakUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeycloakUserSpec   `json:"spec,omitempty"`
	Status KeycloakUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KeycloakUserList contains a list of KeycloakUser.
type KeycloakUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KeycloakUser `json:"items"`
}
