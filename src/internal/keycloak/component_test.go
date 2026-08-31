// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"reflect"
	"testing"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
)

func TestIdentityReplicas(t *testing.T) {
	tests := []struct {
		name     string
		replicas int32
		want     int32
	}{
		{name: "defaults to one", replicas: 0, want: 1},
		{name: "uses configured value", replicas: 3, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := identityReplicas(neteye.NetEyeIdentitySpec{Replicas: tt.replicas}); got != tt.want {
				t.Errorf("identityReplicas() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExternalDatabasePort(t *testing.T) {
	tests := []struct {
		name string
		port int32
		want int32
	}{
		{name: "defaults to mariadb port", port: 0, want: defaultDatabasePort},
		{name: "uses configured value", port: 5306, want: 5306},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := externalDatabasePort(neteye.NetEyeDBConnectionSpec{Port: tt.port}); got != tt.want {
				t.Errorf("externalDatabasePort() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResourceURI(t *testing.T) {
	want := "https://kc.example.com" + HTTPRelativePath
	if got := resourceURI("kc.example.com"); got != want {
		t.Errorf("resourceURI() = %q, want %q", got, want)
	}
}

func TestPodExtraEnvVars(t *testing.T) {
	got := podExtraEnvVars([]string{"KC_FEATURES", "JAVA_OPTS=-Xmx1g"})
	want := []any{
		map[string]any{"name": "KC_FEATURES"},
		map[string]any{"name": "JAVA_OPTS", "value": "-Xmx1g"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("podExtraEnvVars() = %#v, want %#v", got, want)
	}
	if len(podExtraEnvVars(nil)) != 0 {
		t.Errorf("podExtraEnvVars(nil) should be empty")
	}
}

func TestClusterExtensionSpec(t *testing.T) {
	spec := clusterExtensionSpec()
	if spec["namespace"] != OperatorNamespace {
		t.Errorf("namespace = %v, want %v", spec["namespace"], OperatorNamespace)
	}
	source, ok := spec["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is not a map: %T", spec["source"])
	}
	catalog, ok := source["catalog"].(map[string]any)
	if !ok {
		t.Fatalf("catalog is not a map: %T", source["catalog"])
	}
	if catalog["packageName"] != extensionName {
		t.Errorf("packageName = %v, want %v", catalog["packageName"], extensionName)
	}
}

func TestKeycloakInstanceSpec(t *testing.T) {
	identity := neteye.NetEyeIdentitySpec{
		Replicas:        2,
		Hostname:        "kc.example.com",
		PodExtraEnvVars: []string{"KC_FEATURES"},
		DBConnection: neteye.NetEyeDBConnectionSpec{
			Host:           "db.example.com",
			Port:           3307,
			DBName:         "keycloak",
			UsernameSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "username"},
			PasswordSecret: neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "password"},
		},
	}
	spec := keycloakInstanceSpec("ghcr.io/example/keycloak:1.0.0", identity)

	if spec["image"] != "ghcr.io/example/keycloak:1.0.0" {
		t.Errorf("image = %v", spec["image"])
	}
	if spec["instances"] != int64(2) {
		t.Errorf("instances = %v, want int64(2)", spec["instances"])
	}
	db, ok := spec["db"].(map[string]any)
	if !ok {
		t.Fatalf("db is not a map: %T", spec["db"])
	}
	if db["host"] != "db.example.com" {
		t.Errorf("db.host = %v", db["host"])
	}
	if db["port"] != int64(3307) {
		t.Errorf("db.port = %v, want int64(3307)", db["port"])
	}
	ingress, ok := spec["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("ingress is not a map: %T", spec["ingress"])
	}
	if ingress["enabled"] != false {
		t.Errorf("ingress.enabled = %v, want false", ingress["enabled"])
	}
	hostname, ok := spec["hostname"].(map[string]any)
	if !ok {
		t.Fatalf("hostname is not a map: %T", spec["hostname"])
	}
	if hostname["hostname"] != resourceURI("kc.example.com") {
		t.Errorf("hostname.hostname = %v, want %v", hostname["hostname"], resourceURI("kc.example.com"))
	}
	if _, ok := spec["env"]; !ok {
		t.Errorf("env should be set when PodExtraEnvVars is provided")
	}
	if _, ok := spec["networkPolicy"]; !ok {
		t.Error("networkPolicy should always be configured")
	}
}

func TestKeycloakInstanceSpecOmitsEnvWhenEmpty(t *testing.T) {
	spec := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "h"})
	if _, ok := spec["env"]; ok {
		t.Errorf("env should not be set when PodExtraEnvVars is empty")
	}
}

func TestKeycloakNetworkPolicies(t *testing.T) {
	instance := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "h"})
	if !reflect.DeepEqual(instance["networkPolicy"], map[string]any{"enabled": false}) {
		t.Errorf("native network policy = %#v, want disabled", instance["networkPolicy"])
	}
	ingress := keycloakIngressNetworkPolicySpec()
	if !reflect.DeepEqual(ingress["podSelector"], map[string]any{"matchLabels": keycloakWorkloadLabels()}) {
		t.Errorf("ingress selector = %#v", ingress["podSelector"])
	}
	if !reflect.DeepEqual(ingress["policyTypes"], []any{"Ingress"}) {
		t.Errorf("ingress policy types = %#v", ingress["policyTypes"])
	}
	if len(ingress["ingress"].([]any)) != 1 {
		t.Fatalf("ingress rule count = %d, want 1", len(ingress["ingress"].([]any)))
	}
	host := keycloakHostManagementPolicySpec()
	if !reflect.DeepEqual(host["endpointSelector"], map[string]any{"matchLabels": map[string]any{
		"k8s:app":                          "keycloak",
		"k8s:app.kubernetes.io/instance":   InstanceName,
		"k8s:app.kubernetes.io/managed-by": "keycloak-operator",
	}}) {
		t.Errorf("host endpoint selector = %#v", host["endpointSelector"])
	}
	hostRules := host["ingress"].([]any)
	if !reflect.DeepEqual(hostRules[0].(map[string]any)["fromEntities"], []any{"ingress"}) {
		t.Errorf("gateway entities = %#v", hostRules[0].(map[string]any)["fromEntities"])
	}
	if !reflect.DeepEqual(hostRules[1].(map[string]any)["fromEntities"], []any{"host", "remote-node"}) {
		t.Errorf("host entities = %#v", hostRules[1].(map[string]any)["fromEntities"])
	}
	egress := keycloakEgressNetworkPolicySpec(3306)
	if got := egress["policyTypes"]; !reflect.DeepEqual(got, []any{"Egress"}) {
		t.Errorf("policyTypes = %#v, want only Egress", got)
	}
	rules := egress["egress"].([]any)
	if len(rules) != 3 {
		t.Fatalf("egress rule count = %d, want 3", len(rules))
	}
	if _, found := rules[0].(map[string]any)["to"]; found {
		t.Error("database egress must not constrain an external hostname by CIDR")
	}
	if !reflect.DeepEqual(rules[1].(map[string]any)["to"], []any{namespaceAndPodSelector(KubeSystemNamespace, map[string]any{"k8s-app": "kube-dns"})}) {
		t.Errorf("DNS egress destination = %#v", rules[1].(map[string]any)["to"])
	}
	if !reflect.DeepEqual(rules[2].(map[string]any)["ports"], []any{networkPort(7800, "TCP"), networkPort(57800, "TCP")}) {
		t.Errorf("intra-cluster ports = %#v", rules[2].(map[string]any)["ports"])
	}
}
