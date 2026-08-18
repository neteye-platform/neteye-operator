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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "neteye-operator"
)

// ApplyOutcome reports what Apply did while reconciling an object.
type ApplyOutcome int

const (
	// Unchanged means the live object already matched the desired state.
	Unchanged ApplyOutcome = iota
	// Updated means the live object's spec or owner reference was reconciled.
	Updated
	// Created means the object did not exist and was created.
	Created
)

// ObjectDefinition describes a desired operator-managed object. Objects are
// applied as unstructured resources so the operator does not need typed clients
// for the third-party CRDs it manages (cert-manager, Gateway API, Keycloak, OLM).
type ObjectDefinition struct {
	GVK  schema.GroupVersionKind
	Name string
	// Namespace is empty for cluster-scoped objects.
	Namespace string
	Spec      map[string]any
	// Owner is nil for cluster-scoped or otherwise unowned objects.
	Owner *metav1.OwnerReference
}

// Apply creates the object when it is missing, or reconciles spec and owner
// reference drift when it already exists. Newly created objects are labelled as
// managed by the operator. The returned ApplyOutcome lets callers log with
// resource-specific context.
func Apply(ctx context.Context, c client.Client, obj ObjectDefinition) (ApplyOutcome, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GVK)
	key := types.NamespacedName{Name: obj.Name, Namespace: obj.Namespace}

	switch err := c.Get(ctx, key, live); {
	case err == nil:
		return reconcileExisting(ctx, c, live, obj)
	case apierrors.IsNotFound(err):
		return createObject(ctx, c, obj)
	default:
		return Unchanged, err
	}
}

func reconcileExisting(ctx context.Context, c client.Client, live *unstructured.Unstructured, obj ObjectDefinition) (ApplyOutcome, error) {
	currentSpec, _, _ := unstructured.NestedMap(live.Object, "spec")

	ownerChanged := false
	if obj.Owner != nil {
		changed, err := SetOwnerReference(live, *obj.Owner)
		if err != nil {
			return Unchanged, err
		}
		ownerChanged = changed
	}

	specChanged := !reflect.DeepEqual(currentSpec, obj.Spec)
	if !specChanged && !ownerChanged {
		return Unchanged, nil
	}
	if specChanged {
		if err := unstructured.SetNestedMap(live.Object, obj.Spec, "spec"); err != nil {
			return Unchanged, err
		}
	}
	if err := c.Update(ctx, live); err != nil {
		return Unchanged, err
	}
	return Updated, nil
}

func createObject(ctx context.Context, c client.Client, obj ObjectDefinition) (ApplyOutcome, error) {
	desired := &unstructured.Unstructured{Object: map[string]any{}}
	desired.SetGroupVersionKind(obj.GVK)
	desired.SetName(obj.Name)
	if obj.Namespace != "" {
		desired.SetNamespace(obj.Namespace)
	}
	desired.SetLabels(map[string]string{managedByLabel: managedByValue})
	if err := unstructured.SetNestedMap(desired.Object, obj.Spec, "spec"); err != nil {
		return Unchanged, err
	}
	if obj.Owner != nil {
		if _, err := SetOwnerReference(desired, *obj.Owner); err != nil {
			return Unchanged, err
		}
	}
	if err := c.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
		return Unchanged, err
	}
	return Created, nil
}
