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

func tenant(name, namespace, displayName string) *NetEyeTenant {
	return &NetEyeTenant{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: NetEyeTenantSpec{Name: displayName}}
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
	v := tenantValidator(t)
	if _, err := v.ValidateCreate(context.Background(), tenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "Default")); err != nil {
		t.Fatalf("valid tenant rejected: %v", err)
	}
}

func TestNetEyeTenantValidatorRejectsInvalidTenants(t *testing.T) {
	for name, test := range map[string]struct {
		tenant *NetEyeTenant
		want   string
	}{
		"other namespace":    {tenant("neteye-tenant-2bh46bfv", "neteye-tenant-other", "Default"), "must equal metadata.name"},
		"missing prefix":     {tenant("acme", "acme", "Default"), "must start with"},
		"prefix only":        {tenant("neteye-tenant-", "neteye-tenant-", "Default"), "must start with"},
		"shared namespace":   {tenant(NetEyeNamespace, NetEyeNamespace, "Default"), "shared namespace"},
		"blank display name": {tenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "  "), "must not be empty"},
		"surrounding spaces": {tenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", " Default"), "surrounding whitespace"},
		"control character":  {tenant("neteye-tenant-2bh46bfv", "neteye-tenant-2bh46bfv", "De\nfault"), "control characters"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tenantValidator(t).ValidateCreate(context.Background(), test.tenant)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestNetEyeTenantValidatorRequiresAUniqueDisplayName(t *testing.T) {
	existing := tenant("neteye-tenant-aaaaaaaa", "neteye-tenant-aaaaaaaa", "Default")
	v := tenantValidator(t, existing)
	if _, err := v.ValidateCreate(context.Background(), tenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "default")); err == nil || !strings.Contains(err.Error(), "Duplicate") {
		t.Fatalf("duplicate display name accepted: %v", err)
	}
	// Updating a tenant does not conflict with itself.
	if _, err := v.ValidateUpdate(context.Background(), existing, existing); err != nil {
		t.Fatalf("update of an unchanged tenant rejected: %v", err)
	}
	if _, err := v.ValidateCreate(context.Background(), tenant("neteye-tenant-bbbbbbbb", "neteye-tenant-bbbbbbbb", "Second")); err != nil {
		t.Fatalf("distinct display name rejected: %v", err)
	}
}
