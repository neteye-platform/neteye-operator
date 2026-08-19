// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

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
