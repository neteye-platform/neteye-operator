// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RetainedAnnotation marks an object that the operator deliberately orphaned
// while applying a Retain deletion policy. Orphaning is what keeps the object
// alive once its owning custom resource disappears, but it also strips the only
// durable evidence that the object belongs to a NetEye installation. The marker
// restores that evidence so a recreated NetEye resource can resume managing a
// retained installation instead of failing every ownership check forever.
const RetainedAnnotation = "neteye.cloud/retained"

// RetentionClass declares what happens to an operator-owned resource when the
// custom resource that owns it is deleted.
type RetentionClass int

const (
	// RetainWhenRetaining preserves the resource under a Retain deletion
	// policy. It covers resources that hold installation data and resources a
	// retained workload still needs in order to keep serving.
	RetainWhenRetaining RetentionClass = iota
	// RecreatableOnDelete marks a resource that holds no installation data and
	// is rebuilt from the custom resource, so both deletion policies remove it.
	RecreatableOnDelete
)

// OwnedResource is one entry of a component's explicit deletion inventory.
// ADR-0002 requires such an inventory because owner references alone cannot
// express a retention decision: garbage collection is all-or-nothing and runs
// after the owner is already gone.
type OwnedResource struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	Retention RetentionClass
}

// ControlledBy reports whether owner is the controller owner of object.
func ControlledBy(object client.Object, owner metav1.OwnerReference) bool {
	for _, reference := range object.GetOwnerReferences() {
		if reference.UID == owner.UID && reference.Controller != nil && *reference.Controller {
			return true
		}
	}
	return false
}

// IsRetained reports whether object carries the retained marker.
func IsRetained(object client.Object) bool {
	return object.GetAnnotations()[RetainedAnnotation] == "true"
}

// ReleaseInventory applies a deletion policy to every inventory entry. Entries
// are processed in the order given, so callers pass reverse dependency order.
//
// Every entry is attempted even after a failure, so one stuck resource cannot
// hide the state of the rest, and the joined error keeps the failure visible to
// the caller that owns the finalizer.
func ReleaseInventory(ctx context.Context, c client.Client, owner metav1.OwnerReference, retain bool, inventory []OwnedResource) error {
	var errs []error
	for _, entry := range inventory {
		if retain && entry.Retention == RetainWhenRetaining {
			if err := Orphan(ctx, c, owner, entry); err != nil {
				errs = append(errs, fmt.Errorf("retain %s %s/%s: %w", entry.GVK.Kind, entry.Namespace, entry.Name, err))
			}
			continue
		}
		if err := DeleteOwned(ctx, c, owner, entry); err != nil {
			errs = append(errs, fmt.Errorf("delete %s %s/%s: %w", entry.GVK.Kind, entry.Namespace, entry.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Orphan detaches owner from the resource and records the retained marker,
// leaving the object itself untouched. A missing object needs no work, and an
// object the operator does not control is left alone.
func Orphan(ctx context.Context, c client.Client, owner metav1.OwnerReference, entry OwnedResource) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		live, err := getOwned(ctx, c, entry)
		if err != nil || live == nil {
			return err
		}
		if !ControlledBy(live, owner) {
			return nil
		}
		remaining := make([]metav1.OwnerReference, 0, len(live.GetOwnerReferences()))
		for _, reference := range live.GetOwnerReferences() {
			if reference.UID != owner.UID {
				remaining = append(remaining, reference)
			}
		}
		live.SetOwnerReferences(remaining)
		annotations := live.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[RetainedAnnotation] = "true"
		live.SetAnnotations(annotations)
		return client.IgnoreNotFound(c.Update(ctx, live))
	})
}

// DeleteOwned deletes the resource when the operator controls it. Objects that
// are missing, or that belong to another controller, are left alone.
func DeleteOwned(ctx context.Context, c client.Client, owner metav1.OwnerReference, entry OwnedResource) error {
	live, err := getOwned(ctx, c, entry)
	if err != nil || live == nil {
		return err
	}
	if !ControlledBy(live, owner) {
		return nil
	}
	return client.IgnoreNotFound(c.Delete(ctx, live))
}

// RemainingOwned returns the inventory entries that still exist and are still
// controlled by owner, so a finalizer can report what is holding up cleanup.
func RemainingOwned(ctx context.Context, c client.Client, owner metav1.OwnerReference, inventory []OwnedResource) ([]OwnedResource, error) {
	var remaining []OwnedResource
	for _, entry := range inventory {
		live, err := getOwned(ctx, c, entry)
		if err != nil {
			return nil, err
		}
		if live == nil || !ControlledBy(live, owner) {
			continue
		}
		remaining = append(remaining, entry)
	}
	return remaining, nil
}

// getOwned reads one inventory entry, reporting a missing object as a nil
// object rather than an error.
func getOwned(ctx context.Context, c client.Client, entry OwnedResource) (*unstructured.Unstructured, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(entry.GVK)
	key := types.NamespacedName{Namespace: entry.Namespace, Name: entry.Name}
	if err := c.Get(ctx, key, live); err != nil {
		// A missing CRD is treated as a missing object: a delegated operator's
		// API can already be uninstalled by the time NetEye cleans up, and an
		// absent kind cannot be holding any resource.
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return live, nil
}

// SharedBaseInventory lists the shared ingress resources the NetEye reconciler
// owns directly, in reverse dependency order.
//
// They are retained because a retained installation is only reachable while its
// Gateway and routes exist, and because dropping the namespace-wide default-deny
// policy would widen network access to resources that were deliberately kept.
func SharedBaseInventory(namespace, gatewayName string) []OwnedResource {
	return []OwnedResource{
		{GVK: httpRouteGVK(), Namespace: namespace, Name: HTTPToHTTPSRedirectRouteName, Retention: RetainWhenRetaining},
		{GVK: gatewayGVK(), Namespace: namespace, Name: gatewayName, Retention: RetainWhenRetaining},
		{GVK: ciliumNetworkPolicyGVK(), Namespace: namespace, Name: DefaultDenyPolicyName, Retention: RetainWhenRetaining},
	}
}

// Exported GroupVersionKind accessors let components in other packages declare
// their deletion inventories against the same definitions the apply helpers
// use, so an inventory cannot drift from the resource it is meant to cover.
func HTTPRouteGVK() schema.GroupVersionKind           { return httpRouteGVK() }
func GatewayGVK() schema.GroupVersionKind             { return gatewayGVK() }
func CertificateGVK() schema.GroupVersionKind         { return certificateGVK() }
func CiliumNetworkPolicyGVK() schema.GroupVersionKind { return ciliumNetworkPolicyGVK() }
func NativeNetworkPolicyGVK() schema.GroupVersionKind { return nativeNetworkPolicyGVK() }
