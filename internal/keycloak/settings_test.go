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

package keycloak

import (
	"testing"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

func TestResolveOptionsDefaultsEveryEnforcedField(t *testing.T) {
	realm, unknown := ResolveOptions(nil)

	if len(unknown) != 0 {
		t.Fatalf("no options declared, got unknown %v", unknown)
	}
	for _, field := range realmOptions {
		if realm[field] != defaultTheme {
			t.Errorf("field %s = %q, want the default %q", field, realm[field], defaultTheme)
		}
	}
}

func TestResolveOptionsAppliesRecognisedOverride(t *testing.T) {
	realm, unknown := ResolveOptions([]neteyev1alpha1.ServiceOption{
		{Name: "loginTheme", Value: "wp"},
	})

	if len(unknown) != 0 {
		t.Fatalf("loginTheme is recognised, got unknown %v", unknown)
	}
	if realm["loginTheme"] != "wp" {
		t.Errorf("loginTheme = %q, want the override %q", realm["loginTheme"], "wp")
	}
	if realm["adminTheme"] != defaultTheme {
		t.Errorf("adminTheme = %q, an unrelated override must not disturb it", realm["adminTheme"])
	}
}

// An unrecognised name must not take the instance down, and must not stop the
// recognised options around it from taking effect: that is what lets a CR
// written for a newer operator still reconcile on an older one.
func TestResolveOptionsIgnoresUnknownButKeepsTheRest(t *testing.T) {
	realm, unknown := ResolveOptions([]neteyev1alpha1.ServiceOption{
		{Name: "loginThemee", Value: "typo"},
		{Name: "adminTheme", Value: "wp"},
	})

	if len(unknown) != 1 || unknown[0] != "loginThemee" {
		t.Errorf("unknown = %v, want exactly [loginThemee]", unknown)
	}
	if realm["adminTheme"] != "wp" {
		t.Errorf("adminTheme = %q, the option after the typo must still apply", realm["adminTheme"])
	}
	if realm["loginTheme"] != defaultTheme {
		t.Errorf("loginTheme = %q, want the default: the typo set nothing", realm["loginTheme"])
	}
}

func TestDriftPatchCarriesOnlyDriftedFields(t *testing.T) {
	live := map[string]any{
		"loginTheme":   "hacked",
		"adminTheme":   defaultTheme,
		"smtpServer":   map[string]any{"host": "mail"},
		"displayName":  "NetEye",
		"accountTheme": defaultTheme,
		"emailTheme":   defaultTheme,
	}
	desired := Realm{"loginTheme": defaultTheme, "adminTheme": defaultTheme, "accountTheme": defaultTheme, "emailTheme": defaultTheme}

	patch, drifted := DriftPatch(live, desired)

	if !drifted {
		t.Fatal("loginTheme drifted, want drifted=true")
	}
	if len(patch) != 1 || patch["loginTheme"] != defaultTheme {
		t.Errorf("patch = %v, want only the drifted loginTheme", patch)
	}
	if _, ok := patch["displayName"]; ok {
		t.Error("patch carries a field the operator does not own; it would clobber it")
	}
}

// A realm created moments ago has no theme set at all, and that is precisely
// the case enforcement exists to fix.
func TestDriftPatchTreatsMissingFieldAsDrift(t *testing.T) {
	patch, drifted := DriftPatch(map[string]any{}, Realm{"loginTheme": defaultTheme})

	if !drifted || patch["loginTheme"] != defaultTheme {
		t.Errorf("patch = %v, drifted = %v; an absent field must count as drift", patch, drifted)
	}
}

func TestDriftPatchIsEmptyWhenInSync(t *testing.T) {
	live := map[string]any{"loginTheme": defaultTheme}

	patch, drifted := DriftPatch(live, Realm{"loginTheme": defaultTheme})

	if drifted || len(patch) != 0 {
		t.Errorf("patch = %v, drifted = %v; nothing drifted, want no write", patch, drifted)
	}
}
