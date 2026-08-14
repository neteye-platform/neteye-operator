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

package controller

import (
	"strings"
	"testing"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

func TestAggregatePhase(t *testing.T) {
	svc := func(kind string, phase neteyev1alpha1.Phase) neteyev1alpha1.ServiceReference {
		return neteyev1alpha1.ServiceReference{Kind: kind, Name: "sample", Phase: phase}
	}

	cases := []struct {
		name     string
		services []neteyev1alpha1.ServiceReference
		want     neteyev1alpha1.Phase
	}{
		{
			name: "no services declared is Ready: there is nothing left to do",
			want: neteyev1alpha1.PhaseReady,
		},
		{
			name:     "all Ready",
			services: []neteyev1alpha1.ServiceReference{svc("Keycloak", neteyev1alpha1.PhaseReady)},
			want:     neteyev1alpha1.PhaseReady,
		},
		{
			name: "a failure outranks a service that is merely still working",
			services: []neteyev1alpha1.ServiceReference{
				svc("Keycloak", neteyev1alpha1.PhaseDeploying),
				svc("Other", neteyev1alpha1.PhaseFailed),
			},
			want: neteyev1alpha1.PhaseFailed,
		},
		{
			name: "one service still working keeps the whole NetEye out of Ready",
			services: []neteyev1alpha1.ServiceReference{
				svc("Keycloak", neteyev1alpha1.PhaseReady),
				svc("Other", neteyev1alpha1.PhaseBootstrapping),
			},
			want: neteyev1alpha1.PhaseBootstrapping,
		},
		{
			name:     "an empty phase means the service has not reported yet",
			services: []neteyev1alpha1.ServiceReference{svc("Keycloak", "")},
			want:     neteyev1alpha1.PhasePending,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, message := aggregatePhase(tc.services)

			if got != tc.want {
				t.Errorf("phase = %q, want %q", got, tc.want)
			}
			if got != neteyev1alpha1.PhaseReady && !strings.Contains(message, string(tc.want)) {
				t.Errorf("message = %q, must name what is holding the NetEye back", message)
			}
		})
	}
}

// The message must point at the service that is holding the NetEye back, not
// merely restate the phase: that is the whole reason it is rolled up.
func TestAggregatePhaseMessageNamesTheOffendingService(t *testing.T) {
	_, message := aggregatePhase([]neteyev1alpha1.ServiceReference{
		{Kind: "Keycloak", Name: "sample", Phase: neteyev1alpha1.PhaseFailed, Message: "image pull failed"},
	})

	for _, want := range []string{"Keycloak", "sample", "image pull failed"} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, want it to mention %q", message, want)
		}
	}
}
