// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// NetEyeFinalizer keeps the NetEye resource present until the operator has
// applied its deletion policy. Without it, Kubernetes garbage collection would
// remove every owned resource as soon as the resource disappeared, which is
// Delete behavior and would make a Retain policy unimplementable.
const NetEyeFinalizer = "neteye.cloud/neteye-cleanup"

// leaseGVK is declared here because the cluster-authority Lease is owned by the
// reconciler itself rather than by a component.
var leaseGVK = schema.GroupVersionKind{Group: "coordination.k8s.io", Version: "v1", Kind: "Lease"}

// deletionInventory is the complete set of resources this NetEye installation
// owns, in reverse dependency order: components first, then the shared ingress
// resources they depend on.
//
// The cluster-authority Lease is deliberately recreatable under both policies.
// It records the UID of the NetEye resource that owns the shared components, so
// orphaning it would leave a lease held by a resource that no longer exists and
// permanently reject every future NetEye resource in the cluster.
func (r *NetEyeReconciler) deletionInventory(ne *neteye.NetEye) []resources.OwnedResource {
	namespace := keycloak.WorkloadNamespace
	inventory := make([]resources.OwnedResource, 0, 24)
	if r.OTelCollectorComponent != nil {
		inventory = append(inventory, r.OTelCollectorComponent.OwnedResources(namespace)...)
	}
	if r.EDOTGatewayComponent != nil {
		inventory = append(inventory, r.EDOTGatewayComponent.OwnedResources(namespace)...)
	}
	if r.KeycloakComponent != nil {
		inventory = append(inventory, r.KeycloakComponent.OwnedResources(namespace)...)
	}
	inventory = append(inventory, resources.SharedBaseInventory(namespace, ne.Spec.Gateway.Name)...)
	inventory = append(inventory, resources.OwnedResource{
		GVK: leaseGVK, Namespace: namespace, Name: clusterAuthorityLeaseName,
		Retention: resources.RecreatableOnDelete,
	})
	return inventory
}

// reconcileDelete applies the deletion policy and releases the finalizer only
// once cleanup has completed. A failed cleanup keeps the finalizer in place and
// stays visible in status rather than abandoning partially deleted resources.
func (r *NetEyeReconciler) reconcileDelete(ctx context.Context, ne *neteye.NetEye) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if !controllerutil.ContainsFinalizer(ne, NetEyeFinalizer) {
		return ctrl.Result{}, nil
	}

	policy := ne.Spec.EffectiveDeletionPolicy()
	retain := policy == neteye.NetEyeDeletionPolicyRetain
	owner := ownerReferenceFor(ne)
	inventory := r.deletionInventory(ne)
	log.Info("Applying NetEye deletion policy", "policy", policy, "resources", len(inventory))

	if err := resources.ReleaseInventory(ctx, r.Client, owner, retain, inventory); err != nil {
		message := fmt.Sprintf("deletion policy %s did not complete: %v", policy, err)
		log.Error(err, "NetEye cleanup failed; keeping the finalizer", "policy", policy, "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, message)
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}

	// Under Delete the resources are only requested to go; confirm they are
	// actually gone before releasing the finalizer, so a stuck dependent
	// cannot be abandoned half-deleted.
	remaining, err := resources.RemainingOwned(ctx, r.Client, owner, deletableEntries(inventory, retain))
	if err != nil {
		log.Error(err, "unable to confirm NetEye cleanup", "requeueAfter", r.failureRequeue())
		setPhase(ne, neteye.PhaseFailed, fmt.Sprintf("unable to confirm deletion policy %s: %v", policy, err))
		return ctrl.Result{RequeueAfter: r.failureRequeue()}, nil
	}
	if len(remaining) > 0 {
		message := fmt.Sprintf("deletion policy %s is waiting for %s", policy, describeResources(remaining))
		log.V(1).Info("NetEye cleanup still in progress", "remaining", len(remaining), "requeueAfter", r.waitForProgressingRequeue())
		setPhase(ne, neteye.PhaseNotReady, message)
		return ctrl.Result{RequeueAfter: r.waitForProgressingRequeue()}, nil
	}

	if r.AdminProviders != nil {
		r.AdminProviders.Forget(ne.Namespace)
	}
	if err := r.removeFinalizer(ctx, ne); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove NetEye finalizer: %w", err)
	}
	log.Info("NetEye cleanup completed", "policy", policy)
	return ctrl.Result{}, nil
}

// deletableEntries returns the inventory entries the active policy deletes, so
// retained resources are not mistaken for incomplete cleanup.
func deletableEntries(inventory []resources.OwnedResource, retain bool) []resources.OwnedResource {
	if !retain {
		return inventory
	}
	deletable := make([]resources.OwnedResource, 0, len(inventory))
	for _, entry := range inventory {
		if entry.Retention != resources.RetainWhenRetaining {
			deletable = append(deletable, entry)
		}
	}
	return deletable
}

func describeResources(entries []resources.OwnedResource) string {
	described := make([]string, 0, len(entries))
	for _, entry := range entries {
		described = append(described, fmt.Sprintf("%s %s/%s", entry.GVK.Kind, entry.Namespace, entry.Name))
	}
	sort.Strings(described)
	if len(described) > 3 {
		return fmt.Sprintf("%s and %d more", strings.Join(described[:3], ", "), len(described)-3)
	}
	return strings.Join(described, ", ")
}

// ensureFinalizer adds the cleanup finalizer before any resource is created,
// so there is no window in which owned resources exist without the finalizer
// that implements their deletion policy.
func (r *NetEyeReconciler) ensureFinalizer(ctx context.Context, ne *neteye.NetEye) error {
	if controllerutil.ContainsFinalizer(ne, NetEyeFinalizer) {
		return nil
	}
	patch := client.MergeFrom(ne.DeepCopy())
	controllerutil.AddFinalizer(ne, NetEyeFinalizer)
	return r.Patch(ctx, ne, patch)
}

func (r *NetEyeReconciler) removeFinalizer(ctx context.Context, ne *neteye.NetEye) error {
	patch := client.MergeFrom(ne.DeepCopy())
	controllerutil.RemoveFinalizer(ne, NetEyeFinalizer)
	return r.Patch(ctx, ne, patch)
}
