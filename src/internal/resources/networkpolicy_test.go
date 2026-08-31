// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"reflect"
	"testing"
)

func TestDefaultDenyNetworkPolicySpec(t *testing.T) {
	spec := defaultDenyNetworkPolicySpec()
	if !reflect.DeepEqual(spec["endpointSelector"], map[string]any{}) {
		t.Errorf("endpoint selector = %#v, want empty", spec["endpointSelector"])
	}
	if !reflect.DeepEqual(spec["enableDefaultDeny"], map[string]any{"ingress": true, "egress": true}) {
		t.Errorf("default-deny settings = %#v, want ingress and egress enabled", spec["enableDefaultDeny"])
	}
	if !reflect.DeepEqual(spec["ingress"], []any{map[string]any{"fromEntities": []any{"none"}}}) {
		t.Errorf("ingress rules = %#v, want non-matching none entity", spec["ingress"])
	}
	if !reflect.DeepEqual(spec["egress"], []any{map[string]any{"toEntities": []any{"none"}}}) {
		t.Errorf("egress rules = %#v, want non-matching none entity", spec["egress"])
	}
}
