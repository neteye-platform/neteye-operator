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
	"context"
	"reflect"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureHTTPRoute ensures a Gateway API HTTPRoute exists for a hostname and
// routes traffic to a Service in the same namespace as the route.
func EnsureHTTPRoute(ctx context.Context, client client.Client, log *logr.Logger, namespace string, name, gatewayRef string, hostnames []string, backendServiceName string, backendServicePort int64, owner metav1.OwnerReference) error {
	desiredSpec := map[string]interface{}{
		"parentRefs": []interface{}{
			map[string]interface{}{
				"group":       "gateway.networking.k8s.io",
				"kind":        "Gateway",
				"namespace":   namespace,
				"name":        gatewayRef,
				"sectionName": gatewayHTTPSListenerName,
			},
		},
		"hostnames": stringSliceToInterfaces(hostnames),
		"rules": []interface{}{
			map[string]interface{}{
				"backendRefs": []interface{}{
					map[string]interface{}{
						"group": "",
						"kind":  "Service",
						"name":  backendServiceName,
						"port":  backendServicePort,
					},
				},
			},
		},
	}
	return ensureHTTPRouteSpec(ctx, client, log, namespace, name, desiredSpec, owner)
}

func ensureHTTPRouteSpec(ctx context.Context, client client.Client, log *logr.Logger, namespace string, name string, desiredSpec map[string]interface{}, owner metav1.OwnerReference) error {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(httpRouteGVK())
	key := types.NamespacedName{Name: name, Namespace: namespace}
	if err := client.Get(ctx, key, route); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(route.Object, "spec")
		ownerChanged, err := SetOwnerReference(route, owner)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(currentSpec, desiredSpec) && !ownerChanged {
			log.V(1).Info("HttpRoute had no drift", "namespace", namespace, "name", name)
			return nil
		}
		if !reflect.DeepEqual(currentSpec, desiredSpec) {
			if err := unstructured.SetNestedMap(route.Object, desiredSpec, "spec"); err != nil {
				return err
			}
		}
		if err := client.Update(ctx, route); err != nil {
			return err
		}
		log.V(1).Info("HTTPRoute reconciled", "namespace", namespace, "name", name)
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	route = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if _, err := SetOwnerReference(route, owner); err != nil {
		return err
	}
	if err := client.Create(ctx, route); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	log.Info("HTTPRoute created", "namespace", namespace, "name", name)
	return nil
}

func httpRouteGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "HTTPRoute",
	}
}
