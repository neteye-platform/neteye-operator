// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

const otherTenant = "neteye-tenant-aaaaaaaa"

func tenantNamed(name, uid, displayName string) *neteye.NetEyeTenant {
	tenant := tenantResource()
	tenant.Name, tenant.Namespace, tenant.UID = name, name, types.UID(uid)
	tenant.Spec.DisplayName = displayName
	return tenant
}

func displayNameLease(t *testing.T, c client.Client, displayName string) *coordinationv1.Lease {
	t.Helper()
	lease := &coordinationv1.Lease{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: neteye.NetEyeNamespace, Name: tenantDisplayNameLeaseName(displayName)}, lease); err != nil {
		t.Fatalf("display name lease of %q: %v", displayName, err)
	}
	return lease
}

func assertNoDisplayNameLease(t *testing.T, c client.Client, displayName string) {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: neteye.NetEyeNamespace, Name: tenantDisplayNameLeaseName(displayName)}, &coordinationv1.Lease{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("display name lease of %q: error = %v, want not found", displayName, err)
	}
}

func TestNetEyeTenantAtThePreviousVersionWaitsForItsUpgrade(t *testing.T) {
	tenant := tenantResource()
	tenant.Spec.Version = neteye.PreviousNetEyeVersion
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenant)
	reconcileTenant(t, r)

	assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhasePendingUpgrades, "consider upgrading to '"+neteye.CurrentNetEyeVersion+"'")
	if labels := getNamespace(t, c, testTenant).Labels; labels[neteye.TenantLabel] != "" {
		t.Error("a tenant that waits for its upgrade changed its namespace")
	}
}

func TestNetEyeTenantRefusesVersionsThatTheOperatorDoesNotHandle(t *testing.T) {
	for name, test := range map[string]struct {
		version, currentVersion, want string
	}{
		"unknown version":   {"99.99", "", "is not the latest"},
		"skipped upgrade":   {neteye.CurrentNetEyeVersion, "4.48", "only upgrades from"},
		"missing version":   {"", "", "is not the latest"},
		"downgraded record": {neteye.CurrentNetEyeVersion, "99.99", "only upgrades from"},
	} {
		t.Run(name, func(t *testing.T) {
			tenant := tenantResource()
			tenant.Spec.Version = test.version
			tenant.Status.CurrentVersion = test.currentVersion
			r, c := newTenantReconciler(t, plainNamespace(testTenant), tenant)
			reconcileTenant(t, r)

			assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseFailed, test.want)
			if labels := getNamespace(t, c, testTenant).Labels; labels[neteye.TenantLabel] != "" {
				t.Error("a tenant with a version that the operator does not handle changed its namespace")
			}
		})
	}
}

func TestNetEyeTenantRecordsTheUpgradeOnceReady(t *testing.T) {
	tenant := tenantResource()
	tenant.Status.CurrentVersion = neteye.PreviousNetEyeVersion
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenant)
	reconcileTenantUntilReady(t, r, c)

	stored := getTenant(t, c, testTenant, testTenant)
	assertTenantPhase(t, stored, neteye.PhaseReady, "All components are ready")
	if stored.Status.CurrentVersion != neteye.CurrentNetEyeVersion {
		t.Errorf("current version = %q, want %q", stored.Status.CurrentVersion, neteye.CurrentNetEyeVersion)
	}
}

func TestNetEyeTenantHonorsItsProcedureGates(t *testing.T) {
	plans := map[string]tenantReleasePlan{neteye.CurrentNetEyeVersion: {
		Install: tenantGatePlan{tenantNamespaceComponentID: {Requires: []string{"tenant-prepared"}, Produces: []string{"tenant-namespace-ready"}}},
	}}

	t.Run("waits for a required gate", func(t *testing.T) {
		r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
		r.releasePlans = plans
		reconcileTenant(t, r)

		stored := getTenant(t, c, testTenant, testTenant)
		assertTenantPhase(t, stored, neteye.PhaseInstalling, "waiting for NetEye "+neteye.CurrentNetEyeVersion+" procedure gates: tenant-prepared")
		if stored.Status.CurrentVersion != "" {
			t.Errorf("current version = %q while a gate is missing", stored.Status.CurrentVersion)
		}
		if labels := getNamespace(t, c, testTenant).Labels; labels[neteye.TenantLabel] != "" {
			t.Error("the namespace was changed before its gate exists")
		}

		createProcedureGate(context.Background(), t, c, testTenant, "tenant-prepared")
		reconcileTenantUntilReady(t, r, c)
		stored = getTenant(t, c, testTenant, testTenant)
		assertTenantPhase(t, stored, neteye.PhaseReady, "All components are ready")
		if stored.Status.CurrentVersion != neteye.CurrentNetEyeVersion {
			t.Errorf("current version = %q after the install", stored.Status.CurrentVersion)
		}
		gate := &neteye.NetEyeProcedureGate{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: testTenant, Name: procedureGateName(neteye.CurrentNetEyeVersion, "tenant-namespace-ready")}, gate); err != nil {
			t.Fatalf("produced gate: %v", err)
		}
		if owners := gate.OwnerReferences; len(owners) != 1 || owners[0].Kind != "NetEyeTenant" || owners[0].UID != "tenant-uid" || gate.Spec.Producer != neteye.ProcedureGateProducerOperator {
			t.Errorf("produced gate = %#v, want one of the operator owned by the tenant", gate)
		}
	})

	t.Run("ignores the gates when disabled", func(t *testing.T) {
		tenant := tenantResource()
		tenant.Spec.UseProcedureGates = ptr.To(false)
		r, c := newTenantReconciler(t, plainNamespace(testTenant), tenant)
		r.releasePlans = plans
		reconcileTenantUntilReady(t, r, c)

		assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseReady, "All components are ready")
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: testTenant, Name: procedureGateName(neteye.CurrentNetEyeVersion, "tenant-namespace-ready")}, &neteye.NetEyeProcedureGate{}); !apierrors.IsNotFound(err) {
			t.Errorf("gate created while procedure gates are disabled: %v", err)
		}
	})

	t.Run("ignores the gates once installed", func(t *testing.T) {
		tenant := tenantResource()
		tenant.Status.CurrentVersion = neteye.CurrentNetEyeVersion
		r, c := newTenantReconciler(t, plainNamespace(testTenant), tenant)
		r.releasePlans = plans
		reconcileTenantUntilReady(t, r, c)

		assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseReady, "All components are ready")
	})
}

func TestTenantForProcedureGateMapsTheTenantOfTheNamespace(t *testing.T) {
	gate := &neteye.NetEyeProcedureGate{ObjectMeta: metav1.ObjectMeta{Namespace: testTenant, Name: "4.51-gate"}}
	if requests := tenantForProcedureGate(context.Background(), gate); len(requests) != 1 || requests[0].Namespace != testTenant || requests[0].Name != testTenant {
		t.Errorf("requests for a gate in a tenant namespace = %v", requests)
	}
	gate.Namespace = neteye.NetEyeNamespace
	if requests := tenantForProcedureGate(context.Background(), gate); len(requests) != 0 {
		t.Errorf("requests for a gate in the shared namespace = %v", requests)
	}
}

func TestNetEyeTenantDisplayNameGoesToOneTenantOnly(t *testing.T) {
	// Both tenants passed admission at the same time with the same name. The
	// first one that the controller reconciles keeps it.
	first := tenantResource()
	second := tenantNamed(otherTenant, "other-uid", "DEFAULT")
	r, c := newTenantReconciler(t, plainNamespace(testTenant), plainNamespace(otherTenant), first, second)
	reconcileTenantUntilReady(t, r, c)
	reconcileTenantRequest(t, r, otherTenant, otherTenant)

	assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseReady, "")
	assertTenantPhase(t, getTenant(t, c, otherTenant, otherTenant), neteye.PhaseFailed, `display name "DEFAULT" is already used by tenant `+testTenant)
	if labels := getNamespace(t, c, otherTenant).Labels; labels[neteye.TenantLabel] != "" {
		t.Error("the tenant without the display name changed its namespace")
	}
	if lease := displayNameLease(t, c, "Default"); *lease.Spec.HolderIdentity != "tenant-uid" || lease.Labels[neteye.TenantLabel] != testTenant {
		t.Errorf("display name lease = %#v, want it held by the first tenant", lease)
	}

	// Once the second tenant chooses another name, it continues.
	stored := getTenant(t, c, otherTenant, otherTenant)
	stored.Spec.DisplayName = "Second"
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	reconcileNamedTenantUntilReady(t, r, c, otherTenant)
	assertTenantPhase(t, getTenant(t, c, otherTenant, otherTenant), neteye.PhaseReady, "")
}

func TestNetEyeTenantReleasesItsOldDisplayName(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)
	tenant := getTenant(t, c, testTenant, testTenant)
	tenant.Spec.DisplayName = "Renamed"
	if err := c.Update(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)

	displayNameLease(t, c, "Renamed")
	assertNoDisplayNameLease(t, c, "Default")
}

func TestNetEyeTenantTakesOverAStaleDisplayName(t *testing.T) {
	for name, holder := range map[string]client.Object{
		"tenant gone":           nil,
		"tenant created again":  tenantNamed(otherTenant, "new-uid", "Default"),
		"tenant renamed itself": tenantNamed(otherTenant, "other-uid", "Other"),
	} {
		t.Run(name, func(t *testing.T) {
			stale := &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Namespace: neteye.NetEyeNamespace, Name: tenantDisplayNameLeaseName("Default"), Labels: map[string]string{neteye.TenantLabel: otherTenant}},
				Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To("other-uid")},
			}
			objects := []client.Object{plainNamespace(testTenant), tenantResource(), stale}
			if holder != nil {
				objects = append(objects, holder)
			}
			r, c := newTenantReconciler(t, objects...)
			reconcileTenantUntilReady(t, r, c)

			assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseReady, "")
			if lease := displayNameLease(t, c, "Default"); *lease.Spec.HolderIdentity != "tenant-uid" || lease.Labels[neteye.TenantLabel] != testTenant {
				t.Errorf("display name lease = %#v, want it taken over", lease)
			}
		})
	}
}

func TestNetEyeTenantBeingDeletedKeepsItsDisplayName(t *testing.T) {
	holder := tenantNamed(otherTenant, "other-uid", "Default")
	now := metav1.Now()
	holder.DeletionTimestamp = &now
	holder.Finalizers = []string{NetEyeTenantFinalizer}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: neteye.NetEyeNamespace, Name: tenantDisplayNameLeaseName("Default"), Labels: map[string]string{neteye.TenantLabel: otherTenant}},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To("other-uid")},
	}
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource(), holder, lease)
	reconcileTenant(t, r)
	assertTenantPhase(t, getTenant(t, c, testTenant, testTenant), neteye.PhaseFailed, "is already used by tenant "+otherTenant)
}

func TestNetEyeTenantDeletionReleasesItsDisplayName(t *testing.T) {
	r, c := newTenantReconciler(t, plainNamespace(testTenant), tenantResource())
	reconcileTenant(t, r)
	displayNameLease(t, c, "Default")
	if err := c.Delete(context.Background(), getTenant(t, c, testTenant, testTenant)); err != nil {
		t.Fatal(err)
	}
	reconcileTenant(t, r)
	assertNoDisplayNameLease(t, c, "Default")
}

func TestTenantDisplayNameLeaseNameFollowsTheUniquenessKey(t *testing.T) {
	if tenantDisplayNameLeaseName("Café") != tenantDisplayNameLeaseName("CAFÉ") {
		t.Error("display names that the uniqueness check considers equal have different leases")
	}
	if tenantDisplayNameLeaseName("Acme") == tenantDisplayNameLeaseName("Acne") {
		t.Error("different display names share a lease")
	}
}
