// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func retentionOwner() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "neteye.cloud/v1alpha1", Kind: "NetEye", Name: "platform",
		UID:        types.UID("11111111-1111-1111-1111-111111111111"),
		Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
	}
}

func ownedWidget(t *testing.T, name string, owners ...metav1.OwnerReference) *unstructured.Unstructured {
	t.Helper()
	object := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"image": "a"}}}
	object.SetGroupVersionKind(testGVK)
	object.SetNamespace("neteye-tenant-shared")
	object.SetName(name)
	object.SetOwnerReferences(owners)
	return object
}

func widgetEntry(name string, retention RetentionClass) OwnedResource {
	return OwnedResource{GVK: testGVK, Namespace: "neteye-tenant-shared", Name: name, Retention: retention}
}

func getWidget(t *testing.T, c client.Client, name string) (*unstructured.Unstructured, bool) {
	t.Helper()
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(testGVK)
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "neteye-tenant-shared", Name: name}, live)
	if err != nil {
		return nil, false
	}
	return live, true
}

// Retain must leave data-bearing resources in place but detached, because an
// owner reference that outlives its owner is exactly what triggers the
// cascading deletion the policy exists to prevent.
func TestReleaseInventoryRetainOrphansAndMarks(t *testing.T) {
	owner := retentionOwner()
	c := applyClient(t, ownedWidget(t, "retained", owner), ownedWidget(t, "recreatable", owner))

	inventory := []OwnedResource{
		widgetEntry("retained", RetainWhenRetaining),
		widgetEntry("recreatable", RecreatableOnDelete),
	}
	if err := ReleaseInventory(context.Background(), c, owner, true, inventory); err != nil {
		t.Fatalf("ReleaseInventory() error = %v", err)
	}

	retained, found := getWidget(t, c, "retained")
	if !found {
		t.Fatal("retained resource was deleted under a Retain policy")
	}
	if len(retained.GetOwnerReferences()) != 0 {
		t.Errorf("owner references = %v, want none so garbage collection cannot remove it", retained.GetOwnerReferences())
	}
	if !IsRetained(retained) {
		t.Error("retained resource is missing the retained marker")
	}
	if _, found := getWidget(t, c, "recreatable"); found {
		t.Error("a resource with no installation data survived a Retain policy")
	}
}

func TestReleaseInventoryDeleteRemovesEverythingOwned(t *testing.T) {
	owner := retentionOwner()
	c := applyClient(t, ownedWidget(t, "retained", owner), ownedWidget(t, "recreatable", owner))

	inventory := []OwnedResource{
		widgetEntry("retained", RetainWhenRetaining),
		widgetEntry("recreatable", RecreatableOnDelete),
	}
	if err := ReleaseInventory(context.Background(), c, owner, false, inventory); err != nil {
		t.Fatalf("ReleaseInventory() error = %v", err)
	}
	for _, name := range []string{"retained", "recreatable"} {
		if _, found := getWidget(t, c, name); found {
			t.Errorf("%s survived a Delete policy", name)
		}
	}
}

// A resource owned by somebody else is not ours to retain or delete.
func TestReleaseInventoryIgnoresForeignResources(t *testing.T) {
	owner := retentionOwner()
	foreign := metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "other",
		UID: types.UID("22222222-2222-2222-2222-222222222222"), Controller: ptr.To(true),
	}
	c := applyClient(t, ownedWidget(t, "foreign", foreign))

	inventory := []OwnedResource{widgetEntry("foreign", RecreatableOnDelete)}
	if err := ReleaseInventory(context.Background(), c, owner, false, inventory); err != nil {
		t.Fatalf("ReleaseInventory() error = %v", err)
	}
	live, found := getWidget(t, c, "foreign")
	if !found {
		t.Fatal("a resource controlled by another owner was deleted")
	}
	if len(live.GetOwnerReferences()) != 1 {
		t.Errorf("owner references = %v, want the foreign owner left intact", live.GetOwnerReferences())
	}
}

// Missing resources are not an error: cleanup has to be retryable, and the
// second pass of a partially completed cleanup sees exactly this state.
func TestReleaseInventoryToleratesMissingResources(t *testing.T) {
	owner := retentionOwner()
	c := applyClient(t)
	inventory := []OwnedResource{widgetEntry("gone", RetainWhenRetaining), widgetEntry("also-gone", RecreatableOnDelete)}
	if err := ReleaseInventory(context.Background(), c, owner, true, inventory); err != nil {
		t.Fatalf("ReleaseInventory() error = %v", err)
	}
}

func TestRemainingOwnedReportsOnlyOwnedSurvivors(t *testing.T) {
	owner := retentionOwner()
	foreign := metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "other",
		UID: types.UID("22222222-2222-2222-2222-222222222222"), Controller: ptr.To(true),
	}
	c := applyClient(t, ownedWidget(t, "mine", owner), ownedWidget(t, "theirs", foreign))

	remaining, err := RemainingOwned(context.Background(), c, owner, []OwnedResource{
		widgetEntry("mine", RecreatableOnDelete),
		widgetEntry("theirs", RecreatableOnDelete),
		widgetEntry("absent", RecreatableOnDelete),
	})
	if err != nil {
		t.Fatalf("RemainingOwned() error = %v", err)
	}
	if len(remaining) != 1 || remaining[0].Name != "mine" {
		t.Errorf("remaining = %v, want only the resource this owner controls", remaining)
	}
}

// Retaining strips the only durable proof of ownership, so a recreated NetEye
// resource has to be able to adopt a retained installation back. Without this
// the Retain policy would permanently break reinstallation.
func TestRetainedResourceIsReadoptedAndMarkerCleared(t *testing.T) {
	owner := retentionOwner()
	c := applyClient(t, ownedWidget(t, "retained", owner))
	entry := widgetEntry("retained", RetainWhenRetaining)

	if err := ReleaseInventory(context.Background(), c, owner, true, []OwnedResource{entry}); err != nil {
		t.Fatalf("ReleaseInventory() error = %v", err)
	}

	// A new NetEye resource has a different UID.
	newOwner := retentionOwner()
	newOwner.UID = types.UID("33333333-3333-3333-3333-333333333333")
	outcome, err := Apply(context.Background(), c, ObjectDefinition{
		GVK: testGVK, Namespace: "neteye-tenant-shared", Name: "retained",
		Spec: map[string]any{"image": "b"}, Owner: &newOwner,
	})
	if err != nil {
		t.Fatalf("Apply() on a retained resource error = %v", err)
	}
	if outcome != Updated {
		t.Errorf("outcome = %v, want Updated", outcome)
	}

	live, found := getWidget(t, c, "retained")
	if !found {
		t.Fatal("retained resource disappeared")
	}
	if !ControlledBy(live, newOwner) {
		t.Errorf("owner references = %v, want the new NetEye resource to control it", live.GetOwnerReferences())
	}
	if IsRetained(live) {
		t.Error("retained marker survived re-adoption, which would keep widening the adoption path")
	}
}

// Everything that is neither owned nor retained must still fail safe.
func TestUnownedResourceIsStillNotAdopted(t *testing.T) {
	owner := retentionOwner()
	c := applyClient(t, ownedWidget(t, "stranger"))
	_, err := Apply(context.Background(), c, ObjectDefinition{
		GVK: testGVK, Namespace: "neteye-tenant-shared", Name: "stranger",
		Spec: map[string]any{"image": "b"}, Owner: &owner,
	})
	if err == nil {
		t.Fatal("an unowned, unretained resource was adopted")
	}
}
