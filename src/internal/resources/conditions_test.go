/*
Copyright 2026 Wuerth IT | Italy.

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

package resources

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func conditionObj(t *testing.T, conditions ...map[string]any) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	raw := make([]any, 0, len(conditions))
	for _, c := range conditions {
		raw = append(raw, c)
	}
	if err := unstructured.SetNestedSlice(obj.Object, raw, "status", "conditions"); err != nil {
		t.Fatalf("set conditions: %v", err)
	}
	return obj
}

func TestReadyConditionMessage(t *testing.T) {
	tests := []struct {
		name        string
		obj         *unstructured.Unstructured
		wantReady   bool
		wantMessage string
	}{
		{
			name:        "no status",
			obj:         &unstructured.Unstructured{Object: map[string]any{}},
			wantMessage: "waiting for Test status conditions",
		},
		{
			name:      "ready true",
			obj:       conditionObj(t, map[string]any{"type": "Ready", "status": "True"}),
			wantReady: true,
		},
		{
			name:        "ready false with message",
			obj:         conditionObj(t, map[string]any{"type": "Ready", "status": "False", "message": "still starting"}),
			wantMessage: "still starting",
		},
		{
			name:        "ready false falls back to reason",
			obj:         conditionObj(t, map[string]any{"type": "Ready", "status": "False", "reason": "Pending"}),
			wantMessage: "Pending",
		},
		{
			name:        "ready false without detail",
			obj:         conditionObj(t, map[string]any{"type": "Ready", "status": "False"}),
			wantMessage: "waiting for Test Ready condition",
		},
		{
			name:        "no ready condition",
			obj:         conditionObj(t, map[string]any{"type": "Other", "status": "True"}),
			wantMessage: "waiting for Test Ready condition",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, message, err := ReadyConditionMessage(tt.obj, "Test")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ready != tt.wantReady {
				t.Errorf("ready = %v, want %v", ready, tt.wantReady)
			}
			if message != tt.wantMessage {
				t.Errorf("message = %q, want %q", message, tt.wantMessage)
			}
		})
	}
}
