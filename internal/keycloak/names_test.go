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
	"strings"
	"testing"
)

// Every name is derived from the instance, so two NetEye instances sharing a
// namespace do not fight over the same Secrets, Job or upstream CR.
func TestNamesAreScopedToTheInstance(t *testing.T) {
	a := namesFor("alpha")
	b := namesFor("beta")

	pairs := [][2]string{
		{a.DBSecret, b.DBSecret},
		{a.TLSSecret, b.TLSSecret},
		{a.ClientSecret, b.ClientSecret},
		{a.BootstrapJob, b.BootstrapJob},
		{a.UpstreamKeycloak, b.UpstreamKeycloak},
		{a.InitialAdminSecret, b.InitialAdminSecret},
	}
	for _, p := range pairs {
		if p[0] == p[1] {
			t.Errorf("two instances share the name %q", p[0])
		}
		if !strings.HasPrefix(p[0], "alpha") {
			t.Errorf("name %q is not derived from the instance name", p[0])
		}
	}
}

// The upstream Keycloak Operator derives the initial-admin Secret name from
// the Keycloak CR name; getting this wrong means the bootstrap Job cannot log
// in, with no error until the Job runs.
func TestInitialAdminSecretFollowsUpstreamConvention(t *testing.T) {
	n := namesFor("alpha")

	if want := n.UpstreamKeycloak + "-initial-admin"; n.InitialAdminSecret != want {
		t.Errorf("InitialAdminSecret = %q, want %q", n.InitialAdminSecret, want)
	}
}

func TestServiceURLIsInClusterAndCarriesTheRelativePath(t *testing.T) {
	got := ServiceURL("alpha", "neteye-system")

	for _, want := range []string{"https://", "alpha", "neteye-system", ".svc", ":8443", httpRelativePath} {
		if !strings.Contains(got, want) {
			t.Errorf("ServiceURL = %q, want it to contain %q", got, want)
		}
	}
}

func TestDefaultHostnameIsScopedToTheInstance(t *testing.T) {
	if defaultHostname("alpha", "neteye-system") == defaultHostname("beta", "neteye-system") {
		t.Error("two instances in one namespace would present the same certificate hostname")
	}
}
