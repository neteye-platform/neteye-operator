// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testTenant(name, namespace, displayName string) *NetEyeTenant {
	return &NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: NetEyeTenantSpec{DisplayName: displayName}}
}

func tenantValidator(t *testing.T, existing ...*NetEyeTenant) *NetEyeTenantValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, other := range existing {
		builder = builder.WithObjects(other)
	}
	return &NetEyeTenantValidator{reader: builder.Build()}
}

func TestNetEyeTenantValidatorAcceptsATenantInItsOwnNamespace(t *testing.T) {
	if _, err := tenantValidator(t).ValidateCreate(context.Background(), testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")); err != nil {
		t.Fatalf("valid tenant rejected: %v", err)
	}
}

func TestNetEyeTenantValidatorRejectsATenantInAnotherNamespace(t *testing.T) {
	_, err := tenantValidator(t).ValidateCreate(context.Background(), testTenant("neteye-tenant-2bh46bfv", "neteye-tenant-aaaaaaaa", "Default"))
	if err == nil || !strings.Contains(err.Error(), "must equal metadata.name") {
		t.Fatalf("error = %v", err)
	}
}

func TestNetEyeTenantValidatorRequiresAUniqueDisplayNameOnCreate(t *testing.T) {
	existing := testTenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Café")
	v := tenantValidator(t, existing)
	for name, displayName := range map[string]string{
		"other case":          "CAFÉ",
		"other normalization": "Café",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v.ValidateCreate(context.Background(), testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", displayName))
			if err == nil || !strings.Contains(err.Error(), "Duplicate") {
				t.Fatalf("display name %q that looks like %q accepted: %v", displayName, existing.Spec.DisplayName, err)
			}
		})
	}
	if _, err := v.ValidateCreate(context.Background(), testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Second")); err != nil {
		t.Fatalf("distinct display name rejected: %v", err)
	}
}

func TestNetEyeTenantValidatorChecksUniquenessOnlyForANewDisplayName(t *testing.T) {
	// Two tenants with the same display name can exist after two concurrent
	// creates. Updates that keep the name, such as the removal of a finalizer,
	// must still pass; a rename to another taken name must not.
	first := testTenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Default")
	second := testTenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Default")
	third := testTenant("neteye-tenant-cccccccc", "neteye-tenant-cccccccc", "Third")
	v := tenantValidator(t, first, second, third)

	if _, err := v.ValidateUpdate(context.Background(), second, second); err != nil {
		t.Errorf("update that keeps the display name rejected: %v", err)
	}
	recased := testTenant(second.Name, second.Namespace, "default")
	if _, err := v.ValidateUpdate(context.Background(), second, recased); err != nil {
		t.Errorf("change of case only rejected: %v", err)
	}
	renamed := testTenant(third.Name, third.Namespace, "DEFAULT")
	if _, err := v.ValidateUpdate(context.Background(), third, renamed); err == nil || !strings.Contains(err.Error(), "Duplicate") {
		t.Errorf("rename to a taken display name accepted: %v", err)
	}
	deleting := renamed.DeepCopy()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	if _, err := v.ValidateUpdate(context.Background(), third, deleting); err != nil {
		t.Errorf("update of a tenant being deleted rejected: %v", err)
	}
}
