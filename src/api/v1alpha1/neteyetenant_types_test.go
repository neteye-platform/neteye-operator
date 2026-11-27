// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"os"
	"regexp"
	"strings"
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

func TestNetEyeTenantAlyvixEnabledAndEffectiveRDPPorts(t *testing.T) {
	var spec *NetEyeTenantSpec
	if spec.AlyvixEnabled() {
		t.Error("a nil spec enables Alyvix")
	}
	for name, alyvix := range map[string]*NetEyeAlyvixSpec{"no alyvix": nil, "disabled": {}, "disabled with RDP ports": {RDP: &NetEyeAlyvixRDPSpec{Ports: []int32{3390}}}} {
		if (&NetEyeTenantSpec{Alyvix: alyvix}).AlyvixEnabled() {
			t.Errorf("%s: Alyvix enabled", name)
		}
	}
	enabled := &NetEyeTenantSpec{Alyvix: &NetEyeAlyvixSpec{Enabled: true}}
	if !enabled.AlyvixEnabled() {
		t.Error("Alyvix not enabled")
	}
	if got := enabled.Alyvix.EffectiveRDPPorts(); len(got) != 1 || got[0] != DefaultAlyvixRDPPort {
		t.Errorf("default ports = %v, want [3389]", got)
	}
	configured := &NetEyeAlyvixSpec{Enabled: true, RDP: &NetEyeAlyvixRDPSpec{Ports: []int32{3390}}}
	ports := configured.EffectiveRDPPorts()
	ports[0] = 1
	if configured.RDP.Ports[0] != 3390 {
		t.Error("EffectiveRDPPorts returns the spec slice")
	}
}

// TestGeneratedCRDKeepsTheTenantDatabaseTypeImmutable keeps the rule that
// forbids moving the records between a local and an external database.
func TestGeneratedCRDKeepsTheStorageProvidersImmutable(t *testing.T) {
	data, err := os.ReadFile("../../config/crd/bases/neteye.cloud_neteyetenants.yaml")
	if err != nil {
		t.Fatalf("read generated NetEyeTenant CRD: %v", err)
	}
	crd := string(data)
	if got := strings.Count(crd, "self.provider == oldSelf.provider"); got != 2 {
		t.Errorf("generated CRD has %d immutable providers, want 2 (database and objectStorage)", got)
	}
	for _, want := range []string{"external is required when provider is external", "external must not be set when provider is local", "local must not be set when provider is external"} {
		if got := strings.Count(crd, want); got != 2 {
			t.Errorf("generated CRD has rule %q %d times, want 2", want, got)
		}
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse generated NetEyeTenant CRD: %v", err)
	}
	// The images come from the operator for the NetEye version, and the
	// instances of the database are its replicas: none of these is a field.
	properties := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch node := node.(type) {
		case map[string]any:
			if props, ok := node["properties"].(map[string]any); ok {
				for name := range props {
					properties[name] = true
				}
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	versions, _ := parsed["spec"].(map[string]any)["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("generated CRD has %d versions, want 1", len(versions))
	}
	schema := versions[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
	walk(schema["properties"].(map[string]any)["spec"])
	for _, field := range []string{"image", "imageName", "instances", "type", "hostname", "blobs"} {
		if properties[field] {
			t.Errorf("generated CRD still has the field %q", field)
		}
	}
	for _, field := range []string{"provider", "replicas", "dbConnection", "objectStorage"} {
		if !properties[field] {
			t.Errorf("generated CRD has no field %q", field)
		}
	}
}
