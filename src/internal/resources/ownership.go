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
