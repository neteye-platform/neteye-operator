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

// Package keycloak deploys and configures the NetEye-managed Keycloak
// instances. It splits along the one seam that matters: bootstrap runs once
// (a Job), enforcement runs forever (this operator). A Job cannot correct
// drift, which is why enforcement does not live in one.
package keycloak

import (
	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

// defaultTheme is the NetEye-branded Keycloak theme, shipped inside the
// neteye-keycloak image. It is a constant rather than a CRD default because
// materialising it in the object would erase the distinction between "the
// admin chose the NetEye theme" and "nobody chose anything".
const defaultTheme = "neteye"

// realmOptions maps every option name recognised in
// Keycloak.spec.additionalOptions to the realm representation field it
// controls. It is the single source of truth for what is overridable: adding
// an enforced setting means adding one row here, and anything absent from it
// is not overridable.
var realmOptions = map[string]string{
	"loginTheme":   "loginTheme",
	"adminTheme":   "adminTheme",
	"accountTheme": "accountTheme",
	"emailTheme":   "emailTheme",
}

// Realm is the set of realm fields the operator enforces, keyed by the field
// name Keycloak's realm representation uses.
type Realm map[string]string

// ResolveOptions computes the realm values to enforce: the operator's
// defaults, with any recognised override applied on top.
//
// Names that are not in the allow-list are returned in unknown, in the order
// they appeared, and are otherwise ignored — the recognised options around
// them still take effect. Rejecting them instead would mean a typo on an
// optional setting takes down the whole instance, and would stop an older
// operator from reconciling a CR written for a newer one.
func ResolveOptions(opts []neteyev1alpha1.ServiceOption) (realm Realm, unknown []string) {
	realm = Realm{}
	for _, field := range realmOptions {
		realm[field] = defaultTheme
	}

	for _, o := range opts {
		field, recognised := realmOptions[o.Name]
		if !recognised {
			unknown = append(unknown, o.Name)
			continue
		}
		realm[field] = o.Value
	}

	return realm, unknown
}

// DriftPatch compares the enforced fields of a live realm representation
// against the desired ones and returns the body of a partial update carrying
// only the fields that drifted, plus whether there was any drift at all.
//
// The patch deliberately carries nothing but drifted fields: Keycloak's realm
// update accepts a partial representation, so anything left out is left alone.
// That is what makes it safe to run enforcement on every reconcile without
// clobbering realm settings the operator does not own.
//
// A field absent from the live realm counts as drift — a realm created moments
// ago has no theme set, and that is precisely the case enforcement must fix.
func DriftPatch(live map[string]any, desired Realm) (map[string]any, bool) {
	patch := map[string]any{}

	for field, want := range desired {
		if current, ok := live[field].(string); !ok || current != want {
			patch[field] = want
		}
	}

	return patch, len(patch) > 0
}
