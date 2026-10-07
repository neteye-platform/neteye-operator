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

func TestKeycloakEnv(t *testing.T) {
	got := keycloakEnv([]neteye.NetEyeEnvVar{
		{Name: "KC_FEATURES", Value: "preview"},
		{Name: "JAVA_OPTS", Value: "-Xmx1g"},
		{Name: "EMPTY", Value: ""},
	})
	want := []any{
		map[string]any{"name": "KC_FEATURES", "value": "preview"},
		map[string]any{"name": "JAVA_OPTS", "value": "-Xmx1g"},
		map[string]any{"name": "EMPTY", "value": ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keycloakEnv() = %#v, want %#v", got, want)
	}
	if len(keycloakEnv(nil)) != 0 {
		t.Errorf("keycloakEnv(nil) should be empty")
	}
}

func TestKeycloakAdditionalOptions(t *testing.T) {
	got := keycloakAdditionalOptions([]neteye.NetEyeKeycloakOption{
		{Name: "cache-embedded-cluster-name", Value: "custom-cluster"},
		{Name: "proxy-headers", Value: "forwarded"},
		{Name: "spi-connections-http-client--default--connection-pool-size", Value: "20"},
	}, nil)
	want := []any{
		map[string]any{"name": "http-relative-path", "value": HTTPRelativePath},
		map[string]any{"name": "cache-embedded-cluster-name", "value": InfinispanClusterName},
		map[string]any{"name": "spi-connections-http-client--default--connection-pool-size", "value": "20"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keycloakAdditionalOptions() = %#v, want %#v", got, want)
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
		Replicas: 2,
		Hostname: "kc.example.com",
		PodExtraEnvVars: []neteye.NetEyeEnvVar{
			{Name: "KC_FEATURES", Value: "preview"},
		},
		AdditionalOptions: []neteye.NetEyeKeycloakOption{
			{Name: "spi-connections-http-client--default--connection-pool-size", Value: "20"},
		},
		DBConnection: neteye.NetEyeDBConnectionSpec{
			Host:           "db.example.com",
			Port:           3307,
			DBName:         "keycloak",
			UsernameSecret: &neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "username"},
			PasswordSecret: &neteye.NetEyeSecretKeySelector{Name: "kc-db", Key: "password"},
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
	if want := keycloakEnv(identity.PodExtraEnvVars); !reflect.DeepEqual(spec["env"], want) {
		t.Errorf("env = %#v, want %#v", spec["env"], want)
	}
	if want := keycloakAdditionalOptions(identity.AdditionalOptions, identity.Telemetry); !reflect.DeepEqual(spec["additionalOptions"], want) {
		t.Errorf("additionalOptions = %#v, want %#v", spec["additionalOptions"], want)
	}
	if _, ok := spec["networkPolicy"]; !ok {
		t.Error("networkPolicy should always be configured")
	}
}

func TestKeycloakInstanceSpecDatabaseDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name     string
		database neteye.NetEyeDBConnectionSpec
		want     map[string]any
	}{
		{
			name:     "uses defaults when admission is bypassed",
			database: neteye.NetEyeDBConnectionSpec{Host: "db.example.com"},
			want: map[string]any{
				"vendor":         "mariadb",
				"host":           "db.example.com",
				"port":           int64(defaultDatabasePort),
				"database":       "keycloak",
				"usernameSecret": map[string]any{"name": "keycloak-db-credentials", "key": "username"},
				"passwordSecret": map[string]any{"name": "keycloak-db-credentials", "key": "password"},
			},
		},
		{
			name: "preserves explicit overrides",
			database: neteye.NetEyeDBConnectionSpec{
				Host:           "db.example.com",
				Port:           3307,
				DBName:         "custom-keycloak",
				UsernameSecret: &neteye.NetEyeSecretKeySelector{Name: "custom-credentials", Key: "user"},
				PasswordSecret: &neteye.NetEyeSecretKeySelector{Name: "custom-credentials", Key: "pass"},
			},
			want: map[string]any{
				"vendor":         "mariadb",
				"host":           "db.example.com",
				"port":           int64(3307),
				"database":       "custom-keycloak",
				"usernameSecret": map[string]any{"name": "custom-credentials", "key": "user"},
				"passwordSecret": map[string]any{"name": "custom-credentials", "key": "pass"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "kc.example.com", DBConnection: tt.database})
			if got := spec["db"]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("database spec = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestKeycloakInstanceSpecOmitsEnvWhenEmpty(t *testing.T) {
	spec := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "h"})
	if _, ok := spec["env"]; ok {
		t.Errorf("env should not be set when Env is empty")
	}
}

func TestKeycloakInstanceSpecTelemetrySignals(t *testing.T) {
	tests := []struct {
		name      string
		telemetry *neteye.NetEyeIdentityTelemetrySpec
		features  []any
		options   []any
	}{
		{name: "omitted"},
		{name: "both disabled", telemetry: &neteye.NetEyeIdentityTelemetrySpec{}},
		{name: "logs", telemetry: &neteye.NetEyeIdentityTelemetrySpec{LogsEnabled: true}, features: []any{"opentelemetry-logs"}, options: []any{map[string]any{"name": "telemetry-logs-enabled", "value": "true"}}},
		{name: "metrics", telemetry: &neteye.NetEyeIdentityTelemetrySpec{MetricsEnabled: true}, features: []any{"opentelemetry-metrics"}, options: []any{map[string]any{"name": "telemetry-metrics-enabled", "value": "true"}, map[string]any{"name": "metrics-enabled", "value": "true"}}},
		{name: "both", telemetry: &neteye.NetEyeIdentityTelemetrySpec{LogsEnabled: true, MetricsEnabled: true}, features: []any{"opentelemetry-logs", "opentelemetry-metrics"}, options: []any{map[string]any{"name": "telemetry-logs-enabled", "value": "true"}, map[string]any{"name": "telemetry-metrics-enabled", "value": "true"}, map[string]any{"name": "metrics-enabled", "value": "true"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "keycloak.example.com", Telemetry: tt.telemetry})
			if got, found := spec["features"]; (len(tt.features) > 0 && (!reflect.DeepEqual(got, map[string]any{"enabled": tt.features}) || !found)) || (len(tt.features) == 0 && found) {
				t.Errorf("features = %#v, found=%t", got, found)
			}
			if got, found := spec["telemetry"]; (len(tt.features) > 0 && (!reflect.DeepEqual(got, map[string]any{"endpoint": "http://otel-edot-gateway:4317", "protocol": "grpc", "serviceName": "neteye-keycloak", "resourceAttributes": map[string]any{"data_stream.namespace": "neteye_system_internal"}}) || !found)) || (len(tt.features) == 0 && found) {
				t.Errorf("telemetry = %#v, found=%t", got, found)
			}
			options := spec["additionalOptions"].([]any)
			for _, want := range tt.options {
				if !containsOption(options, want) {
					t.Errorf("additionalOptions = %#v, missing %#v", options, want)
				}
			}
			if len(options) != 2+len(tt.options) {
				t.Errorf("additionalOptions = %#v, want no telemetry option leakage", options)
			}
			if spec["startOptimized"] != false {
				t.Errorf("startOptimized = %#v, want false", spec["startOptimized"])
			}
		})
	}
}

func TestKeycloakInstanceSpecEnabledFeatures(t *testing.T) {
	tests := []struct {
		name      string
		features  []string
		telemetry *neteye.NetEyeIdentityTelemetrySpec
		want      []any
	}{
		{name: "none"},
		{name: "spec features", features: []string{"token-exchange", "admin-fine-grained-authz:v2"}, want: []any{"token-exchange", "admin-fine-grained-authz:v2"}},
		{
			name:      "merged with telemetry",
			features:  []string{"token-exchange"},
			telemetry: &neteye.NetEyeIdentityTelemetrySpec{LogsEnabled: true},
			want:      []any{"opentelemetry-logs", "token-exchange"},
		},
		{
			name:      "managed features are dropped",
			features:  []string{"opentelemetry-logs", "opentelemetry-metrics", "token-exchange"},
			telemetry: &neteye.NetEyeIdentityTelemetrySpec{LogsEnabled: true},
			want:      []any{"opentelemetry-logs", "token-exchange"},
		},
		{name: "duplicates collapse", features: []string{"token-exchange", "token-exchange"}, want: []any{"token-exchange"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "keycloak.example.com", EnabledFeatures: tt.features, Telemetry: tt.telemetry})
			got, found := spec["features"]
			if len(tt.want) == 0 {
				if found {
					t.Fatalf("features = %#v, want omitted", got)
				}
				return
			}
			if !found || !reflect.DeepEqual(got, map[string]any{"enabled": tt.want}) {
				t.Errorf("features = %#v, found=%t, want %#v", got, found, map[string]any{"enabled": tt.want})
			}
		})
	}
}

func TestKeycloakTelemetryResourceAttributes(t *testing.T) {
	attributes := map[string]string{
		"data_stream.namespace":  "tenant-a",
		"deployment.environment": "production",
	}
	identity := neteye.NetEyeIdentitySpec{Hostname: "keycloak.example.com", Telemetry: &neteye.NetEyeIdentityTelemetrySpec{LogsEnabled: true, ResourceAttributes: attributes}}

	for _, flags := range []neteye.NetEyeIdentityTelemetrySpec{
		{LogsEnabled: true, ResourceAttributes: attributes},
		{MetricsEnabled: true, ResourceAttributes: attributes},
		{LogsEnabled: true, MetricsEnabled: true, ResourceAttributes: attributes},
		{ResourceAttributes: attributes},
	} {
		*identity.Telemetry = flags
		spec := keycloakInstanceSpec("img", identity)
		telemetry, found := spec["telemetry"].(map[string]any)
		if enabled := flags.LogsEnabled || flags.MetricsEnabled; !enabled {
			if found {
				t.Errorf("telemetry = %#v, want omitted when signals are disabled", telemetry)
			}
			continue
		}
		if !found {
			t.Fatal("telemetry is missing when a signal is enabled")
		}
		if got, want := telemetry["serviceName"], "neteye-keycloak"; got != want {
			t.Errorf("serviceName = %q, want %q", got, want)
		}
		want := map[string]any{"data_stream.namespace": "tenant-a", "deployment.environment": "production"}
		if got := telemetry["resourceAttributes"]; !reflect.DeepEqual(got, want) {
			t.Errorf("resourceAttributes = %#v, want %#v", got, want)
		}
	}
	if !reflect.DeepEqual(attributes, map[string]string{"data_stream.namespace": "tenant-a", "deployment.environment": "production"}) {
		t.Errorf("input resource attributes mutated: %#v", attributes)
	}

	merged := keycloakTelemetryResourceAttributes(attributes)
	merged["deployment.environment"] = "staging"
	if attributes["deployment.environment"] != "production" {
		t.Errorf("resource attributes alias input map: %#v", attributes)
	}
}

func containsOption(options []any, want any) bool {
	for _, option := range options {
		if reflect.DeepEqual(option, want) {
			return true
		}
	}
	return false
}

func TestKeycloakNetworkPolicies(t *testing.T) {
	instance := keycloakInstanceSpec("img", neteye.NetEyeIdentitySpec{Hostname: "h"})
	if !reflect.DeepEqual(instance["networkPolicy"], map[string]any{"enabled": false}) {
		t.Errorf("native network policy = %#v, want disabled", instance["networkPolicy"])
	}
	ingress := keycloakIngressNetworkPolicySpec()
	if !reflect.DeepEqual(ingress["endpointSelector"], map[string]any{"matchLabels": keycloakCiliumWorkloadLabels()}) {
		t.Errorf("ingress selector = %#v", ingress["endpointSelector"])
	}
	if _, found := ingress["policyTypes"]; found {
		t.Error("CiliumNetworkPolicy must not carry native NetworkPolicy policyTypes")
	}
	ingressRules := ingress["ingress"].([]any)
	if len(ingressRules) != 2 {
		t.Fatalf("ingress rule count = %d, want 2", len(ingressRules))
	}
	clusterRule := ingressRules[0].(map[string]any)
	wantClusterPorts := []any{map[string]any{"ports": []any{map[string]any{"port": "7800", "protocol": "TCP"}, map[string]any{"port": "57800", "protocol": "TCP"}}}}
	if !reflect.DeepEqual(clusterRule["toPorts"], wantClusterPorts) {
		t.Errorf("cluster ingress ports = %#v", clusterRule["toPorts"])
	}
	// An external Keycloak must not reach the Infinispan cluster transport.
	if !reflect.DeepEqual(clusterRule["fromEndpoints"], []any{map[string]any{"matchLabels": keycloakCiliumWorkloadLabels()}}) {
		t.Errorf("cluster ingress source = %#v, want Keycloak peers only", clusterRule["fromEndpoints"])
	}
	operatorRule := ingressRules[1].(map[string]any)
	wantSource := []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": OperatorSystemNamespace}}}
	if !reflect.DeepEqual(operatorRule["fromEndpoints"], wantSource) {
		t.Errorf("operator ingress source = %#v", operatorRule["fromEndpoints"])
	}
	wantAdminPorts := []any{map[string]any{"ports": []any{map[string]any{"port": "8080", "protocol": "TCP"}}}}
	if !reflect.DeepEqual(operatorRule["toPorts"], wantAdminPorts) {
		t.Errorf("operator ingress ports = %#v", operatorRule["toPorts"])
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
	egress := keycloakEgressNetworkPolicySpec()
	if !reflect.DeepEqual(egress["endpointSelector"], map[string]any{"matchLabels": keycloakCiliumWorkloadLabels()}) {
		t.Errorf("egress endpoint selector = %#v", egress["endpointSelector"])
	}
	if !reflect.DeepEqual(egress["egress"], []any{map[string]any{"toEntities": []any{"all"}}}) {
		t.Errorf("egress must reach every destination, got %#v", egress["egress"])
	}
	if _, found := egress["egressDeny"]; found {
		t.Errorf("egress must carry no deny exceptions, got %#v", egress["egressDeny"])
	}
	if _, found := egress["policyTypes"]; found {
		t.Error("CiliumNetworkPolicy must not carry native NetworkPolicy policyTypes")
	}
}
