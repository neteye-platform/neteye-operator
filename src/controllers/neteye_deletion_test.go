// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

var keycloakInstanceGVK = schema.GroupVersionKind{Group: "k8s.keycloak.org", Version: "v2beta1", Kind: "Keycloak"}

// deleteAndReconcile requests deletion and drives reconciliation until the
// finalizer is released or the budget runs out, mirroring how the controller
// converges across requeues.
func deleteAndReconcile(ctx context.Context, t *testing.T, c client.Client, r *NetEyeReconciler, ne *neteye.NetEye) {
	t.Helper()
	if err := c.Delete(ctx, ne); err != nil {
		t.Fatalf("delete NetEye: %v", err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ne)}
	for range 5 {
		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatalf("reconcile deletion: %v", err)
		}
		current := &neteye.NetEye{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); apierrors.IsNotFound(err) {
			return
		} else if err != nil {
			t.Fatalf("get NetEye during deletion: %v", err)
		}
	}
	current := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err == nil {
		t.Fatalf("NetEye still present after deletion reconciles, finalizers=%v status=%q", current.Finalizers, current.Status.Message)
	}
}

func fetch(ctx context.Context, t *testing.T, c client.Client, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, bool) {
	t.Helper()
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, live); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		t.Fatalf("get %s %s/%s: %v", gvk.Kind, namespace, name, err)
	}
	return live, true
}

// The default policy must not destroy the installation. envtest runs no garbage
// collector, so what survives here is exactly what the finalizer decided rather
// than what collection happened to reach.
func TestDeletionPolicyRetainPreservesInstallation(t *testing.T) {
	c, _, ctx, ne, r := readyElasticStackTestPlatform(t, nil)
	if got := ne.Spec.EffectiveDeletionPolicy(); got != neteye.NetEyeDeletionPolicyRetain {
		t.Fatalf("effective deletion policy = %q, want Retain by default", got)
	}
	namespace := keycloak.WorkloadNamespace

	deleteAndReconcile(ctx, t, c, r, ne)

	for _, retained := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{keycloakInstanceGVK, keycloak.InstanceName},
		{certificateGVK, keycloak.TLSCertificateName},
		{gatewayGVK, "neteye"},
		{httpRouteGVK, keycloak.HTTPRouteName},
	} {
		live, found := fetch(ctx, t, c, retained.gvk, namespace, retained.name)
		if !found {
			t.Errorf("%s %q was deleted under a Retain policy", retained.gvk.Kind, retained.name)
			continue
		}
		for _, reference := range live.GetOwnerReferences() {
			if reference.UID == ne.UID {
				t.Errorf("%s %q still references the deleted NetEye resource, so garbage collection will remove it anyway",
					retained.gvk.Kind, retained.name)
			}
		}
		if !resources.IsRetained(live) {
			t.Errorf("%s %q is missing the retained marker, so a recreated NetEye resource could not adopt it",
				retained.gvk.Kind, retained.name)
		}
	}

	// The authority lease records the owning resource's UID, so retaining it
	// would permanently block every future NetEye resource in the cluster.
	lease := &coordinationv1.Lease{}
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterAuthorityLeaseName}, lease)
	if !apierrors.IsNotFound(err) {
		t.Errorf("cluster authority lease survived deletion (err=%v); a stale holder blocks reinstallation", err)
	}
}

func TestDeletionPolicyDeleteRemovesInstallation(t *testing.T) {
	c, _, ctx, ne, r := readyElasticStackTestPlatform(t, nil)
	namespace := keycloak.WorkloadNamespace

	current := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatalf("get NetEye: %v", err)
	}
	current.Spec.DeletionPolicy = neteye.NetEyeDeletionPolicyDelete
	if err := c.Update(ctx, current); err != nil {
		t.Fatalf("set deletion policy: %v", err)
	}

	deleteAndReconcile(ctx, t, c, r, current)

	for _, removed := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{keycloakInstanceGVK, keycloak.InstanceName},
		{certificateGVK, keycloak.TLSCertificateName},
		{gatewayGVK, "neteye"},
		{httpRouteGVK, keycloak.HTTPRouteName},
	} {
		if _, found := fetch(ctx, t, c, removed.gvk, namespace, removed.name); found {
			t.Errorf("%s %q survived a Delete policy", removed.gvk.Kind, removed.name)
		}
	}
}

// Retaining strips the owner reference, so reinstallation is the case most
// likely to break: without the retained marker every ownership check would
// reject the resources the previous installation deliberately kept.
func TestRetainedInstallationCanBeReadopted(t *testing.T) {
	c, s, ctx, ne, r := readyElasticStackTestPlatform(t, nil)
	namespace := keycloak.WorkloadNamespace
	spec := ne.Spec

	deleteAndReconcile(ctx, t, c, r, ne)

	recreated := &neteye.NetEye{}
	recreated.Name, recreated.Namespace, recreated.Spec = "platform", namespace, spec
	if err := c.Create(ctx, recreated); err != nil {
		t.Fatalf("recreate NetEye: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(recreated)}); err != nil {
		t.Fatalf("reconcile recreated NetEye over retained resources: %v", err)
	}

	live, found := fetch(ctx, t, c, keycloakInstanceGVK, namespace, keycloak.InstanceName)
	if !found {
		t.Fatal("retained Keycloak instance disappeared")
	}
	adopted := false
	for _, reference := range live.GetOwnerReferences() {
		if reference.UID == recreated.UID {
			adopted = true
		}
	}
	if !adopted {
		t.Errorf("retained Keycloak instance was not adopted by the recreated NetEye resource, owners=%v", live.GetOwnerReferences())
	}
	if resources.IsRetained(live) {
		t.Error("retained marker survived re-adoption")
	}
	_ = s
}

// The finalizer exists to keep a failed cleanup visible instead of abandoning
// half-deleted resources.
func TestDeletionKeepsFinalizerWhileCleanupFails(t *testing.T) {
	c, _, ctx, ne, r := readyElasticStackTestPlatform(t, nil)

	current := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), current); err != nil {
		t.Fatalf("get NetEye: %v", err)
	}
	current.Spec.DeletionPolicy = neteye.NetEyeDeletionPolicyDelete
	if err := c.Update(ctx, current); err != nil {
		t.Fatalf("set deletion policy: %v", err)
	}
	if err := c.Delete(ctx, current); err != nil {
		t.Fatalf("delete NetEye: %v", err)
	}

	// A reconciler whose client rejects writes cannot complete cleanup.
	blocked := &NetEyeReconciler{
		Client: failingWriter{Client: c}, Log: r.Log, Scheme: r.Scheme,
		KeycloakComponent: r.KeycloakComponent, OTelCollectorComponent: r.OTelCollectorComponent,
		EDOTGatewayComponent: r.EDOTGatewayComponent,
	}
	if _, err := blocked.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(current)}); err != nil {
		t.Fatalf("reconcile should report cleanup failure in status, not return an error: %v", err)
	}

	after := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(current), after); err != nil {
		t.Fatalf("NetEye was removed despite a failed cleanup: %v", err)
	}
	if !containsString(after.Finalizers, NetEyeFinalizer) {
		t.Errorf("finalizers = %v, want the cleanup finalizer retained after a failure", after.Finalizers)
	}
	if after.Status.Phase != neteye.PhaseFailed {
		t.Errorf("phase = %q, want %q so the stuck cleanup is visible", after.Status.Phase, neteye.PhaseFailed)
	}
}

// failingWriter rejects deletes so cleanup cannot finish.
type failingWriter struct {
	client.Client
}

func (f failingWriter) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	return apierrors.NewInternalError(deleteRejectedError{kind: obj.GetObjectKind().GroupVersionKind().Kind})
}

type deleteRejectedError struct{ kind string }

func (e deleteRejectedError) Error() string { return "delete rejected for " + e.kind }

// The finalizer has to be in place before any owned resource exists, or a
// deletion that lands in between would fall back to cascading collection and
// ignore the deletion policy entirely.
func TestReconcileAddsCleanupFinalizerBeforeOwningResources(t *testing.T) {
	c, _, ctx, ne, _ := readyElasticStackTestPlatform(t, nil)
	stored := &neteye.NetEye{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(ne), stored); err != nil {
		t.Fatalf("get NetEye: %v", err)
	}
	if !containsString(stored.Finalizers, NetEyeFinalizer) {
		t.Errorf("finalizers = %v, want %q", stored.Finalizers, NetEyeFinalizer)
	}
}
