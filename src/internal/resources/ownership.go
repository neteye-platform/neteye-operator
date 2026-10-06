// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OwnerReference builds a controller owner reference for a child resource owned
// by the provided Kubernetes object.
func OwnerReference(apiVersion, kind string, owner client.Object) metav1.OwnerReference {
	controller := true
	blockOwnerDeletion := true
	return metav1.OwnerReference{
		APIVersion:         apiVersion,
		Kind:               kind,
		Name:               owner.GetName(),
		UID:                owner.GetUID(),
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
}

// SetOwnerReference adds the owner reference when it is not already present. It
// returns true when the object changed. If the object is already controlled by a
// different owner, it returns an explicit conflict instead of asking the API
// server to reject an invalid second controller reference.
func SetOwnerReference(object *unstructured.Unstructured, owner metav1.OwnerReference) (bool, error) {
	for _, existing := range object.GetOwnerReferences() {
		if existing.UID == owner.UID {
			return false, nil
		}
		if existing.Controller != nil && *existing.Controller {
			return false, fmt.Errorf(
				"%s %s/%s is already controlled by %s %s/%s",
				object.GetKind(), object.GetNamespace(), object.GetName(), existing.Kind, existing.APIVersion, existing.Name,
			)
		}
	}
	object.SetOwnerReferences(append(object.GetOwnerReferences(), owner))
	return true, nil
}

// RequireManagedOwner accepts only an existing object controlled by owner, or
// one the operator itself retained. Create paths intentionally do not call it
// because a new object has no owner references until the desired controller
// owner is attached.
func RequireManagedOwner(object *unstructured.Unstructured, owner metav1.OwnerReference) error {
	for _, existing := range object.GetOwnerReferences() {
		if existing.UID == owner.UID && existing.Controller != nil && *existing.Controller {
			return nil
		}
		if existing.Controller != nil && *existing.Controller {
			return fmt.Errorf("%s %s/%s is already controlled by %s %s/%s", object.GetKind(), object.GetNamespace(), object.GetName(), existing.Kind, existing.APIVersion, existing.Name)
		}
	}
	// A resource the operator orphaned under a Retain deletion policy has no
	// controller owner left, so the loop above cannot recognize it. Its
	// retained marker is the operator's own record that the object belongs to a
	// NetEye installation, which is what lets a recreated NetEye resource
	// resume managing it. Every other unowned object still fails safe.
	if IsRetained(object) {
		return nil
	}
	return fmt.Errorf("%s %s/%s is not controlled by the expected owner and cannot be adopted", object.GetKind(), object.GetNamespace(), object.GetName())
}

// ClearRetainedMarker drops the retained marker once the object is owned again.
// It returns true when the object changed. Keeping a stale marker would widen
// the re-adoption path above to objects that are no longer retained.
func ClearRetainedMarker(object *unstructured.Unstructured) bool {
	annotations := object.GetAnnotations()
	if _, present := annotations[RetainedAnnotation]; !present {
		return false
	}
	delete(annotations, RetainedAnnotation)
	object.SetAnnotations(annotations)
	return true
}
