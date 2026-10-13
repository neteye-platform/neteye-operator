// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"strings"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func resultsFrom(entries ...componentResult) map[componentID]componentResult {
	results := make(map[componentID]componentResult, len(entries))
	for _, entry := range entries {
		results[entry.ID] = entry
	}
	return results
}

func conditionOf(t *testing.T, status neteye.NetEyeStatus, conditionType string) metav1.Condition {
	t.Helper()
	condition := apimeta.FindStatusCondition(status.Conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %q is missing; conditions = %+v", conditionType, status.Conditions)
	}
	return *condition
}

func statusFor(generation int64, version string, entries ...componentResult) neteye.NetEyeStatus {
	status := neteye.NetEyeStatus{ObservedGeneration: generation}
	status.Components = buildComponentStatus(generation, resultsFrom(entries...))
	applyComponentConditions(&status, generation)
	applyUpgradeAvailableCondition(&status, generation, version)
	applyCurrentVersion(&status, version)
	return status
}

// This is the case a single phase value cannot represent, and the reason the
// conditions were added: a healthy installation on the previous release is
// ready and has an upgrade available at the same time.
func TestHealthyPreviousReleaseIsReadyAndUpgradable(t *testing.T) {
	status := statusFor(4, neteye.PreviousNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateReady, Reason: "Available"},
		componentResult{ID: otelCollectorComponentID, State: componentStateReady, Reason: disabledReason},
		componentResult{ID: edotGatewayComponentID, State: componentStateReady, Reason: disabledReason},
	)

	if ready := conditionOf(t, status, neteye.ConditionReady); ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %q (reason %q), want True for a fully reconciled installation", ready.Status, ready.Reason)
	}
	upgrade := conditionOf(t, status, neteye.ConditionUpgradeAvailable)
	if upgrade.Status != metav1.ConditionTrue || upgrade.Reason != neteye.ReasonUpgradeAvailable {
		t.Errorf("UpgradeAvailable = %q/%q, want True/%s", upgrade.Status, upgrade.Reason, neteye.ReasonUpgradeAvailable)
	}
	if status.CurrentVersion != neteye.PreviousNetEyeVersion {
		t.Errorf("currentVersion = %q, want %q", status.CurrentVersion, neteye.PreviousNetEyeVersion)
	}
}

// ADR-0005's headline scenario: one branch failed while an independent branch
// is still converging. Both facts have to be reported at once.
func TestDegradedAndProgressingAreIndependent(t *testing.T) {
	status := statusFor(4, neteye.CurrentNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateProgressing, Reason: "ResourcesNotReady"},
		componentResult{ID: otelCollectorComponentID, State: componentStateDegraded, Reason: "ConfigMapMissing"},
		componentResult{ID: edotGatewayComponentID, State: componentStateReady, Reason: "Available"},
	)

	if degraded := conditionOf(t, status, neteye.ConditionDegraded); degraded.Status != metav1.ConditionTrue {
		t.Errorf("Degraded = %q, want True", degraded.Status)
	}
	if progressing := conditionOf(t, status, neteye.ConditionProgressing); progressing.Status != metav1.ConditionTrue {
		t.Errorf("Progressing = %q, want True at the same time as Degraded", progressing.Status)
	}
	ready := conditionOf(t, status, neteye.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != neteye.ReasonComponentsDegraded {
		t.Errorf("Ready = %q/%q, want False/%s", ready.Status, ready.Reason, neteye.ReasonComponentsDegraded)
	}
	if status.CurrentVersion != "" {
		t.Errorf("currentVersion = %q, want it to stay unset until the release is fully applied", status.CurrentVersion)
	}
}

// A component waiting on a healthy prerequisite is not itself a failure, and
// automation must be able to tell the two apart by reason alone.
func TestBlockedComponentIsNotDegraded(t *testing.T) {
	status := statusFor(4, neteye.CurrentNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateProgressing, Reason: "ServiceNotReady"},
		componentResult{
			ID: otelCollectorComponentID, State: componentStateBlocked, Reason: dependencyNotReadyReason,
			BlockingDependencies: []componentID{identityComponentID},
		},
	)

	if degraded := conditionOf(t, status, neteye.ConditionDegraded); degraded.Status != metav1.ConditionFalse {
		t.Errorf("Degraded = %q, want False when a component is only waiting for a prerequisite", degraded.Status)
	}
	entry := status.Components[string(otelCollectorComponentID)]
	if entry.Status != neteye.ComponentStateBlocked {
		t.Errorf("component status = %q, want %q", entry.Status, neteye.ComponentStateBlocked)
	}
	if entry.Reason != neteye.ReasonDependencyNotReady {
		t.Errorf("component reason = %q, want %q so automation can distinguish it from a failure", entry.Reason, neteye.ReasonDependencyNotReady)
	}
	if len(entry.BlockingDependencies) != 1 || entry.BlockingDependencies[0] != string(identityComponentID) {
		t.Errorf("blockingDependencies = %v, want the prerequisite identified", entry.BlockingDependencies)
	}
}

// Every component reports the complete, digest-pinned image set the operator
// selected for it, so the deployed software set is auditable from status alone.
func TestResolvedImagesArePublishedAndDigestPinned(t *testing.T) {
	components, ok := neteye.ComponentsForVersion(neteye.CurrentNetEyeVersion)
	if !ok {
		t.Fatal("current release has no component image set")
	}
	status := statusFor(4, neteye.CurrentNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateReady, Reason: "Available", ResolvedImages: identityResolvedImages(components)},
		componentResult{ID: otelCollectorComponentID, State: componentStateReady, Reason: "Available", ResolvedImages: otelCollectorResolvedImages(components)},
		componentResult{ID: edotGatewayComponentID, State: componentStateReady, Reason: "Available", ResolvedImages: edotGatewayResolvedImages(components)},
	)

	wantNames := map[string][]string{
		string(identityComponentID):      {"server"},
		string(otelCollectorComponentID): {"collector", "ca-bundle"},
		string(edotGatewayComponentID):   {"gateway", "ca-bundle"},
	}
	for component, names := range wantNames {
		entry, present := status.Components[component]
		if !present {
			t.Errorf("component %q is missing from status", component)
			continue
		}
		if len(entry.ResolvedImages) != len(names) {
			t.Errorf("component %q resolved images = %v, want %d entries", component, entry.ResolvedImages, len(names))
		}
		for _, image := range entry.ResolvedImages {
			if image.Name == "" {
				t.Errorf("component %q has an image with no logical name", component)
			}
			if !strings.Contains(image.Image, "@sha256:") {
				t.Errorf("component %q image %q is not pinned by digest", component, image.Image)
			}
		}
	}
}

// ADR-0002: a component with no container images reports an empty list rather
// than an absent field, so consumers never have to distinguish the two.
func TestComponentWithoutImagesReportsEmptyList(t *testing.T) {
	status := statusFor(4, neteye.CurrentNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateReady, Reason: "Available"},
	)
	entry := status.Components[string(identityComponentID)]
	if entry.ResolvedImages == nil {
		t.Error("resolvedImages is nil, want an empty list")
	}
	if len(entry.ResolvedImages) != 0 {
		t.Errorf("resolvedImages = %v, want empty", entry.ResolvedImages)
	}
}

// lastTransitionTime is the one thing a phase cannot carry, and it is only
// meaningful if it survives passes in which the status did not change.
func TestLastTransitionTimeIsPreservedAcrossPasses(t *testing.T) {
	ready := componentResult{ID: identityComponentID, State: componentStateReady, Reason: "Available"}
	status := statusFor(4, neteye.CurrentNetEyeVersion, ready)
	first := conditionOf(t, status, neteye.ConditionReady).LastTransitionTime

	// Backdate so a rewrite would be visible.
	for i := range status.Conditions {
		status.Conditions[i].LastTransitionTime = metav1.NewTime(first.Add(-time.Hour))
	}
	backdated := conditionOf(t, status, neteye.ConditionReady).LastTransitionTime

	status.Components = buildComponentStatus(5, resultsFrom(ready))
	applyComponentConditions(&status, 5)
	after := conditionOf(t, status, neteye.ConditionReady)

	if !after.LastTransitionTime.Equal(&backdated) {
		t.Errorf("lastTransitionTime = %v, want it preserved at %v when the status did not change", after.LastTransitionTime, backdated)
	}
	if after.ObservedGeneration != 5 {
		t.Errorf("observedGeneration = %d, want it refreshed to 5", after.ObservedGeneration)
	}
}

func TestLastTransitionTimeMovesWhenStatusChanges(t *testing.T) {
	status := statusFor(4, neteye.CurrentNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateReady, Reason: "Available"},
	)
	for i := range status.Conditions {
		status.Conditions[i].LastTransitionTime = metav1.NewTime(status.Conditions[i].LastTransitionTime.Add(-time.Hour))
	}
	before := conditionOf(t, status, neteye.ConditionReady).LastTransitionTime

	status.Components = buildComponentStatus(4, resultsFrom(
		componentResult{ID: identityComponentID, State: componentStateDegraded, Reason: "EnsureResourcesFailed"},
	))
	applyComponentConditions(&status, 4)
	after := conditionOf(t, status, neteye.ConditionReady)

	if after.Status != metav1.ConditionFalse {
		t.Fatalf("Ready = %q, want False", after.Status)
	}
	if after.LastTransitionTime.Equal(&before) {
		t.Error("lastTransitionTime did not move when Ready flipped, so alerting could not tell how long it has been failing")
	}
}

// An unsupported version reaching storage must report the stable reason
// ADR-0003 names, and must not report a half-applied release as achieved.
func TestUnsupportedVersionReportsStableReason(t *testing.T) {
	status := neteye.NetEyeStatus{CurrentVersion: neteye.CurrentNetEyeVersion}
	failCondition(&status, 7, neteye.ReasonUnsupportedVersion, "unsupported NetEye version '9.99'")
	applyUpgradeAvailableCondition(&status, 7, "9.99")
	applyCurrentVersion(&status, "9.99")

	ready := conditionOf(t, status, neteye.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != neteye.ReasonUnsupportedVersion {
		t.Errorf("Ready = %q/%q, want False/%s", ready.Status, ready.Reason, neteye.ReasonUnsupportedVersion)
	}
	if status.CurrentVersion != neteye.CurrentNetEyeVersion {
		t.Errorf("currentVersion = %q, want the previously achieved release to be kept", status.CurrentVersion)
	}
}

func TestDisabledComponentDoesNotBlockReadiness(t *testing.T) {
	status := statusFor(4, neteye.CurrentNetEyeVersion,
		componentResult{ID: identityComponentID, State: componentStateReady, Reason: "Available"},
		componentResult{ID: otelCollectorComponentID, State: componentStateReady, Reason: disabledReason},
	)
	if ready := conditionOf(t, status, neteye.ConditionReady); ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %q, want True when a component is disabled rather than failing", ready.Status)
	}
	if got := status.Components[string(otelCollectorComponentID)].Status; got != neteye.ComponentStateDisabled {
		t.Errorf("component status = %q, want %q", got, neteye.ComponentStateDisabled)
	}
}
