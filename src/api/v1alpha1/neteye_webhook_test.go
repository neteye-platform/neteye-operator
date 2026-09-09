// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNetEyeValidatorValidateCreate(t *testing.T) {
	validator := &NetEyeValidator{}
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{name: "current version", version: CurrentNetEyeVersion},
		{name: "previous version", version: PreviousNetEyeVersion, wantErr: true},
		{name: "future version", version: "99.99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validator.ValidateCreate(context.Background(), netEyeWithVersion(tt.version))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorValidateUpdate(t *testing.T) {
	validator := &NetEyeValidator{}
	tests := []struct {
		name       string
		oldVersion string
		newVersion string
		wantErr    bool
	}{
		{name: "current version unchanged", oldVersion: CurrentNetEyeVersion, newVersion: CurrentNetEyeVersion},
		{name: "previous to current", oldVersion: PreviousNetEyeVersion, newVersion: CurrentNetEyeVersion},
		{name: "previous version unchanged", oldVersion: PreviousNetEyeVersion, newVersion: PreviousNetEyeVersion, wantErr: true},
		{name: "current to previous", oldVersion: CurrentNetEyeVersion, newVersion: PreviousNetEyeVersion, wantErr: true},
		{name: "skipped upgrade", oldVersion: "4.48", newVersion: CurrentNetEyeVersion, wantErr: true},
		{name: "future version", oldVersion: CurrentNetEyeVersion, newVersion: "99.99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validator.ValidateUpdate(context.Background(), netEyeWithVersion(tt.oldVersion), netEyeWithVersion(tt.newVersion))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUpdate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorValidateDelete(t *testing.T) {
	validator := &NetEyeValidator{}
	if _, err := validator.ValidateDelete(context.Background(), netEyeWithVersion(CurrentNetEyeVersion)); err != nil {
		t.Errorf("ValidateDelete() error = %v, want nil", err)
	}
}

func TestNetEyeValidatorRejectsWrongNamespace(t *testing.T) {
	validator := &NetEyeValidator{}
	foreign := netEyeWithVersion(CurrentNetEyeVersion)
	foreign.Namespace = "tenant-a"
	if _, err := validator.ValidateCreate(context.Background(), foreign); err == nil {
		t.Fatal("ValidateCreate() accepted a NetEye outside neteye-tenant-shared")
	}
	if _, err := validator.ValidateUpdate(context.Background(), netEyeWithVersion(CurrentNetEyeVersion), foreign); err == nil {
		t.Fatal("ValidateUpdate() accepted a NetEye outside neteye-tenant-shared")
	}
}

func TestNetEyeValidatorValidatesElasticStackConfiguration(t *testing.T) {
	valid := validTelemetryConfig()
	tests := []struct {
		name    string
		config  *NetEyeElasticStackSpec
		wantErr bool
	}{
		{name: "absent is disabled"},
		{name: "disabled incomplete config", config: &NetEyeElasticStackSpec{}},
		{name: "enabled valid", config: valid},
		{name: "enabled requires telemetry", config: &NetEyeElasticStackSpec{Enabled: true}, wantErr: true},
		{name: "enabled requires collector", config: &NetEyeElasticStackSpec{Enabled: true, Telemetry: &NetEyeTelemetrySpec{}}, wantErr: true},
		{name: "enabled requires gateway", config: &NetEyeElasticStackSpec{Enabled: true, Telemetry: &NetEyeTelemetrySpec{OTelCollector: &NetEyeOtelCollectorSpec{}}}, wantErr: true},
		{name: "gateway endpoints must not be empty", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.ElasticsearchEndpoints = nil }), wantErr: true},
		{name: "endpoint must be HTTPS absolute", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.ElasticsearchEndpoints = []string{"/_bulk"} }), wantErr: true},
		{name: "endpoint host must not be an IP literal", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.ElasticsearchEndpoints = []string{"https://192.0.2.1:9200"} }), wantErr: true},
		{name: "empty api key override rejected", config: withGatewayAPIKey(valid, NetEyeSecretKeySelector{}), wantErr: true},
		{name: "partial api key override rejected", config: withGatewayAPIKey(valid, NetEyeSecretKeySelector{Name: "api-key"}), wantErr: true},
		{name: "malformed api key override rejected", config: withGatewayAPIKey(valid, NetEyeSecretKeySelector{Name: "bad_name", Key: "api_key"}), wantErr: true},
		{name: "malformed basic auth override rejected", config: withBasicAuthSecret(valid, " bad_name "), wantErr: true},
		{name: "whitespace-only basic auth override rejected", config: withBasicAuthSecret(valid, " "), wantErr: true},
		{name: "malformed collector root CA override rejected", config: withCollectorRootCASecret(valid, "bad_name"), wantErr: true},
		{name: "malformed gateway root CA override rejected", config: withGatewayRootCASecret(valid, "bad_name"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := netEyeWithVersion(CurrentNetEyeVersion)
			obj.Spec.ElasticStack = tt.config
			_, err := (&NetEyeValidator{}).ValidateCreate(context.Background(), obj)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorValidatesEDOTGatewayConfiguration(t *testing.T) {
	valid := &NetEyeElasticStackSpec{
		Enabled: true,
		Telemetry: &NetEyeTelemetrySpec{
			OTelCollector: &NetEyeOtelCollectorSpec{Replicas: 1},
			EDOTGateway: &NetEyeEDOTGatewaySpec{
				Replicas:               1,
				ElasticsearchEndpoints: []string{"https://elasticsearch.example.com:9200"},
				APIKeySecret:           &NetEyeSecretKeySelector{Name: "elasticsearch-api-key", Key: "api_key"},
				RootCASecretName:       "neteye-root-ca",
			},
		},
	}
	tests := []struct {
		name    string
		config  *NetEyeElasticStackSpec
		wantErr bool
	}{
		{name: "valid EDOT configuration", config: valid},
		{name: "valid explicit Secret overrides", config: withOverrides(valid)},
		{name: "EDOT endpoints must not be empty", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.ElasticsearchEndpoints = nil }), wantErr: true},
		{name: "EDOT endpoint must be HTTPS", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) {
			g.ElasticsearchEndpoints = []string{"http://elasticsearch.example.com:9200"}
		}), wantErr: true},
		{name: "EDOT endpoint must be absolute", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.ElasticsearchEndpoints = []string{"/_bulk"} }), wantErr: true},
		{name: "EDOT API key Secret name is required", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.APIKeySecret = &NetEyeSecretKeySelector{Key: "api_key"} }), wantErr: true},
		{name: "EDOT API key Secret key is required", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.APIKeySecret = &NetEyeSecretKeySelector{Name: "api-key"} }), wantErr: true},
		{name: "EDOT API key Secret name must be valid", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) {
			g.APIKeySecret = &NetEyeSecretKeySelector{Name: "bad_name", Key: "api_key"}
		}), wantErr: true},
		{name: "EDOT root CA Secret name must be valid", config: withEDOT(valid, func(g *NetEyeEDOTGatewaySpec) { g.RootCASecretName = "bad_name" }), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := netEyeWithVersion(CurrentNetEyeVersion)
			obj.Spec.ElasticStack = tt.config
			_, err := (&NetEyeValidator{}).ValidateCreate(context.Background(), obj)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestNetEyeValidatorUpdateUsesTelemetryContract(t *testing.T) {
	validator := &NetEyeValidator{}
	oldObj := netEyeWithVersion(CurrentNetEyeVersion)
	newObj := netEyeWithVersion(CurrentNetEyeVersion)
	newObj.Spec.ElasticStack = validTelemetryConfig()
	if _, err := validator.ValidateUpdate(context.Background(), oldObj, newObj); err != nil {
		t.Fatalf("valid telemetry update rejected: %v", err)
	}
	newObj.Spec.ElasticStack.Telemetry.EDOTGateway = nil
	if _, err := validator.ValidateUpdate(context.Background(), oldObj, newObj); err == nil {
		t.Fatal("telemetry update without EDOT gateway was accepted")
	}
}

func TestNetEyeValidatorRejectsManagedKeycloakOptions(t *testing.T) {
	tests := []struct {
		name       string
		optionName string
		wantErr    bool
	}{
		{name: "custom option", optionName: "spi-connections-http-client--default--connection-pool-size"},
		{name: "relative path", optionName: "http-relative-path", wantErr: true},
		{name: "proxy headers", optionName: "proxy-headers", wantErr: true},
		{name: "cluster name", optionName: "spi-cache-embedded--default--cluster-name", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := netEyeWithVersion(CurrentNetEyeVersion)
			obj.Spec.Identity.AdditionalOptions = []NetEyeKeycloakOption{{Name: tt.optionName, Value: "custom"}}
			_, err := (&NetEyeValidator{}).ValidateCreate(context.Background(), obj)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func validTelemetryConfig() *NetEyeElasticStackSpec {
	return &NetEyeElasticStackSpec{Enabled: true, Telemetry: &NetEyeTelemetrySpec{OTelCollector: &NetEyeOtelCollectorSpec{}, EDOTGateway: &NetEyeEDOTGatewaySpec{ElasticsearchEndpoints: []string{"https://elasticsearch.example.com:9200"}}}}
}

func withEDOT(config *NetEyeElasticStackSpec, mutate func(*NetEyeEDOTGatewaySpec)) *NetEyeElasticStackSpec {
	copy := config.DeepCopy()
	mutate(copy.Telemetry.EDOTGateway)
	return copy
}

func withGatewayAPIKey(config *NetEyeElasticStackSpec, value NetEyeSecretKeySelector) *NetEyeElasticStackSpec {
	return withEDOT(config, func(g *NetEyeEDOTGatewaySpec) { g.APIKeySecret = &value })
}

func withOverrides(config *NetEyeElasticStackSpec) *NetEyeElasticStackSpec {
	copy := config.DeepCopy()
	copy.Telemetry.OTelCollector.BasicAuthSecretName = "custom-collector-basicauth"
	copy.Telemetry.OTelCollector.RootCASecretName = "custom-neteye-root-ca"
	copy.Telemetry.EDOTGateway.APIKeySecret = &NetEyeSecretKeySelector{Name: "custom-elasticsearch-api-key", Key: "api_key"}
	copy.Telemetry.EDOTGateway.RootCASecretName = "custom-neteye-root-ca"
	return copy
}

func withBasicAuthSecret(config *NetEyeElasticStackSpec, value string) *NetEyeElasticStackSpec {
	copy := config.DeepCopy()
	copy.Telemetry.OTelCollector.BasicAuthSecretName = value
	return copy
}

func withCollectorRootCASecret(config *NetEyeElasticStackSpec, value string) *NetEyeElasticStackSpec {
	copy := config.DeepCopy()
	copy.Telemetry.OTelCollector.RootCASecretName = value
	return copy
}

func withGatewayRootCASecret(config *NetEyeElasticStackSpec, value string) *NetEyeElasticStackSpec {
	copy := config.DeepCopy()
	copy.Telemetry.EDOTGateway.RootCASecretName = value
	return copy
}

func TestNetEyeValidatorRejectsSecondAuthority(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := netEyeWithVersion(CurrentNetEyeVersion)
	existing.Name = "existing"
	validator := &NetEyeValidator{reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()}
	candidate := netEyeWithVersion(CurrentNetEyeVersion)
	candidate.ObjectMeta = metav1.ObjectMeta{Name: "candidate", Namespace: NetEyeNamespace}

	if _, err := validator.ValidateCreate(context.Background(), candidate); err == nil {
		t.Fatal("ValidateCreate() accepted a second NetEye authority")
	}
}

func netEyeWithVersion(version string) *NetEye {
	return &NetEye{
		ObjectMeta: metav1.ObjectMeta{Namespace: NetEyeNamespace},
		Spec:       NetEyeSpec{Version: version},
	}
}
