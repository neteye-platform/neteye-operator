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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOwnerReference(t *testing.T) {
	owner := &unstructured.Unstructured{Object: map[string]any{}}
	owner.SetName("platform")
	owner.SetUID("uid-123")

	ref := OwnerReference("neteye.cloud/v1alpha1", "NetEye", owner)

	if ref.APIVersion != "neteye.cloud/v1alpha1" || ref.Kind != "NetEye" || ref.Name != "platform" || ref.UID != "uid-123" {
		t.Errorf("ref = %+v", ref)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Error("expected Controller = true")
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Error("expected BlockOwnerDeletion = true")
	}
}

func TestSetOwnerReferenceAddsAndIsIdempotent(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	owner := metav1.OwnerReference{APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", UID: "uid-123"}

	changed, err := SetOwnerReference(obj, owner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected the owner reference to be added")
	}
	if len(obj.GetOwnerReferences()) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(obj.GetOwnerReferences()))
	}

	changed, err = SetOwnerReference(obj, owner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no change when the owner reference already exists")
	}
}

func TestSetOwnerReferenceConflict(t *testing.T) {
	controller := true
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetOwnerReferences([]metav1.OwnerReference{
		{APIVersion: "other/v1", Kind: "Other", Name: "x", UID: "uid-other", Controller: &controller},
	})

	changed, err := SetOwnerReference(obj, metav1.OwnerReference{
		APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform", UID: "uid-123",
	})
	if err == nil {
		t.Error("expected a conflict error for a second controller owner")
	}
	if changed {
		t.Error("expected no change on conflict")
	}
}
