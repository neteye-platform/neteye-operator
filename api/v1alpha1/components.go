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

import "fmt"

// Components holds the resolved image references for a given NetEye version.
// The NetEye CR declares a product version; the operator — not the admin —
// decides which container images that version is made of, and stamps them
// onto the managed-service CRs it creates.
type Components struct {
	// KeycloakImage is the full image reference for the Keycloak container.
	KeycloakImage string

	// KeycloakConfigImage is the ansible-runner image that runs the one-shot
	// post-deploy Keycloak configuration Job.
	KeycloakConfigImage string
}

// imageRegistry is the registry every component image is pulled from. It is a
// single var rather than being repeated in each entry of versionMap so that
// pointing a build at a different registry is one edit, and so that the map
// stays readable as versions accumulate.
//
// TODO: this is a development registry. Make it configurable (operator flag or
// ConfigMap) before this ships.
var imageRegistry = "172.19.69.105:5000"

// versionMap maps a NetEye product version to the component images that
// version ships. Add a row here when a NetEye release changes any component
// image; nothing outside this file needs to know the mapping exists.
var versionMap = map[string]Components{
	"4.36": components("neteye-keycloak:test", "neteye-keycloak-config:v0.2.27"),
	"4.37": components("neteye-keycloak:test", "neteye-keycloak-config:v0.2.27"),
	"4.50": components("neteye-keycloak:test", "neteye-keycloak-config:v0.2.27"),
}

func components(keycloak, keycloakConfig string) Components {
	return Components{
		KeycloakImage:       fmt.Sprintf("%s/%s", imageRegistry, keycloak),
		KeycloakConfigImage: fmt.Sprintf("%s/%s", imageRegistry, keycloakConfig),
	}
}

// ComponentsForVersion returns the component image set for neteyeVersion. The
// second return value is false for a version this operator build does not
// know about, which the caller surfaces as PhaseFailed: deploying an unknown
// version would mean guessing at images.
func ComponentsForVersion(neteyeVersion string) (Components, bool) {
	c, ok := versionMap[neteyeVersion]
	return c, ok
}

// SupportedVersions lists the NetEye versions this operator build can deploy,
// for error messages that tell the admin what to write instead.
func SupportedVersions() []string {
	versions := make([]string, 0, len(versionMap))
	for v := range versionMap {
		versions = append(versions, v)
	}
	return versions
}
