// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import "testing"

func TestIsLatestVersion(t *testing.T) {
	if !IsLatestVersion(CurrentNetEyeVersion) {
		t.Errorf("IsLatestVersion(%q) = false, want true", CurrentNetEyeVersion)
	}
	if IsLatestVersion(PreviousNetEyeVersion) {
		t.Errorf("IsLatestVersion(%q) = true, want false", PreviousNetEyeVersion)
	}
	if IsLatestVersion("0.0") {
		t.Error("IsLatestVersion(\"0.0\") = true, want false")
	}
}

func TestIsPreviousVersion(t *testing.T) {
	if !IsPreviousVersion(PreviousNetEyeVersion) {
		t.Errorf("IsPreviousVersion(%q) = false, want true", PreviousNetEyeVersion)
	}
	if IsPreviousVersion(CurrentNetEyeVersion) {
		t.Errorf("IsPreviousVersion(%q) = true, want false", CurrentNetEyeVersion)
	}
}

func TestIsSupportedVersion(t *testing.T) {
	if !IsSupportedVersion(CurrentNetEyeVersion) {
		t.Errorf("IsSupportedVersion(%q) = false, want true", CurrentNetEyeVersion)
	}
	if IsSupportedVersion("0.0") {
		t.Error("IsSupportedVersion(\"0.0\") = true, want false")
	}
}

func TestComponentsForVersion(t *testing.T) {
	c, ok := ComponentsForVersion(CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("ComponentsForVersion(%q) not found", CurrentNetEyeVersion)
	}
	if c.KeycloakImage == "" {
		t.Error("expected a resolved Keycloak image")
	}
	if _, ok := ComponentsForVersion("0.0"); ok {
		t.Error("ComponentsForVersion(\"0.0\") found, want not found")
	}
}

func TestSupportedVersions(t *testing.T) {
	versions := SupportedVersions()
	for _, v := range versions {
		if v == CurrentNetEyeVersion {
			return
		}
	}
	t.Errorf("SupportedVersions() = %v, missing %q", versions, CurrentNetEyeVersion)
}
