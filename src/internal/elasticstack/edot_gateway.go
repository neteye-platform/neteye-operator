// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	EDOTGatewayConfigMapName          = "otel-edot-gateway-config"
	EDOTGatewayVariablesConfigMapName = "otel-edot-gateway-variables"
	EDOTGatewayDeploymentName         = "otel-edot-gateway"
	EDOTGatewayIngressPolicyName      = "neteye-otel-edot-gateway-ingress"
	EDOTGatewayEgressPolicyName       = "neteye-otel-edot-gateway-egress"
	edotGatewayAppLabel               = "otel-edot-gateway"
	DefaultAPMAPIKeySecretName        = "otel-collector-apm-api-key-secret"
	DefaultAPMAPIKeySecretKey         = "api_key"
)

// EDOTGatewayComponent reconciles the isolated Elasticsearch-export boundary.
type EDOTGatewayComponent struct{ client client.Client }

func NewEDOTGatewayComponent(c client.Client) *EDOTGatewayComponent {
	return &EDOTGatewayComponent{client: c}
}

func (c *EDOTGatewayComponent) Ensure(ctx context.Context, namespace string, spec *neteye.NetEyeEDOTGatewaySpec, elasticsearchEndpoints []string, image string, identityTelemetryEnabled bool, owner metav1.OwnerReference) Outcome {
	if spec == nil {
		return degradedOutcome(ReasonInvalidConfiguration, "edot gateway configuration is required", nil)
	}
	if spec.Replicas < 0 {
		return degradedOutcome(ReasonInvalidConfiguration, "edot gateway replicas must be at least one", nil)
	}
	if image == "" {
		return degradedOutcome(ReasonInvalidConfiguration, "edot gateway resolved image is required", nil)
	}
	endpoints, targets, err := validatedEndpoints(elasticsearchEndpoints)
	if err != nil {
		return degradedOutcome(ReasonInvalidConfiguration, err.Error(), nil)
	}
	if err := c.ensureIngressPolicy(ctx, namespace, identityTelemetryEnabled, owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
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
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	configVersion, err := resources.EnsureConfigMap(ctx, c.client, namespace, EDOTGatewayConfigMapName, map[string]string{"edot-gateway-config.yaml": edotGatewayConfig}, owner)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	versions := edotGatewayInputVersions(configVersion, apiKeyVersion, rootCAVersion)
	if err := resources.EnsureDeployment(ctx, c.client, edotGatewayDeployment(namespace, spec, image, string(encodedEndpoints), versions), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := deleteOwnedResources(ctx, c.client, namespace, owner, edotGatewayLegacyInventory()); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureService(ctx, c.client, telemetryService(namespace, EDOTGatewayServiceName, edotGatewayAppLabel), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := c.ensureEgressPolicy(ctx, namespace, targets, owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	return deploymentOutcome(ctx, c.client, namespace, EDOTGatewayDeploymentName, "EDOT Gateway is ready")
}

// Delete removes only EDOT gateway-owned objects. It never deletes collector
// objects or externally managed Elasticsearch credentials and CA Secrets.
func (c *EDOTGatewayComponent) Delete(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	inventory := append(edotGatewayResourceInventory(), edotGatewayLegacyInventory()...)
	return deleteOwnedResources(ctx, c.client, namespace, owner, append(inventory, legacyPolicyInventory(EDOTGatewayIngressPolicyName, EDOTGatewayEgressPolicyName)...))
}

func (c *EDOTGatewayComponent) ensureIngressPolicy(ctx context.Context, namespace string, identityTelemetryEnabled bool, owner metav1.OwnerReference) error {
	return resources.EnsureNetworkPolicy(ctx, c.client, namespace, EDOTGatewayIngressPolicyName, edotGatewayIngressPolicy(identityTelemetryEnabled), owner)
}

func (c *EDOTGatewayComponent) ensureEgressPolicy(ctx context.Context, namespace string, targets []egressTarget, owner metav1.OwnerReference) error {
	return resources.EnsureNetworkPolicy(ctx, c.client, namespace, EDOTGatewayEgressPolicyName, edotGatewayEgressPolicy(targets), owner)
}

func edotGatewayDeployment(namespace string, spec *neteye.NetEyeEDOTGatewaySpec, image, elasticsearchEndpoints string, annotations map[string]string) *appsv1.Deployment {
	labels := map[string]string{"app": edotGatewayAppLabel}
	apiKey := spec.EffectiveAPIKeySecret()
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: EDOTGatewayDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(spec.EffectiveReplicas()), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}, Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "edot-gateway", Image: image, Args: []string{"--config", "/etc/edot/config.yaml"}, Ports: collectorPorts(), Env: []corev1.EnvVar{{Name: "ELASTICSEARCH_ENDPOINTS", Value: elasticsearchEndpoints}, {Name: "ELASTICSEARCH_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: apiKey.Name}, Key: apiKey.Key}}}}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/edot", ReadOnly: true}, {Name: "root-ca", MountPath: rootCAMountPath, ReadOnly: true}}, StartupProbe: healthProbe(5, 30), ReadinessProbe: healthProbe(10, 3), LivenessProbe: healthProbe(10, 3)}},
				Volumes: []corev1.Volume{
					{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: EDOTGatewayConfigMapName}, Items: []corev1.KeyToPath{{Key: "edot-gateway-config.yaml", Path: "config.yaml"}}}}},
					{Name: "root-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: spec.EffectiveRootCASecretName(), Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "ca.crt"}}}}},
				},
			}},
		},
	}
}

type egressTarget struct {
	host string
	port int64
}

func validatedEndpoints(values []string) ([]string, []egressTarget, error) {
	if len(values) == 0 {
		return nil, nil, fmt.Errorf("at least one Elasticsearch endpoint is required")
	}
	unique := map[egressTarget]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !isSupportedEndpointHost(u.Hostname()) {
			return nil, nil, fmt.Errorf("invalid unsupported Elasticsearch endpoint %q", value)
		}
		port := int64(443)
		if u.Port() != "" {
			port, err = strconv.ParseInt(u.Port(), 10, 64)
			if err != nil || port < 1 || port > 65535 {
				return nil, nil, fmt.Errorf("invalid unsupported Elasticsearch endpoint %q", value)
			}
		}
		target := egressTarget{u.Hostname(), port}
		unique[target] = struct{}{}
		normalized = append(normalized, value)
	}
	targets := make([]egressTarget, 0, len(unique))
	for target := range unique {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].host != targets[j].host {
			return targets[i].host < targets[j].host
		}
		return targets[i].port < targets[j].port
	})
	return normalized, targets, nil
}

func isSupportedEndpointHost(host string) bool {
	return net.ParseIP(host) != nil || validateDNSName(host, "host") == nil
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

func edotGatewayIngressPolicy(identityTelemetryEnabled bool) map[string]any {
	ingress := []any{map[string]any{"from": []any{map[string]any{"podSelector": appSelector(collectorAppLabel)}}, "ports": resources.NetworkPolicyPorts("TCP", 4317, 4318)}}
	if identityTelemetryEnabled {
		ingress = append(ingress, map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{
			"app":                          "keycloak",
			"app.kubernetes.io/instance":   keycloakconfig.InstanceName,
			"app.kubernetes.io/managed-by": "keycloak-operator",
		}}}}, "ports": resources.NetworkPolicyPorts("TCP", 4317)})
	}
	return map[string]any{"podSelector": appSelector(edotGatewayAppLabel), "policyTypes": []any{"Ingress"}, "ingress": ingress}
}

func edotGatewayEgressPolicy(targets []egressTarget) map[string]any {
	rules := make([]any, 0, len(targets)+2)
	if slices.ContainsFunc(targets, func(target egressTarget) bool { return net.ParseIP(target.host) == nil }) {
		rules = append(rules, dnsEgress())
	}
	rules = append(rules, elasticsearchEgress(targets)...)
	return map[string]any{"podSelector": appSelector(edotGatewayAppLabel), "policyTypes": []any{"Egress"}, "egress": rules}
}

// elasticsearchEgress allows the Elasticsearch endpoints and Elasticsearch on
// the cluster nodes (TCP 9200). An endpoint given by IP gets an ipBlock. A
// NetworkPolicy can select neither a DNS name nor the nodes, so the other
// ports, and openPorts, are open to any destination.
func elasticsearchEgress(targets []egressTarget, openPorts ...int64) []any {
	open := append([]int64{9200}, openPorts...)
	rules := make([]any, 0, len(targets)+1)
	for _, target := range targets {
		if net.ParseIP(target.host) == nil {
			open = append(open, target.port)
			continue
		}
		rules = append(rules, map[string]any{
			"to":    []any{map[string]any{"ipBlock": map[string]any{"cidr": hostCIDR(target.host)}}},
			"ports": resources.NetworkPolicyPorts("TCP", target.port),
		})
	}
	slices.Sort(open)
	return append(rules, map[string]any{"ports": resources.NetworkPolicyPorts("TCP", slices.Compact(open)...)})
}

// hostCIDR returns the single-address CIDR of ip in canonical form, which the
// API server's CIDR validation expects.
func hostCIDR(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed.To4() != nil {
		return parsed.String() + "/32"
	}
	return parsed.String() + "/128"
}

func edotGatewayInputVersions(configVersion, apiKeyVersion, rootCAVersion string) map[string]string {
	return map[string]string{"neteye.cloud/config-resource-version": configVersion, "neteye.cloud/api-key-resource-version": apiKeyVersion, "neteye.cloud/root-ca-resource-version": rootCAVersion}
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
  elasticsearch/otel:
    endpoints: ${ELASTICSEARCH_ENDPOINTS}
    api_key: "${ELASTICSEARCH_API_KEY}"
    tls: {ca_file: /etc/neteye/root-ca/ca.crt}
extensions:
  health_check: {endpoint: 0.0.0.0:13133}
service:
  extensions: [health_check]
  pipelines:
    logs: {receivers: [otlp], processors: [batch, elasticapm], exporters: [elasticapm, elasticsearch/otel]}
    metrics: {receivers: [otlp], processors: [batch, elasticapm], exporters: [elasticapm, elasticsearch/otel]}
    traces: {receivers: [otlp], processors: [batch, elasticapm], exporters: [elasticapm, elasticsearch/otel]}
    metrics/aggregated-otel-metrics: {receivers: [elasticapm], exporters: [elasticsearch/otel]}
`
