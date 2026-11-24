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

// RouteParent is the Gateway listener that a route must attach to.
type RouteParent struct{ Group, Kind, Namespace, Name, Section string }

// RouteReady reports whether the Gateway accepted the route on the expected
// listener and resolved its references. It does not wait; callers requeue.
func RouteReady(ctx context.Context, c client.Client, namespace, routeName, kind string, expected RouteParent) (bool, string, error) {
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
		// An omitted namespace is the namespace of the route.
		parentNamespace, _, _ := unstructured.NestedString(ref, "namespace")
		if parentNamespace == "" {
			parentNamespace = namespace
		}
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

// GatewayListenerProgrammed reports whether the Gateway reports the listener
// programmed for its current generation, so the data plane serves it. A route
// can be accepted before the listener is programmed. It does not wait; callers
// requeue.
func GatewayListenerProgrammed(ctx context.Context, c client.Client, gateway GatewayRef, listener string) (bool, string, error) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gatewayGVK())
	if err := c.Get(ctx, types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}, object); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Gateway %s/%s is not created", gateway.Namespace, gateway.Name), nil
		}
		return false, "", err
	}
	listeners, _, err := unstructured.NestedSlice(object.Object, "status", "listeners")
	if err != nil {
		return false, "", err
	}
	waiting := fmt.Sprintf("waiting for Gateway %s/%s to program listener %s", gateway.Namespace, gateway.Name, listener)
	for _, raw := range listeners {
		status, ok := raw.(map[string]any)
		if !ok || status["name"] != listener {
			continue
		}
		conditions, _, err := unstructured.NestedSlice(status, "conditions")
		if err != nil {
			return false, "", err
		}
		for _, rawCondition := range conditions {
			condition, ok := rawCondition.(map[string]any)
			if !ok || condition["type"] != "Programmed" {
				continue
			}
			observed, found, err := unstructured.NestedInt64(condition, "observedGeneration")
			if err != nil {
				return false, "", err
			}
			if found && observed < object.GetGeneration() {
				return false, waiting, nil
			}
			if condition["status"] != "True" {
				if message, _, _ := unstructured.NestedString(condition, "message"); message != "" {
					return false, message, nil
				}
				return false, waiting, nil
			}
			return true, "", nil
		}
	}
	return false, waiting, nil
}
