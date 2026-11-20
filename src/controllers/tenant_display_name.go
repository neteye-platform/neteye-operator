// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

// tenantDisplayNameLeasePrefix starts the name of the Lease that gives a
// display name to one tenant. The rest of the name is the SHA-256 of the
// display name key, so names that the uniqueness check considers equal share
// one Lease.
const tenantDisplayNameLeasePrefix = "neteye-tenant-display-name-"

func tenantDisplayNameLeaseName(displayName string) string {
	sum := sha256.Sum256([]byte(neteye.TenantDisplayNameKey(displayName)))
	return tenantDisplayNameLeasePrefix + hex.EncodeToString(sum[:])
}

// claimDisplayName gives the display name of the tenant to the tenant. The
// API server creates a Lease only once, so only one tenant can hold a display
// name, also when two writes pass the admission check at the same time. The
// Lease lives in the shared NetEye namespace, where tenant users cannot change
// it. Its holder is the UID of the tenant and its label is the name of the
// tenant. The names that the tenant held before stay held until its namespace
// shows the new name: see releaseDisplayNames.
//
// When another tenant holds the Lease, claimDisplayName returns the name of
// that tenant. A Lease whose tenant is gone, was created again, or uses
// another display name both in its spec and on its namespace is stale: the
// tenant takes it over. The update carries the resource version that was
// read, so two tenants cannot both take over the same Lease.
func (r *NetEyeTenantReconciler) claimDisplayName(ctx context.Context, tenant *neteye.NetEyeTenant) (string, error) {
	key := client.ObjectKey{Namespace: neteye.NetEyeNamespace, Name: tenantDisplayNameLeaseName(tenant.Spec.DisplayName)}
	lease := &coordinationv1.Lease{}
	err := r.apiReader().Get(ctx, key, lease)
	if apierrors.IsNotFound(err) {
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, Labels: map[string]string{neteye.TenantLabel: tenant.Name}},
			Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To(string(tenant.UID))},
		}
		err = r.Create(ctx, lease)
		if err == nil {
			return "", nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("create display name lease %q: %w", key.Name, err)
		}
		err = r.apiReader().Get(ctx, key, lease)
	}
	if err != nil {
		return "", fmt.Errorf("get display name lease %q: %w", key.Name, err)
	}
	if !leaseHeldBy(lease, tenant) {
		holder, err := r.displayNameHolder(ctx, lease)
		if err != nil || holder != "" {
			return holder, err
		}
		lease.Labels = map[string]string{neteye.TenantLabel: tenant.Name}
		lease.Spec.HolderIdentity = ptr.To(string(tenant.UID))
		if err := r.Update(ctx, lease); err != nil {
			return "", fmt.Errorf("take over display name lease %q: %w", key.Name, err)
		}
	}
	return "", nil
}

func leaseHeldBy(lease *coordinationv1.Lease, tenant *neteye.NetEyeTenant) bool {
	return lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == string(tenant.UID) && lease.Labels[neteye.TenantLabel] == tenant.Name
}

// displayNameHolder returns the name of the tenant that still holds lease, or
// an empty name when the lease is stale. A tenant holds the names of its spec
// and of its namespace, so the old name of a renamed tenant stays held until
// its namespace shows the new name. A tenant that is being deleted holds its
// names until it is gone.
func (r *NetEyeTenantReconciler) displayNameHolder(ctx context.Context, lease *coordinationv1.Lease) (string, error) {
	name := lease.Labels[neteye.TenantLabel]
	if name == "" || lease.Spec.HolderIdentity == nil {
		return "", nil
	}
	holder := &neteye.NetEyeTenant{}
	if err := r.apiReader().Get(ctx, client.ObjectKey{Namespace: name, Name: name}, holder); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get NetEyeTenant %q that holds display name lease %q: %w", name, lease.Name, err)
	}
	if string(holder.UID) != *lease.Spec.HolderIdentity {
		return "", nil
	}
	names, err := r.heldDisplayNameLeases(ctx, holder)
	if err != nil {
		return "", err
	}
	if _, held := names[lease.Name]; !held {
		return "", nil
	}
	return name, nil
}

// heldDisplayNameLeases returns the names of the Leases of the display names
// that the tenant shows: the one of its spec, and the one that its namespace
// shows, which is older while the metadata of the namespace is not updated
// yet.
func (r *NetEyeTenantReconciler) heldDisplayNameLeases(ctx context.Context, tenant *neteye.NetEyeTenant) (map[string]struct{}, error) {
	names := map[string]struct{}{tenantDisplayNameLeaseName(tenant.Spec.DisplayName): {}}
	namespace := &corev1.Namespace{}
	if err := r.apiReader().Get(ctx, client.ObjectKey{Name: tenant.Namespace}, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return names, nil
		}
		return nil, fmt.Errorf("get namespace %q of NetEyeTenant %q: %w", tenant.Namespace, tenant.Name, err)
	}
	if namespace.Labels[neteye.TenantLabel] == tenant.Name {
		if shown, ok := namespace.Annotations[neteye.TenantDisplayNameAnnotation]; ok {
			names[tenantDisplayNameLeaseName(shown)] = struct{}{}
		}
	}
	return names, nil
}

// releaseDisplayNames deletes the display name Leases that the tenant holds,
// except the ones in keep. The delete carries the UID and the resource
// version that were read, so a Lease that another tenant took over in the
// meantime stays.
func (r *NetEyeTenantReconciler) releaseDisplayNames(ctx context.Context, tenant *neteye.NetEyeTenant, keep map[string]struct{}) error {
	leases := &coordinationv1.LeaseList{}
	if err := r.apiReader().List(ctx, leases, client.InNamespace(neteye.NetEyeNamespace), client.MatchingLabels{neteye.TenantLabel: tenant.Name}); err != nil {
		return fmt.Errorf("list the display name leases of the tenant: %w", err)
	}
	for i := range leases.Items {
		lease := &leases.Items[i]
		if _, kept := keep[lease.Name]; kept || !strings.HasPrefix(lease.Name, tenantDisplayNameLeasePrefix) || !leaseHeldBy(lease, tenant) {
			continue
		}
		precondition := client.Preconditions{UID: ptr.To(lease.UID), ResourceVersion: ptr.To(lease.ResourceVersion)}
		if err := r.Delete(ctx, lease, precondition); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return fmt.Errorf("delete display name lease %q: %w", lease.Name, err)
		}
	}
	return nil
}
