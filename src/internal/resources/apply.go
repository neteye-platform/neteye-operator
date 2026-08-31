// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
// reference drift when it already exists. Newly created objects are labeled as
// managed by the operator. The returned ApplyOutcome lets callers log with
// resource-specific context.
func Apply(ctx context.Context, c client.Client, obj ObjectDefinition) (ApplyOutcome, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GVK)
	key := types.NamespacedName{Name: obj.Name, Namespace: obj.Namespace}

	switch err := c.Get(ctx, key, live); {
	case err == nil:
		return reconcileExisting(ctx, c, key, obj)
	case apierrors.IsNotFound(err):
		return createObject(ctx, c, obj)
	default:
		return Unchanged, err
	}
}

func reconcileExisting(ctx context.Context, c client.Client, key types.NamespacedName, obj ObjectDefinition) (ApplyOutcome, error) {
	outcome := Unchanged
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GVK)
		if err := c.Get(ctx, key, live); err != nil {
			return err
		}

		currentSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
		ownerChanged := false
		if obj.Owner != nil {
			changed, err := SetOwnerReference(live, *obj.Owner)
			if err != nil {
				return err
			}
			ownerChanged = changed
		}

		specChanged := !reflect.DeepEqual(currentSpec, obj.Spec)
		if !specChanged && !ownerChanged {
			return nil
		}
		if specChanged {
			if err := unstructured.SetNestedMap(live.Object, obj.Spec, "spec"); err != nil {
				return err
			}
		}
		if err := c.Update(ctx, live); err != nil {
			return err
		}
		outcome = Updated
		return nil
	})
	return outcome, err
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
	if err := c.Create(ctx, desired); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return Unchanged, err
		}
		key := types.NamespacedName{Name: obj.Name, Namespace: obj.Namespace}
		return reconcileExisting(ctx, c, key, obj)
	}
	return Created, nil
}
