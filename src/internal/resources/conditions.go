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
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ReadyConditionMessage scans status.conditions for a "Ready" condition and
// reports whether it is True. When it is not ready, it returns a human-readable
// message describing what is being waited on, using subject to name the resource
// (for example "TLS Certificate" or "Keycloak").
func ReadyConditionMessage(obj *unstructured.Unstructured, subject string) (bool, string, error) {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		return false, "", err
	}
	if !found || len(conditions) == 0 {
		return false, fmt.Sprintf("waiting for %s status conditions", subject), nil
	}

	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]any)
		if !ok {
			continue
		}
		if conditionType, _, _ := unstructured.NestedString(condition, "type"); conditionType != "Ready" {
			continue
		}
		if status, _, _ := unstructured.NestedString(condition, "status"); status == "True" {
			return true, "", nil
		}

		message, _, _ := unstructured.NestedString(condition, "message")
		if message == "" {
			message, _, _ = unstructured.NestedString(condition, "reason")
		}
		if message == "" {
			message = fmt.Sprintf("waiting for %s Ready condition", subject)
		}
		return false, message, nil
	}

	return false, fmt.Sprintf("waiting for %s Ready condition", subject), nil
}
