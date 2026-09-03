// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package elasticstack reconciles the shared Elastic Stack OpenTelemetry Collector.
package elasticstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	ConfigMapName              = "otel-collector-config"
	VariablesConfigMapName     = "otel-collector-variables"
	DeploymentName             = "otel-collector"
	ServiceName                = "otel-collector-service"
	GRPCRouteName              = "otel-collector-route"
	HTTPRouteName              = "otel-collector-crosstenant-route"
	IngressPolicyName          = "neteye-otel-collector-ingress"
	EgressPolicyName           = "neteye-otel-collector-egress"
	GRPCRouteHostname          = "otel-collector.neteyelocal"
	CrossTenantRouteHostname   = "otel-collector-crosstenant.neteyelocal"
	GRPCListenerName           = "otel-collector"
	CrossTenantListenerName    = "otel-collector-crosstenant"
	GRPCTLSCertName            = "otel-collector-tls"
	GRPCTLSSecretName          = "otel-collector-tls-secret"
	CrossTenantTLSCertName     = "otel-collector-crosstenant-tls"
	CrossTenantTLSSecretName   = "otel-collector-crosstenant-tls-secret"
	DefaultAPIKeySecretName    = "otel-collector-api-key"
	DefaultAPIKeySecretKey     = "api_key"
	DefaultBasicAuthSecretName = "otel-collector-basicauth"
	DefaultRootCASecretName    = "neteye-root-ca"
	GatewayHTTPSPort           = "443"
)

type Component struct {
	client client.Client
	log    logr.Logger
}

func NewComponent(c client.Client, log logr.Logger) *Component {
	return &Component{client: c, log: log}
}

// EnsureResources checks user-managed prerequisites first, then creates Elastic Stack feature module resources.
func (c *Component) EnsureResources(ctx context.Context, namespace string, config neteye.NetEyeElasticStackSpec, identityHostname, gatewayNamespace, gatewayName, collectorImage string, issuerRef resources.CertificateIssuerRef, owner metav1.OwnerReference) (bool, string, error) {
	if config.OTelCollector == nil {
		return false, "Elastic Stack feature module configuration is incomplete: otelCollector is required when enabled", nil
	}
	collector := config.OTelCollector
	references := resolvedReferences(collector)
	apiKey := &corev1.Secret{}
	if ready, message, err := c.requireSecret(ctx, namespace, references.apiKeySecret.Name, apiKey); err != nil || !ready {
		return ready, message, err
	}
	if len(apiKey.Data[references.apiKeySecret.Key]) == 0 {
		return false, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", references.apiKeySecret.Name, references.apiKeySecret.Key, namespace), nil
	}
	basicAuth := &corev1.Secret{}
	if ready, message, err := c.requireSecret(ctx, namespace, references.basicAuthSecretName, basicAuth); err != nil || !ready {
		return ready, message, err
	}
	if len(basicAuth.Data["htpasswd"]) == 0 {
		return false, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", references.basicAuthSecretName, "htpasswd", namespace), nil
	}
	rootCA := &corev1.Secret{}
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: references.rootCASecretName}, rootCA); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("required user-managed Secret %q is missing in namespace %q", references.rootCASecretName, namespace), nil
		}
		return false, "", err
	}
	if len(rootCA.Data["tls.crt"]) == 0 {
		return false, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", references.rootCASecretName, "tls.crt", namespace), nil
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, ConfigMapName, map[string]string{"otel-collector-config.yaml": collectorConfig}, owner); err != nil {
		return false, "", err
	}
	endpoints, err := json.Marshal(collector.ElasticsearchEndpoints)
	if err != nil {
		return false, "", err
	}
	issuer := collector.OIDCIssuerURL
	if issuer == "" {
		issuer = "https://" + identityHostname + "/auth/realms/master"
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, VariablesConfigMapName, map[string]string{"ELASTICSEARCH_ENDPOINTS": string(endpoints), "OIDC_ISSUER": issuer}, owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureDeployment(ctx, c.client, deployment(namespace, *collector, collectorImage, references), owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureService(ctx, c.client, service(namespace), owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureCertificate(ctx, c.client, namespace, GRPCTLSCertName, GRPCTLSSecretName, GRPCRouteHostname, []string{GRPCRouteHostname}, issuerRef, &owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureCertificate(ctx, c.client, namespace, CrossTenantTLSCertName, CrossTenantTLSSecretName, CrossTenantRouteHostname, []string{CrossTenantRouteHostname}, issuerRef, &owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureGRPCRoute(ctx, c.client, namespace, GRPCRouteName, gatewayNamespace, gatewayName, GRPCListenerName, GRPCRouteHostname, ServiceName, 4317, &owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gatewayNamespace, gatewayName, CrossTenantListenerName, []string{CrossTenantRouteHostname}, ServiceName, 4318, &owner); err != nil {
		return false, "", err
	}
	if err := c.ensureNetworkPolicies(ctx, namespace, collector.ElasticsearchEndpoints, issuer, owner); err != nil {
		return false, "", err
	}
	for _, certificate := range []string{GRPCTLSCertName, CrossTenantTLSCertName} {
		certificateReady, certificateMessage, err := resources.IsCertificateReady(ctx, c.client, namespace, certificate)
		if err != nil {
			return false, "", err
		}
		if !certificateReady {
			return false, certificateMessage, nil
		}
	}
	ready, message, err := resources.IsDeploymentReady(ctx, c.client, namespace, DeploymentName)
	if err != nil {
		return false, "", err
	}
	if !ready {
		return false, message, nil
	}
	return true, "Elastic Stack feature module resources are ready", nil
}

func (c *Component) requireSecret(ctx context.Context, namespace, name string, secret *corev1.Secret) (bool, string, error) {
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("required user-managed Secret %q is missing in namespace %q", name, namespace), nil
		}
		return false, "", err
	}
	return true, "", nil
}

// DeleteResources deletes only Elastic Stack feature module objects controlled by owner. User-managed
// prerequisite resources are deliberately not included.
func (c *Component) DeleteResources(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	for _, resource := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, ConfigMapName}, {schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, VariablesConfigMapName}, {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, DeploymentName}, {schema.GroupVersionKind{Version: "v1", Kind: "Service"}, ServiceName}, {schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"}, GRPCRouteName}, {schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}, HTTPRouteName}, {schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}, GRPCTLSCertName}, {schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}, CrossTenantTLSCertName}, {schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, IngressPolicyName}, {schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, EgressPolicyName},
	} {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(resource.gvk)
		if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resource.name}, object); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if !controlledBy(object, owner) {
			continue
		}
		if err := c.client.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func controlledBy(object client.Object, owner metav1.OwnerReference) bool {
	for _, reference := range object.GetOwnerReferences() {
		if reference.UID == owner.UID && reference.Controller != nil && *reference.Controller {
			return true
		}
	}
	return false
}

type references struct {
	apiKeySecret        neteye.NetEyeSecretKeySelector
	basicAuthSecretName string
	rootCASecretName    string
}

func resolvedReferences(config *neteye.NetEyeOtelCollectorSpec) references {
	resolved := references{apiKeySecret: neteye.NetEyeSecretKeySelector{Name: DefaultAPIKeySecretName, Key: DefaultAPIKeySecretKey}, basicAuthSecretName: DefaultBasicAuthSecretName, rootCASecretName: DefaultRootCASecretName}
	if config.APIKeySecret != nil {
		resolved.apiKeySecret.Name = config.APIKeySecret.Name
		resolved.apiKeySecret.Key = config.APIKeySecret.Key
	}
	if strings.TrimSpace(config.BasicAuthSecretName) != "" {
		resolved.basicAuthSecretName = config.BasicAuthSecretName
	}
	if strings.TrimSpace(config.RootCASecretName) != "" {
		resolved.rootCASecretName = config.RootCASecretName
	}
	return resolved
}

func deployment(namespace string, config neteye.NetEyeOtelCollectorSpec, collectorImage string, references references) *appsv1.Deployment {
	labels := map[string]string{"app": "otel-collector"}
	mode := int32(0440)
	replicas := config.Replicas
	if replicas == 0 {
		replicas = 1
	}
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: namespace}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(replicas), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "otel-collector-ca-bundle", Image: "docker.io/alpine:3.23.5", Command: []string{"/bin/sh", "-ec", caBundleCommand}, VolumeMounts: []corev1.VolumeMount{{Name: "otel-trusted-ca-bundle", MountPath: "/work"}, {Name: "host-trusted-ca-bundle", MountPath: "/input/system/tls-ca-bundle.pem", ReadOnly: true}, {Name: "neteye-root-ca", MountPath: "/input/neteye", ReadOnly: true}}}},
		Containers:     []corev1.Container{{Name: "otel-collector", Image: collectorImage, Args: []string{"--config", "/etc/otel-collector-config/otel-collector-config.yaml"}, Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: 13133}, {Name: "otlp-grpc", ContainerPort: 4317}, {Name: "otlp-http", ContainerPort: 4318}}, EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: VariablesConfigMapName}}}}, Env: []corev1.EnvVar{{Name: "ELASTICSEARCH_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: references.apiKeySecret.Name}, Key: references.apiKeySecret.Key}}}}, VolumeMounts: []corev1.VolumeMount{{Name: "otel-config", MountPath: "/etc/otel-collector-config", ReadOnly: true}, {Name: "otel-trusted-ca-bundle", MountPath: "/etc/pki/tls/certs/ca-bundle.crt", SubPath: "ca-bundle.pem", ReadOnly: true}, {Name: "otel-basicauth", MountPath: "/etc/otel-collector-basicauth", ReadOnly: true}}, StartupProbe: probe(5, 30), ReadinessProbe: probe(10, 3), LivenessProbe: probe(10, 3)}},
		Volumes:        []corev1.Volume{{Name: "otel-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName}}}}, {Name: "otel-trusted-ca-bundle", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "host-trusted-ca-bundle", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", Type: ptr.To(corev1.HostPathFile)}}}, {Name: "neteye-root-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: references.rootCASecretName, Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "ca.crt"}}}}}, {Name: "otel-basicauth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: references.basicAuthSecretName, DefaultMode: &mode}}}},
	}}}}
}

func (c *Component) ensureNetworkPolicies(ctx context.Context, namespace string, endpoints []string, issuer string, owner metav1.OwnerReference) error {
	if _, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, Namespace: namespace, Name: IngressPolicyName, Owner: &owner, Spec: ingressPolicySpec()}); err != nil {
		return err
	}
	targets, err := egressTargets(endpoints, issuer)
	if err != nil {
		return err
	}
	_, err = resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, Namespace: namespace, Name: EgressPolicyName, Owner: &owner, Spec: egressPolicySpec(targets)})
	return err
}

type egressTarget struct{ host, port string }

func egressTargets(endpoints []string, issuer string) ([]egressTarget, error) {
	byTarget := map[string]egressTarget{}
	for _, value := range append(append([]string{}, endpoints...), issuer) {
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("parse Elastic Stack egress target %q", value)
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		byTarget[u.Hostname()+":"+port] = egressTarget{host: u.Hostname(), port: port}
	}
	targets := make([]egressTarget, 0, len(byTarget))
	for _, target := range byTarget {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].host+":"+targets[i].port < targets[j].host+":"+targets[j].port })
	return targets, nil
}

func ingressPolicySpec() map[string]any {
	return map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"k8s:app": "otel-collector"}}, "ingress": []any{
		map[string]any{"fromEntities": []any{"ingress"}, "toPorts": []any{tcpPorts("4317", "4318")}},
		map[string]any{"fromEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts("13133")}},
	}}
}

func egressPolicySpec(targets []egressTarget) map[string]any {
	dnsRules := make([]any, 0, len(targets))
	byPort := map[string][]any{}
	for _, target := range targets {
		dnsRules = append(dnsRules, map[string]any{"matchName": target.host})
		byPort[target.port] = append(byPort[target.port], map[string]any{"matchName": target.host})
	}
	ports := make([]string, 0, len(byPort))
	for port := range byPort {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	egress := []any{
		map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "kube-system", "k8s:k8s-app": "kube-dns"}}}, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "53", "protocol": "TCP"}, map[string]any{"port": "53", "protocol": "UDP"}}, "rules": map[string]any{"dns": dnsRules}}}},
		map[string]any{"toEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts(GatewayHTTPSPort)}},
	}
	for _, port := range ports {
		egress = append(egress, map[string]any{"toFQDNs": byPort[port], "toPorts": []any{tcpPorts(port)}})
	}
	return map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"k8s:app": "otel-collector"}}, "egress": egress}
}

func tcpPorts(ports ...string) map[string]any {
	values := make([]any, 0, len(ports))
	for _, port := range ports {
		values = append(values, map[string]any{"port": port, "protocol": "TCP"})
	}
	return map[string]any{"ports": values}
}

func probe(period, failures int32) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromString("health")}}, PeriodSeconds: period, FailureThreshold: failures, TimeoutSeconds: 2}
}

func service(namespace string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ServiceName, Namespace: namespace}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "otel-collector"}, Ports: []corev1.ServicePort{{Name: "otlp-grpc", Protocol: corev1.ProtocolTCP, Port: 4317, TargetPort: intstr.FromInt32(4317), AppProtocol: ptr.To("kubernetes.io/h2c")}, {Name: "otlp-http", Protocol: corev1.ProtocolTCP, Port: 4318, TargetPort: intstr.FromInt32(4318)}}}}
}

const caBundleCommand = `tmp_bundle=/work/ca-bundle.pem.tmp
final_bundle=/work/ca-bundle.pem
cat /input/system/tls-ca-bundle.pem > "$tmp_bundle"
for cert in /input/neteye/*; do
  if [ -f "$cert" ]; then cat "$cert" >> "$tmp_bundle"; printf '\n' >> "$tmp_bundle"; fi
done
sed -e '/^#/d' -e '/^[[:space:]]*$/d' -e 's/TRUSTED //g' "$tmp_bundle" > "$final_bundle"
rm -f "$tmp_bundle"
chmod 755 /work
chmod 644 "$final_bundle"
echo "=== Init container completed successfully ==="`

const collectorConfig = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
        auth:
          authenticator: oidc
  otlp/crosstenant:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
        auth:
          authenticator: basicauth/crosstenant

extensions:
  oidc:
    issuer_url: "${OIDC_ISSUER}"
    audience: account
    username_claim: sub
  health_check:
    endpoint: 0.0.0.0:13133
  basicauth/crosstenant:
    htpasswd:
      file: /etc/otel-collector-basicauth/htpasswd

processors:
  batch:
    send_batch_size: 1000
    timeout: 1s
    send_batch_max_size: 1500
  batch/metrics:
    # Explicitly set to 0 to avoid splitting metrics requests
    send_batch_max_size: 0
    timeout: 1s
  attributes/tenant:
    actions:
      - key: data_stream.namespace
        from_context: auth.claims.tenant
        action: upsert
  transform/crosstenant:
    error_mode: ignore
    metric_statements:
      - context: resource
        statements:
          - set(attributes["data_stream.namespace"], attributes["icinga2.custom.tenant"]) where attributes["icinga2.custom.tenant"] != nil
          - set(attributes["data_stream.namespace"], "master") where attributes["data_stream.namespace"] == nil

service:
  telemetry:
    logs:
      level: debug
  extensions: [oidc, basicauth/crosstenant, health_check]
  pipelines:
    metrics:
      receivers: [otlp]
      processors: [attributes/tenant, batch/metrics]
      exporters: [elasticsearch/otel]
    logs:
      receivers: [otlp]
      processors: [attributes/tenant, batch]
      exporters: [elasticsearch/otel]
    traces:
      receivers: [otlp]
      processors: [attributes/tenant, batch]
      exporters: [elasticsearch/otel]
    metrics/crosstenant:
      receivers: [otlp/crosstenant]
      processors: [transform/crosstenant, batch/metrics]
      exporters: [elasticsearch/otel]

exporters:
  elasticsearch/otel:
    endpoints: ${ELASTICSEARCH_ENDPOINTS}
    api_key: "${ELASTICSEARCH_API_KEY}"
    tls:
      ca_file: /etc/pki/tls/certs/ca-bundle.crt
`
