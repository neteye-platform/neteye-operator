// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import "testing"

func TestEffectiveDeletionPolicy(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec *NetEyeSpec
		want NetEyeDeletionPolicy
	}{
		{name: "nil spec", spec: nil, want: NetEyeDeletionPolicyRetain},
		{name: "unset", spec: &NetEyeSpec{}, want: NetEyeDeletionPolicyRetain},
		{name: "explicit retain", spec: &NetEyeSpec{DeletionPolicy: NetEyeDeletionPolicyRetain}, want: NetEyeDeletionPolicyRetain},
		{name: "explicit delete", spec: &NetEyeSpec{DeletionPolicy: NetEyeDeletionPolicyDelete}, want: NetEyeDeletionPolicyDelete},
		// An object that bypassed defaulting must never be read as Delete.
		{name: "garbage value", spec: &NetEyeSpec{DeletionPolicy: NetEyeDeletionPolicy("nonsense")}, want: NetEyeDeletionPolicyRetain},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.EffectiveDeletionPolicy(); got != tt.want {
				t.Errorf("EffectiveDeletionPolicy() = %q, want %q", got, tt.want)
			}
		})
	}
}
