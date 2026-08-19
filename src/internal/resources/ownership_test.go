// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

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
