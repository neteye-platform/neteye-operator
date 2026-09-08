// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestComponentResults(t *testing.T) {
	failure := errors.New("apply failed")
	blocked, err := blockedResult("telemetry", []componentID{"identity"}, "waiting for identity", time.Minute)
	if err != nil {
		t.Fatalf("blocked result: %v", err)
	}
	degraded, err := degradedResult("telemetry", "ApplyFailed", "could not apply", time.Minute, failure)
	if err != nil {
		t.Fatalf("degraded result: %v", err)
	}
	ready, err := readyResult("identity", "Available", "ready")
	if err != nil {
		t.Fatalf("ready result: %v", err)
	}
	progressing, err := progressingResult("identity", "Waiting", "waiting", time.Second)
	if err != nil {
		t.Fatalf("progressing result: %v", err)
	}
	tests := []struct {
		name string
		got  componentResult
		want componentState
	}{
		{"ready", ready, componentStateReady},
		{"progressing", progressing, componentStateProgressing},
		{"degraded", degraded, componentStateDegraded},
		{"blocked", blocked, componentStateBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got.State != tt.want {
				t.Errorf("state = %q, want %q", tt.got.State, tt.want)
			}
			if tt.got.ID == "" {
				t.Error("component result has an empty ID")
			}
			if tt.want == componentStateDegraded && !errors.Is(tt.got.Err, failure) {
				t.Error("intrinsic failure error was not preserved")
			}
			if tt.want != componentStateDegraded && tt.got.Err != nil {
				t.Errorf("error = %v, want nil", tt.got.Err)
			}
		})
	}
	if blocked.Reason != dependencyNotReadyReason || !reflect.DeepEqual(blocked.BlockingDependencies, []componentID{"identity"}) {
		t.Errorf("blocked result = %+v", blocked)
	}
	blocking := []componentID{"identity"}
	copyResult, err := blockedResult("telemetry", blocking, "waiting", 0)
	if err != nil {
		t.Fatalf("copied blocked result: %v", err)
	}
	blocking[0] = "changed"
	if copyResult.BlockingDependencies[0] != "identity" {
		t.Error("blocked result retained caller-owned dependencies")
	}
	if _, err := degradedResult("telemetry", "ApplyFailed", "failed", 0, nil); err == nil {
		t.Error("expected degraded result without error to fail")
	}
	if _, err := readyResult("", "Available", "ready"); err == nil {
		t.Error("expected ready result without ID to fail")
	}
	if _, err := blockedResult("telemetry", nil, "waiting", 0); err == nil {
		t.Error("expected blocked result without dependencies to fail")
	}
}

func TestLifecycleGraphValidationAndEligibility(t *testing.T) {
	for _, nodes := range [][]lifecycleNode{
		{{ID: ""}},
		{{ID: "a"}, {ID: "a"}},
		{{ID: "a", Dependencies: []componentID{"missing"}}},
		{{ID: "a"}, {ID: "b", Dependencies: []componentID{"a", "a"}}},
		{{ID: "a", Dependencies: []componentID{"b"}}, {ID: "b", Dependencies: []componentID{"a"}}},
	} {
		if _, err := newLifecycleGraph(nodes); err == nil {
			t.Errorf("expected invalid graph %v", nodes)
		}
	}
	nodes := []lifecycleNode{{ID: "telemetry", Dependencies: []componentID{"identity"}}, {ID: "identity"}, {ID: "alerts"}}
	g, err := newLifecycleGraph(nodes)
	if err != nil {
		t.Fatalf("new graph: %v", err)
	}
	if got := g.orderedNodes(); !reflect.DeepEqual(got, []lifecycleNode{{ID: "alerts"}, {ID: "identity"}, {ID: "telemetry", Dependencies: []componentID{"identity"}}}) {
		t.Errorf("order = %v", got)
	}
	nodes[0].Dependencies[0] = "alerts"
	got, err := g.blockingDependencies("telemetry", nil)
	if err != nil {
		t.Fatalf("blocking dependencies: %v", err)
	}
	if !reflect.DeepEqual(got, []componentID{"identity"}) {
		t.Errorf("graph retained caller-owned dependencies: %v", got)
	}
	if got, err := g.blockingDependencies("alerts", nil); err != nil || len(got) != 0 {
		t.Errorf("root blocking dependencies = %v, %v", got, err)
	}
	if _, err := g.blockingDependencies("unknown", nil); err == nil {
		t.Error("expected unknown lifecycle node lookup to fail")
	}
	progressing, err := progressingResult("identity", "Waiting", "waiting", time.Second)
	if err != nil {
		t.Fatalf("progressing result: %v", err)
	}
	results := map[componentID]componentResult{"identity": progressing}
	if got := ids(g.eligible(results)); !reflect.DeepEqual(got, []componentID{"alerts", "identity"}) {
		t.Errorf("eligible before ready = %v", got)
	}
	ready, err := readyResult("identity", "Available", "ready")
	if err != nil {
		t.Fatalf("ready result: %v", err)
	}
	results["identity"] = ready
	if got := ids(g.eligible(results)); !reflect.DeepEqual(got, []componentID{"alerts", "identity", "telemetry"}) {
		t.Errorf("eligible after ready = %v", got)
	}
}

func TestLifecycleGraphOrdersNewlyAvailableNodesDeterministically(t *testing.T) {
	g, err := newLifecycleGraph([]lifecycleNode{
		{ID: "zeta"},
		{ID: "beta"},
		{ID: "alpha", Dependencies: []componentID{"beta"}},
	})
	if err != nil {
		t.Fatalf("new graph: %v", err)
	}
	if got := ids(g.orderedNodes()); !reflect.DeepEqual(got, []componentID{"beta", "alpha", "zeta"}) {
		t.Errorf("order = %v", got)
	}
}

func TestRunComponentOperationsIsolatesFailuresAndBlocksDependants(t *testing.T) {
	g, err := newLifecycleGraph([]lifecycleNode{
		{ID: identityComponentID}, {ID: telemetryComponentID},
		{ID: "dashboard", Dependencies: []componentID{telemetryComponentID}},
		{ID: "child", Dependencies: []componentID{"dashboard"}},
	})
	if err != nil {
		t.Fatalf("new graph: %v", err)
	}
	identityCalls, telemetryCalls, blockedCalls := 0, 0, 0
	failure := errors.New("telemetry failed")
	results, err := runComponentOperations(g, map[componentID]componentOperation{
		identityComponentID: func() (componentResult, error) {
			identityCalls++
			return readyResult(identityComponentID, "Available", "ready")
		},
		telemetryComponentID: func() (componentResult, error) {
			telemetryCalls++
			return degradedResult(telemetryComponentID, "ApplyFailed", "failed", time.Minute, failure)
		},
		"dashboard": func() (componentResult, error) { blockedCalls++; return readyResult("dashboard", "Available", "ready") },
		"child":     func() (componentResult, error) { blockedCalls++; return readyResult("child", "Available", "ready") },
	})
	if err != nil {
		t.Fatalf("run operations: %v", err)
	}
	if identityCalls != 1 || telemetryCalls != 1 || blockedCalls != 0 {
		t.Errorf("calls = identity:%d telemetry:%d blocked:%d", identityCalls, telemetryCalls, blockedCalls)
	}
	if len(results) != 4 || results[identityComponentID].State != componentStateReady || !errors.Is(results[telemetryComponentID].Err, failure) {
		t.Errorf("results = %+v", results)
	}
	for id, blocker := range map[componentID]componentID{"dashboard": telemetryComponentID, "child": "dashboard"} {
		result := results[id]
		if result.State != componentStateBlocked || result.Reason != dependencyNotReadyReason || !reflect.DeepEqual(result.BlockingDependencies, []componentID{blocker}) {
			t.Errorf("%s result = %+v", id, result)
		}
	}
}

func TestRunComponentOperationsRejectsMismatchedOperationsBeforeInvocation(t *testing.T) {
	g, err := newLifecycleGraph([]lifecycleNode{{ID: "a"}})
	if err != nil {
		t.Fatalf("new graph: %v", err)
	}
	called := false
	_, err = runComponentOperations(g, map[componentID]componentOperation{"b": func() (componentResult, error) { called = true; return readyResult("b", "", "") }})
	if err == nil || called {
		t.Errorf("err=%v called=%t, want systemic mismatch without invocation", err, called)
	}
}

func TestValidateOperationResultRejectsMalformedResults(t *testing.T) {
	failure := errors.New("failure")
	for _, result := range []componentResult{
		{ID: "a", State: componentStateReady, Err: failure},
		{ID: "a", State: componentStateProgressing, Err: failure},
		{ID: "a", State: componentStateBlocked, Reason: dependencyNotReadyReason},
		{ID: "a", State: componentStateBlocked, Reason: "WrongReason", BlockingDependencies: []componentID{"b"}},
		{ID: "a", State: componentStateBlocked, Reason: dependencyNotReadyReason, BlockingDependencies: []componentID{"b"}, Err: failure},
		{ID: "a", State: componentStateReady, BlockingDependencies: []componentID{"b"}},
	} {
		if err := validateOperationResult("a", result); err == nil {
			t.Errorf("malformed result %+v was accepted", result)
		}
	}
}

func TestRunComponentOperationsIsolatesIdentityFailureFromTelemetry(t *testing.T) {
	g, err := newLifecycleGraph([]lifecycleNode{{ID: identityComponentID}, {ID: telemetryComponentID}})
	if err != nil {
		t.Fatalf("new graph: %v", err)
	}
	failure := errors.New("identity failed")
	telemetryCalls := 0
	results, err := runComponentOperations(g, map[componentID]componentOperation{
		identityComponentID: func() (componentResult, error) {
			return degradedResult(identityComponentID, "ApplyFailed", "failed", time.Minute, failure)
		},
		telemetryComponentID: func() (componentResult, error) {
			telemetryCalls++
			return readyResult(telemetryComponentID, "Available", "ready")
		},
	})
	if err != nil {
		t.Fatalf("run operations: %v", err)
	}
	if telemetryCalls != 1 || len(results) != 2 || !errors.Is(results[identityComponentID].Err, failure) || results[telemetryComponentID].State != componentStateReady {
		t.Errorf("calls=%d results=%+v", telemetryCalls, results)
	}
}

func ids(nodes []lifecycleNode) []componentID {
	ids := make([]componentID, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
	}
	return ids
}
