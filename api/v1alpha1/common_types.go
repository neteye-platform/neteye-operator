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

// Phase is the coarse lifecycle state of a NetEye instance or of one managed
// service. It answers "is this thing up?" and nothing else: aspects that can
// degrade independently of the deployment — settings drifting, options being
// ignored — are reported on Conditions instead, and deliberately do not move
// a healthy object out of PhaseReady.
type Phase string

const (
	// PhasePending is the state of an object that has been accepted but whose
	// reconciliation has not produced anything yet.
	PhasePending Phase = "Pending"

	// PhaseDeploying means the workloads are being created or are not yet
	// reporting readiness.
	PhaseDeploying Phase = "Deploying"

	// PhaseBootstrapping means the workloads are up and the one-shot
	// configuration step is still running.
	PhaseBootstrapping Phase = "Bootstrapping"

	// PhaseReady means the object is fully deployed and configured.
	PhaseReady Phase = "Ready"

	// PhaseFailed means reconciliation hit an error it cannot recover from
	// without a change to the spec.
	PhaseFailed Phase = "Failed"
)

// Condition types reported by every managed service, and rolled up onto the
// owning NetEye.
const (
	// ConditionAvailable reports whether the service's workloads are up.
	ConditionAvailable = "Available"

	// ConditionSettingsEnforced reports whether the last enforcement pass
	// successfully re-asserted every enforced setting. Enforcement failing is
	// a degraded instance, not a failed deploy, so it never moves Phase.
	ConditionSettingsEnforced = "SettingsEnforced"

	// ConditionAdditionalOptionsAccepted reports whether every entry in
	// spec.additionalOptions was recognised. Unrecognised entries are ignored,
	// not fatal.
	ConditionAdditionalOptionsAccepted = "AdditionalOptionsAccepted"

	// ConditionServicesReady is reported on NetEye only: whether every managed
	// service it owns has reached PhaseReady.
	ConditionServicesReady = "ServicesReady"
)

// ServiceOption is a single setting override: it replaces the operator's
// compiled-in default for one enforced setting.
//
// Only names the operator recognises have an effect. An unrecognised name is
// ignored rather than rejected, so a CR written for a newer operator still
// reconciles on an older one; the ignored names are reported on
// status.conditions rather than failing the object.
//
// Overriding a setting does not make it unenforced: the resulting value is
// still re-asserted on every reconcile pass.
type ServiceOption struct {
	// Name of the setting to override, e.g. "loginTheme".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Value to enforce instead of the operator's default.
	// +kubebuilder:validation:Required
	Value string `json:"value"`
}

// ServiceReference is the rolled-up state of one managed service, surfaced on
// the owning NetEye so that `kubectl get neteye` answers "what is broken"
// without a second lookup. The service's own CR remains the authority: this
// is a cache, not the source of truth.
type ServiceReference struct {
	// Kind of the managed service CR, e.g. "Keycloak".
	Kind string `json:"kind"`

	// Name of the managed service CR.
	Name string `json:"name"`

	// Namespace the managed service CR lives in.
	Namespace string `json:"namespace"`

	// Phase last observed on the managed service CR.
	Phase Phase `json:"phase,omitempty"`

	// Message last observed on the managed service CR.
	Message string `json:"message,omitempty"`
}
