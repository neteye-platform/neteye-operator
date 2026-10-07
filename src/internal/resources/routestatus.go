// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RouteParent is the Gateway listener a route must be accepted by.
type RouteParent struct {
	// Group and Kind default to the Gateway API Gateway when empty.
	Group, Kind              string
	Namespace, Name, Section string
}

// IsRouteReady reports whether the named route of the given kind is accepted
// by, and resolves its references for, the expected Gateway listener, as
// observed for the route's current generation.
func IsRouteReady(ctx context.Context, c client.Client, namespace, routeName, kind string, expected RouteParent) (bool, string, error) {
	if expected.Kind == "" {
		expected.Kind = "Gateway"
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: kind})
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: routeName}, route); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("%s %s is not created", kind, routeName), nil
		}
		return false, "", err
	}
	parents, found, err := unstructured.NestedSlice(route.Object, "status", "parents")
	if err != nil {
		return false, "", err
	}
	if !found || len(parents) == 0 {
		return false, fmt.Sprintf("waiting for Gateway %s/%s to report %s %s status", expected.Namespace, expected.Name, kind, routeName), nil
	}
	expectedGroup := expected.Group
	if expectedGroup == "" {
		expectedGroup = "gateway.networking.k8s.io"
	}
	for _, rawParent := range parents {
		parent, ok := rawParent.(map[string]any)
		if !ok {
			continue
		}
		ref, ok := parent["parentRef"].(map[string]any)
		if !ok {
			continue
		}
		group, _, _ := unstructured.NestedString(ref, "group")
		if group == "" {
			group = "gateway.networking.k8s.io"
		}
		parentKind, _, _ := unstructured.NestedString(ref, "kind")
		if parentKind == "" {
			parentKind = "Gateway"
		}
		parentNamespace, _, _ := unstructured.NestedString(ref, "namespace")
		parentName, _, _ := unstructured.NestedString(ref, "name")
		section, _, _ := unstructured.NestedString(ref, "sectionName")
		if group != expectedGroup || parentKind != expected.Kind || parentNamespace != expected.Namespace || parentName != expected.Name || section != expected.Section {
			continue
		}
		conditions, _, err := unstructured.NestedSlice(parent, "conditions")
		if err != nil {
			return false, "", err
		}
		for _, conditionType := range []string{"Accepted", "ResolvedRefs"} {
			var condition map[string]any
			for _, rawCondition := range conditions {
				candidate, ok := rawCondition.(map[string]any)
				if ok && candidate["type"] == conditionType {
					condition = candidate
					break
				}
			}
			if condition == nil {
				return false, fmt.Sprintf("waiting for %s %s %s condition from Gateway %s/%s", kind, routeName, conditionType, expected.Namespace, expected.Name), nil
			}
			observed, found, err := unstructured.NestedInt64(condition, "observedGeneration")
			if err != nil {
				return false, "", err
			}
			if found && observed < route.GetGeneration() {
				return false, fmt.Sprintf("%s %s %s status is stale", kind, routeName, conditionType), nil
			}
			status, _, _ := unstructured.NestedString(condition, "status")
			if status != "True" {
				message, _, _ := unstructured.NestedString(condition, "message")
				if message == "" {
					message = fmt.Sprintf("waiting for %s %s %s condition from Gateway %s/%s", kind, routeName, conditionType, expected.Namespace, expected.Name)
				}
				return false, message, nil
			}
		}
		return true, "", nil
	}
	return false, fmt.Sprintf("waiting for Gateway %s/%s to accept %s %s", expected.Namespace, expected.Name, kind, routeName), nil
}
