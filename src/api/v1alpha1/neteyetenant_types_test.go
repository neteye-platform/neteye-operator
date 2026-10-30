// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"os"
	"regexp"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// TestGeneratedCRDUsesTheTenantNamePattern keeps the name rule of the CRD and
// TenantNamePattern in agreement. A kubebuilder marker cannot read a Go
// constant, so the pattern is written twice; the API server enforces the CRD
// copy and the controller enforces the constant.
func TestGeneratedCRDUsesTheTenantNamePattern(t *testing.T) {
	data, err := os.ReadFile("../../config/crd/bases/neteye.cloud_neteyetenants.yaml")
	if err != nil {
		t.Fatalf("read generated NetEyeTenant CRD: %v", err)
	}
	crd := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, crd); err != nil {
		t.Fatalf("decode generated NetEyeTenant CRD: %v", err)
	}
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found || len(versions) == 0 {
		t.Fatalf("find versions in generated CRD: found=%t err=%v", found, err)
	}
	version, ok := versions[0].(map[string]interface{})
	if !ok {
		t.Fatalf("generated CRD version has unexpected type %T", versions[0])
	}
	rules, found, err := unstructured.NestedSlice(version, "schema", "openAPIV3Schema", "x-kubernetes-validations")
	if err != nil || !found {
		t.Fatalf("find validation rules in generated CRD: found=%t err=%v", found, err)
	}
	want := "self.metadata.name.matches('" + TenantNamePattern + "')"
	for _, rule := range rules {
		if entry, ok := rule.(map[string]interface{}); ok && entry["rule"] == want {
			return
		}
	}
	t.Errorf("generated CRD has no rule %q; validation rules: %v", want, rules)
}

func TestTenantNamePatternAcceptsOnlyTheInstallerFormat(t *testing.T) {
	pattern := regexp.MustCompile(TenantNamePattern)
	for name, valid := range map[string]bool{
		"neteye-tenant-2bh46bfv":  true,
		"neteye-tenant-abcdefgh":  true,
		"neteye-tenant-23456789":  true,
		"neteye-tenant-shared":    false,
		"neteye-tenant-2bh46bf":   false,
		"neteye-tenant-2bh46bfvx": false,
		"neteye-tenant-2bh46bf0":  false,
		"neteye-tenant-2bh46bfo":  false,
		"neteye-tenant-2bh46bf1":  false,
		"neteye-tenant-2bh46bfl":  false,
		"neteye-tenant-2bh46bfi":  false,
		"neteye-tenant-2BH46BFV":  false,
		"other-prefix-2bh46bfv":   false,
	} {
		if got := pattern.MatchString(name); got != valid {
			t.Errorf("%q: match = %t, want %t", name, got, valid)
		}
	}
}
