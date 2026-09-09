// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	EDOTGatewayConfigMapName          = "otel-edot-gateway-config"
	EDOTGatewayVariablesConfigMapName = "otel-edot-gateway-variables"
	EDOTGatewayDeploymentName         = "otel-edot-gateway"
	EDOTGatewayIngressPolicyName      = "neteye-otel-edot-gateway-ingress"
	EDOTGatewayEgressPolicyName       = "neteye-otel-edot-gateway-egress"
	edotGatewayAppLabel               = "otel-edot-gateway"
)

// EDOTGatewayComponent reconciles the isolated Elasticsearch-export boundary.
type EDOTGatewayComponent struct{ client client.Client }

func NewEDOTGatewayComponent(c client.Client) *EDOTGatewayComponent {
	return &EDOTGatewayComponent{client: c}
}

func (c *EDOTGatewayComponent) Ensure(ctx context.Context, namespace string, spec *neteye.NetEyeEDOTGatewaySpec, image string, owner metav1.OwnerReference) (bool, string, error) {
	if spec == nil {
		return false, "edot gateway configuration is required", nil
	}
	if spec.Replicas < 0 {
		return false, "edot gateway replicas must be at least one", nil
	}
	if image == "" {
		return false, "edot gateway resolved image is required", nil
	}
	endpoints, targets, err := validatedEndpoints(spec.ElasticsearchEndpoints)
	if err != nil {
		return false, err.Error(), nil
	}
	key := spec.EffectiveAPIKeySecret()
	apiKeyVersion, err := requiredSecretResourceVersion(ctx, c.client, namespace, key.Name, key.Key)
	if err != nil {
		return prerequisiteOutcome(err)
	}
	rootCAVersion, err := requiredSecretResourceVersion(ctx, c.client, namespace, spec.EffectiveRootCASecretName(), "tls.crt")
	if err != nil {
		return prerequisiteOutcome(err)
	}
	encodedEndpoints, err := json.Marshal(endpoints)
	if err != nil {
		return false, "", err
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, EDOTGatewayConfigMapName, map[string]string{"edot-gateway-config.yaml": edotGatewayConfig}, owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, EDOTGatewayVariablesConfigMapName, map[string]string{"ELASTICSEARCH_ENDPOINTS": string(encodedEndpoints)}, owner); err != nil {
		return false, "", err
	}
	versions, err := edotGatewayInputVersions(ctx, c.client, namespace, apiKeyVersion, rootCAVersion)
	if err != nil {
		return false, "", err
	}
	if err := resources.EnsureDeployment(ctx, c.client, edotGatewayDeployment(namespace, spec, image, versions), owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureService(ctx, c.client, telemetryService(namespace, EDOTGatewayServiceName, edotGatewayAppLabel), owner); err != nil {
		return false, "", err
	}
	if err := c.ensurePolicies(ctx, namespace, targets, owner); err != nil {
		return false, "", err
	}
	return resources.IsDeploymentReady(ctx, c.client, namespace, EDOTGatewayDeploymentName)
}

// Delete removes only EDOT gateway-owned objects. It never deletes collector
// objects or externally managed Elasticsearch credentials and CA Secrets.
func (c *EDOTGatewayComponent) Delete(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	return deleteOwnedResources(ctx, c.client, namespace, owner, edotGatewayResourceInventory())
}

func (c *EDOTGatewayComponent) ensurePolicies(ctx context.Context, namespace string, targets []egressTarget, owner metav1.OwnerReference) error {
	if _, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: ciliumPolicyGVK, Namespace: namespace, Name: EDOTGatewayIngressPolicyName, Owner: &owner, Spec: edotGatewayIngressPolicy(namespace)}); err != nil {
		return err
	}
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: ciliumPolicyGVK, Namespace: namespace, Name: EDOTGatewayEgressPolicyName, Owner: &owner, Spec: edotGatewayEgressPolicy(targets)})
	return err
}

func edotGatewayDeployment(namespace string, spec *neteye.NetEyeEDOTGatewaySpec, image string, annotations map[string]string) *appsv1.Deployment {
	labels := map[string]string{"app": edotGatewayAppLabel}
	apiKey := spec.EffectiveAPIKeySecret()
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: EDOTGatewayDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(spec.EffectiveReplicas()), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}, Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "edot-gateway-ca-bundle", Image: "docker.io/alpine:3.23.5", Command: []string{"/bin/sh", "-ec", caBundleCommand}, VolumeMounts: []corev1.VolumeMount{{Name: "trusted-ca", MountPath: "/work"}, {Name: "host-ca", MountPath: "/input/system/tls-ca-bundle.pem", ReadOnly: true}, {Name: "root-ca", MountPath: "/input/neteye", ReadOnly: true}}}},
				Containers:     []corev1.Container{{Name: "edot-gateway", Image: image, Args: []string{"--config", "/etc/edot/config.yaml"}, Ports: collectorPorts(), EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: EDOTGatewayVariablesConfigMapName}}}}, Env: []corev1.EnvVar{{Name: "ELASTICSEARCH_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: apiKey.Name}, Key: apiKey.Key}}}}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/edot", ReadOnly: true}, {Name: "trusted-ca", MountPath: "/etc/pki/tls/certs/ca-bundle.crt", SubPath: "ca-bundle.pem", ReadOnly: true}}, StartupProbe: healthProbe(5, 30), ReadinessProbe: healthProbe(10, 3), LivenessProbe: healthProbe(10, 3)}},
				Volumes: []corev1.Volume{
					{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: EDOTGatewayConfigMapName}, Items: []corev1.KeyToPath{{Key: "edot-gateway-config.yaml", Path: "config.yaml"}}}}},
					{Name: "trusted-ca", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					{Name: "host-ca", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", Type: ptr.To(corev1.HostPathFile)}}},
					{Name: "root-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: spec.EffectiveRootCASecretName(), Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "ca.crt"}}}}},
				},
			}},
		},
	}
}

type egressTarget struct{ host, port string }

func validatedEndpoints(values []string) ([]string, []egressTarget, error) {
	if len(values) == 0 {
		return nil, nil, fmt.Errorf("at least one Elasticsearch endpoint is required")
	}
	unique := map[string]egressTarget{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) != nil {
			return nil, nil, fmt.Errorf("invalid unsupported Elasticsearch endpoint %q", value)
		}
		if err := validateDNSName(u.Hostname(), "Elasticsearch endpoint hostname"); err != nil {
			return nil, nil, err
		}
		port := u.Port()
		if port == "" {
			port = "443"
		} else {
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return nil, nil, fmt.Errorf("invalid unsupported Elasticsearch endpoint %q", value)
			}
		}
		unique[u.Hostname()+":"+port] = egressTarget{u.Hostname(), port}
		normalized = append(normalized, value)
	}
	targets := make([]egressTarget, 0, len(unique))
	for _, target := range unique {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].host+targets[i].port < targets[j].host+targets[j].port })
	return normalized, targets, nil
}
func validateDNSName(value, field string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 253 {
		return fmt.Errorf("%s must be a non-empty DNS name", field)
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("%s must be a non-empty DNS name", field)
		}
		for _, r := range label {
			if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return fmt.Errorf("%s must be a non-empty DNS name", field)
			}
		}
	}
	return nil
}
func edotGatewayIngressPolicy(namespace string) map[string]any {
	return map[string]any{"endpointSelector": labelsFor(edotGatewayAppLabel), "ingress": []any{map[string]any{"fromEndpoints": []any{namespaceScopedEndpoint(collectorAppLabel, namespace)}, "toPorts": []any{tcpPorts("4317", "4318")}}, map[string]any{"fromEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts("13133")}}}}
}
func edotGatewayEgressPolicy(targets []egressTarget) map[string]any {
	rules := []any{dnsEgress(targetHosts(targets))}
	for _, target := range targets {
		rules = append(rules, fqdnEgress(target.host, target.port))
	}
	return map[string]any{"endpointSelector": labelsFor(edotGatewayAppLabel), "egress": rules}
}
func targetHosts(targets []egressTarget) []string {
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		result = append(result, target.host)
	}
	return result
}

func edotGatewayInputVersions(ctx context.Context, c client.Client, namespace, apiKeyVersion, rootCAVersion string) (map[string]string, error) {
	configVersion, err := configMapResourceVersion(ctx, c, namespace, EDOTGatewayConfigMapName)
	if err != nil {
		return nil, err
	}
	variablesVersion, err := configMapResourceVersion(ctx, c, namespace, EDOTGatewayVariablesConfigMapName)
	if err != nil {
		return nil, err
	}
	return map[string]string{"neteye.cloud/config-resource-version": configVersion, "neteye.cloud/variables-resource-version": variablesVersion, "neteye.cloud/api-key-resource-version": apiKeyVersion, "neteye.cloud/root-ca-resource-version": rootCAVersion}, nil
}

const edotGatewayConfig = `receivers:
  otlp:
    protocols:
      grpc: {endpoint: 0.0.0.0:4317}
      http: {endpoint: 0.0.0.0:4318}
processors:
  batch: {}
  elasticapm: {}
connectors:
  elasticapm: {}
exporters:
  debug: {}
  elasticsearch/otel:
    endpoints: ${ELASTICSEARCH_ENDPOINTS}
    api_key: "${ELASTICSEARCH_API_KEY}"
    tls: {ca_file: /etc/pki/tls/certs/ca-bundle.crt}
    mapping: {mode: otel}
extensions:
  health_check: {endpoint: 0.0.0.0:13133}
service:
  extensions: [health_check]
  pipelines:
    logs: {receivers: [otlp], processors: [batch, elasticapm], exporters: [elasticapm, elasticsearch/otel]}
    metrics: {receivers: [otlp], processors: [batch, elasticapm], exporters: [elasticapm, elasticsearch/otel, debug]}
    traces: {receivers: [otlp], processors: [batch, elasticapm], exporters: [elasticapm, elasticsearch/otel]}
    metrics/aggregated-otel-metrics: {receivers: [elasticapm], exporters: [elasticsearch/otel]}
`
