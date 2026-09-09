// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// KeycloakRealmEvents configures the realm's login and admin action event
// logging. It is always enforced by the operator: see ADR-0004.
type KeycloakRealmEvents struct {
	// EventsEnabled enables login event logging.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	EventsEnabled *bool `json:"eventsEnabled,omitempty"`

	// AdminEventsEnabled enables admin action event logging.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	AdminEventsEnabled *bool `json:"adminEventsEnabled,omitempty"`

	// AdminEventsDetailsEnabled includes the representation of the affected
	// resource in admin events.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	AdminEventsDetailsEnabled *bool `json:"adminEventsDetailsEnabled,omitempty"`

	// EventsExpiration is how long login events are retained, in seconds.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=15552000
	EventsExpiration *int64 `json:"eventsExpiration,omitempty"`

	// AdminEventsExpiration is how long admin events are retained, in seconds.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=15552000
	AdminEventsExpiration *int64 `json:"adminEventsExpiration,omitempty"`
}

// KeycloakRealmBruteForceProtection configures the realm's account-lockout
// defense against repeated failed logins. It is always enforced by the
// operator: see ADR-0004. Permanent lockout is never enabled and is not
// exposed as a field.
type KeycloakRealmBruteForceProtection struct {
	// BruteForceProtected enables brute force detection.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	BruteForceProtected *bool `json:"bruteForceProtected,omitempty"`

	// MaxDeltaTimeSeconds is the failure reset time.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=43200
	MaxDeltaTimeSeconds *int64 `json:"maxDeltaTimeSeconds,omitempty"`

	// MaxFailureWaitSeconds is the maximum wait time after repeated failures.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=900
	MaxFailureWaitSeconds *int64 `json:"maxFailureWaitSeconds,omitempty"`

	// MinimumQuickLoginWaitSeconds is the wait time after a quick login
	// failure.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=60
	MinimumQuickLoginWaitSeconds *int64 `json:"minimumQuickLoginWaitSeconds,omitempty"`

	// QuickLoginCheckMilliSeconds is the threshold below which two login
	// attempts count as a quick login.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1000
	QuickLoginCheckMilliSeconds *int64 `json:"quickLoginCheckMilliSeconds,omitempty"`

	// FailureFactor is the number of failures before a lockout is triggered.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=30
	FailureFactor *int64 `json:"failureFactor,omitempty"`

	// WaitIncrementSeconds is how much the wait time grows after each
	// lockout.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=60
	WaitIncrementSeconds *int64 `json:"waitIncrementSeconds,omitempty"`
}

// KeycloakRealmTheme configures the Keycloak themes applied to the realm's
// login, admin console, account console, and emails. Unlike Events and
// BruteForceProtection, this is optional and opt-in: theme names are
// installation-specific branding, not a Keycloak-side default worth
// enforcing. When nil, the operator leaves the realm's themes untouched.
type KeycloakRealmTheme struct {
	// LoginTheme is the theme applied to the login pages.
	// +kubebuilder:validation:Optional
	LoginTheme string `json:"loginTheme,omitempty"`

	// AdminTheme is the theme applied to the admin console.
	// +kubebuilder:validation:Optional
	AdminTheme string `json:"adminTheme,omitempty"`

	// AccountTheme is the theme applied to the account console.
	// +kubebuilder:validation:Optional
	AccountTheme string `json:"accountTheme,omitempty"`

	// EmailTheme is the theme applied to outgoing emails.
	// +kubebuilder:validation:Optional
	EmailTheme string `json:"emailTheme,omitempty"`
}

// KeycloakRealmSpec declares the desired state of one Keycloak realm.
type KeycloakRealmSpec struct {
	// Realm is the Keycloak realm name, which is immutable after creation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="realm is immutable"
	Realm string `json:"realm"`

	// DisplayName is the human-readable realm name shown in the Keycloak admin console.
	// +kubebuilder:validation:Optional
	DisplayName string `json:"displayName,omitempty"`

	// DisplayNameHTML is the HTML variant of DisplayName shown on realm-themed pages.
	// +kubebuilder:validation:Optional
	DisplayNameHTML string `json:"displayNameHtml,omitempty"`

	// Enabled enables the realm in Keycloak.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// RememberMe enables the "remember me" option on the login form.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	RememberMe *bool `json:"rememberMe,omitempty"`

	// Events configures login and admin action event logging. Always
	// enforced by the operator; see ADR-0004.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	Events KeycloakRealmEvents `json:"events,omitempty"`

	// BruteForceProtection configures account-lockout defense against
	// repeated failed logins. Always enforced by the operator; see ADR-0004.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	BruteForceProtection KeycloakRealmBruteForceProtection `json:"bruteForceProtection,omitempty"`

	// Theme configures the realm's login, admin console, account console, and
	// email themes. Optional: when omitted, the operator does not manage
	// themes and leaves whatever is already set in Keycloak.
	// +kubebuilder:validation:Optional
	Theme *KeycloakRealmTheme `json:"theme,omitempty"`

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
