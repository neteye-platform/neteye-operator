// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import "testing"

// TestValidateReleaseData is the CI gate ADR-0003 requires on embedded release
// data: both releases in the support window must have a digest-pinned image set
// and the forward transition between them must be declared.
func TestValidateReleaseData(t *testing.T) {
	if err := ValidateReleaseData(); err != nil {
		t.Fatalf("embedded release data is invalid: %v", err)
	}
}

func TestSupportWindowIsManageable(t *testing.T) {
	for _, version := range []string{PreviousNetEyeVersion, CurrentNetEyeVersion} {
		if !IsSupportedVersion(version) {
			t.Errorf("IsSupportedVersion(%q) = false, want the whole support window to be manageable", version)
		}
		components, ok := ComponentsForVersion(version)
		if !ok {
			t.Fatalf("ComponentsForVersion(%q) is missing", version)
		}
		if components.KeycloakImage == "" {
			t.Errorf("release %q has no Keycloak image", version)
		}
	}
	// The two releases are distinct product lines and must not silently share
	// one image set, which is what happens when the window is advanced by hand.
	previous, _ := ComponentsForVersion(PreviousNetEyeVersion)
	current, _ := ComponentsForVersion(CurrentNetEyeVersion)
	if previous.KeycloakImage == current.KeycloakImage {
		t.Errorf("releases %q and %q share Keycloak image %q; the previous release's tested image set was not preserved",
			PreviousNetEyeVersion, CurrentNetEyeVersion, current.KeycloakImage)
	}
}

func TestUpgradeGraph(t *testing.T) {
	if !IsSupportedUpgrade(PreviousNetEyeVersion, CurrentNetEyeVersion) {
		t.Errorf("IsSupportedUpgrade(%q, %q) = false, want the window's forward transition to be declared", PreviousNetEyeVersion, CurrentNetEyeVersion)
	}
	if IsSupportedUpgrade(CurrentNetEyeVersion, PreviousNetEyeVersion) {
		t.Error("a downgrade is declared as a supported upgrade")
	}
	if IsSupportedUpgrade(CurrentNetEyeVersion, CurrentNetEyeVersion) {
		t.Error("a same-version transition is declared as an upgrade")
	}
	if IsSupportedUpgrade("4.48", CurrentNetEyeVersion) {
		t.Error("an upgrade from outside the support window is declared")
	}
}
