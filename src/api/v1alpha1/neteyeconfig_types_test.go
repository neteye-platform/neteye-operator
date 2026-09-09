// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"fmt"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

func TestNetEyeEDOTGatewaySpecDeepCopy(t *testing.T) {
	original := &NetEyeElasticStackSpec{
		Telemetry: &NetEyeTelemetrySpec{
			OTelCollector: &NetEyeOtelCollectorSpec{BasicAuthSecretName: "basic-auth", RootCASecretName: "collector-ca"},
			EDOTGateway: &NetEyeEDOTGatewaySpec{
				Replicas:               2,
				ElasticsearchEndpoints: []string{"https://elasticsearch.example.com:9200"},
				APIKeySecret:           &NetEyeSecretKeySelector{Name: "api-key", Key: "api_key"},
				RootCASecretName:       "root-ca",
			},
		},
	}
	copy := original.DeepCopy()
	copy.Telemetry.OTelCollector.BasicAuthSecretName = "other-basic-auth"
	copy.Telemetry.EDOTGateway.ElasticsearchEndpoints[0] = "https://other.example.com:9200"
	copy.Telemetry.EDOTGateway.APIKeySecret.Name = "other-api-key"
	copy.Telemetry.EDOTGateway.RootCASecretName = "other-root-ca"

	if original.Telemetry.OTelCollector.BasicAuthSecretName != "basic-auth" {
		t.Error("DeepCopy shared collector configuration")
	}
	if original.Telemetry.EDOTGateway.ElasticsearchEndpoints[0] != "https://elasticsearch.example.com:9200" {
		t.Error("DeepCopy shared EDOT endpoint slice")
	}
	if original.Telemetry.EDOTGateway.APIKeySecret.Name != "api-key" {
		t.Error("DeepCopy shared EDOT API key selector")
	}
	if original.Telemetry.EDOTGateway.RootCASecretName != "root-ca" {
		t.Error("DeepCopy shared EDOT root CA name")
	}
}

func TestGeneratedCRDDefaultsEDOTGatewayReplicas(t *testing.T) {
	data, err := os.ReadFile("../../config/crd/bases/neteye.cloud_neteyes.yaml")
	if err != nil {
		t.Fatalf("read generated NetEye CRD: %v", err)
	}
	crd := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, crd); err != nil {
		t.Fatalf("decode generated NetEye CRD: %v", err)
	}
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found || len(versions) == 0 {
		t.Fatalf("find versions in generated CRD: found=%t err=%v", found, err)
	}
	version, ok := versions[0].(map[string]interface{})
	if !ok {
		t.Fatalf("generated CRD version has unexpected type %T", versions[0])
	}
	value, found, err := unstructured.NestedFieldCopy(version, "schema", "openAPIV3Schema", "properties", "spec", "properties", "elasticStack", "properties", "telemetry", "properties", "edotGateway", "properties", "replicas", "default")
	if err != nil || !found {
		t.Fatalf("find EDOT replicas default in generated CRD: found=%t err=%v", found, err)
	}
	if value != int64(1) && value != float64(1) {
		t.Errorf("EDOT replicas default = %v, want 1", value)
	}
}

func TestTelemetryDefaults(t *testing.T) {
	collector := &NetEyeOtelCollectorSpec{}
	if collector.EffectiveReplicas() != 1 || collector.EffectiveBasicAuthSecretName() != "otel-collector-basicauth" || collector.EffectiveRootCASecretName() != "neteye-root-ca" {
		t.Fatalf("collector defaults are incorrect: replicas=%d basicAuth=%q rootCA=%q", collector.EffectiveReplicas(), collector.EffectiveBasicAuthSecretName(), collector.EffectiveRootCASecretName())
	}
	gateway := &NetEyeEDOTGatewaySpec{}
	if gateway.EffectiveReplicas() != 1 || gateway.EffectiveAPIKeySecret() != (NetEyeSecretKeySelector{Name: "otel-collector-api-key", Key: "api_key"}) || gateway.EffectiveRootCASecretName() != "neteye-root-ca" {
		t.Fatalf("gateway defaults are incorrect: replicas=%d apiKey=%+v rootCA=%q", gateway.EffectiveReplicas(), gateway.EffectiveAPIKeySecret(), gateway.EffectiveRootCASecretName())
	}
}

func TestGeneratedCRDUsesFinalTelemetrySchema(t *testing.T) {
	data, err := os.ReadFile("../../config/crd/bases/neteye.cloud_neteyes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	crd := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, crd); err != nil {
		t.Fatal(err)
	}
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found || len(versions) == 0 {
		t.Fatalf("find CRD versions: found=%t err=%v", found, err)
	}
	version := versions[0].(map[string]interface{})
	properties, found, err := unstructured.NestedMap(version, "schema", "openAPIV3Schema", "properties", "spec", "properties", "elasticStack", "properties", "telemetry", "properties")
	if err != nil || !found {
		t.Fatalf("find telemetry schema: found=%t err=%v", found, err)
	}
	collector := properties["otelCollector"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, removed := range []string{"elasticsearchEndpoints", "apiKeySecret", "oidcIssuerURL"} {
		if _, exists := collector[removed]; exists {
			t.Errorf("collector schema still contains removed field %q", removed)
		}
	}
	if _, exists := properties["edotGateway"]; !exists {
		t.Error("telemetry schema is missing edotGateway")
	}
	for field, want := range map[string]interface{}{"replicas": float64(1), "basicAuthSecretName": "otel-collector-basicauth", "rootCASecretName": "neteye-root-ca"} {
		got := collector[field].(map[string]interface{})["default"]
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("collector %s default = %v, want %v", field, got, want)
		}
	}
	gateway := properties["edotGateway"].(map[string]interface{})["properties"].(map[string]interface{})
	for field, want := range map[string]interface{}{"replicas": float64(1), "rootCASecretName": "neteye-root-ca"} {
		got := gateway[field].(map[string]interface{})["default"]
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("gateway %s default = %v, want %v", field, got, want)
		}
	}
	apiKeyDefault := gateway["apiKeySecret"].(map[string]interface{})["default"].(map[string]interface{})
	if apiKeyDefault["name"] != "otel-collector-api-key" || apiKeyDefault["key"] != "api_key" {
		t.Errorf("gateway API key default = %#v", apiKeyDefault)
	}
}

func TestAddToSchemeRegistersNetEyeResourceTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add NetEye types to scheme: %v", err)
	}

	for _, kind := range []string{"NetEye", "NetEyeList"} {
		if _, err := scheme.New(GroupVersion.WithKind(kind)); err != nil {
			t.Errorf("scheme does not register %s: %v", kind, err)
		}
	}
}

func TestIsLatestVersion(t *testing.T) {
	if !IsLatestVersion(CurrentNetEyeVersion) {
		t.Errorf("IsLatestVersion(%q) = false, want true", CurrentNetEyeVersion)
	}
	if IsLatestVersion(PreviousNetEyeVersion) {
		t.Errorf("IsLatestVersion(%q) = true, want false", PreviousNetEyeVersion)
	}
	if IsLatestVersion("0.0") {
		t.Error("IsLatestVersion(\"0.0\") = true, want false")
	}
}

func TestIsPreviousVersion(t *testing.T) {
	if !IsPreviousVersion(PreviousNetEyeVersion) {
		t.Errorf("IsPreviousVersion(%q) = false, want true", PreviousNetEyeVersion)
	}
	if IsPreviousVersion(CurrentNetEyeVersion) {
		t.Errorf("IsPreviousVersion(%q) = true, want false", CurrentNetEyeVersion)
	}
}

func TestIsSupportedVersion(t *testing.T) {
	if !IsSupportedVersion(CurrentNetEyeVersion) {
		t.Errorf("IsSupportedVersion(%q) = false, want true", CurrentNetEyeVersion)
	}
	if IsSupportedVersion("0.0") {
		t.Error("IsSupportedVersion(\"0.0\") = true, want false")
	}
}

func TestComponentsForVersion(t *testing.T) {
	c, ok := ComponentsForVersion(CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("ComponentsForVersion(%q) not found", CurrentNetEyeVersion)
	}
	if c.KeycloakImage == "" {
		t.Error("expected a resolved Keycloak image")
	}
	if c.OTelCollectorImage == "" {
		t.Error("expected a resolved OpenTelemetry Collector image")
	}
	if c.EDOTGatewayImage == "" {
		t.Error("expected a resolved EDOT Gateway image")
	}
	if _, ok := ComponentsForVersion("0.0"); ok {
		t.Error("ComponentsForVersion(\"0.0\") found, want not found")
	}
}

func TestComponentsForVersionEDOTGatewayImageOverride(t *testing.T) {
	t.Setenv(RelatedImageEDOTGatewayEnv, "registry.example/edot-gateway:dev")

	components, ok := ComponentsForVersion(CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("ComponentsForVersion(%q) not found", CurrentNetEyeVersion)
	}
	if got, want := components.EDOTGatewayImage, "registry.example/edot-gateway:dev"; got != want {
		t.Errorf("EDOTGatewayImage = %q, want %q", got, want)
	}
}

func TestComponentsForVersionKeycloakImageOverride(t *testing.T) {
	t.Setenv(RelatedImageKeycloakEnv, "registry.example/neteye-keycloak:dev")

	components, ok := ComponentsForVersion(CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("ComponentsForVersion(%q) not found", CurrentNetEyeVersion)
	}
	if got, want := components.KeycloakImage, "registry.example/neteye-keycloak:dev"; got != want {
		t.Errorf("KeycloakImage = %q, want %q", got, want)
	}
}

func TestComponentsForVersionWhitespaceKeycloakImageOverrideUsesDefault(t *testing.T) {
	t.Setenv(RelatedImageKeycloakEnv, " \t ")

	components, ok := ComponentsForVersion(CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("ComponentsForVersion(%q) not found", CurrentNetEyeVersion)
	}
	if got, want := components.KeycloakImage, netEyeVersionMap[CurrentNetEyeVersion].KeycloakImage; got != want {
		t.Errorf("KeycloakImage = %q, want %q", got, want)
	}
}

func TestComponentsForVersionOTelCollectorImageOverride(t *testing.T) {
	t.Setenv(RelatedImageOTelCollectorEnv, "registry.example/otel-collector:dev")

	components, ok := ComponentsForVersion(CurrentNetEyeVersion)
	if !ok {
		t.Fatalf("ComponentsForVersion(%q) not found", CurrentNetEyeVersion)
	}
	if got, want := components.OTelCollectorImage, "registry.example/otel-collector:dev"; got != want {
		t.Errorf("OTelCollectorImage = %q, want %q", got, want)
	}
}

func TestSupportedVersions(t *testing.T) {
	versions := SupportedVersions()
	for _, v := range versions {
		if v == CurrentNetEyeVersion {
			return
		}
	}
	t.Errorf("SupportedVersions() = %v, missing %q", versions, CurrentNetEyeVersion)
}
