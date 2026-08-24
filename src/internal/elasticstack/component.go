// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package elasticstack reconciles the shared Elastic Stack OpenTelemetry Collector.
package elasticstack

import (
	"context"
	"encoding/json"
	"fmt"

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
	ConfigMapName          = "otel-collector-config"
	VariablesConfigMapName = "otel-collector-variables"
	DeploymentName         = "otel-collector"
	ServiceName            = "otel-collector-service"
	GRPCRouteName          = "otel-collector-route"
	HTTPRouteName          = "otel-collector-crosstenant-route"
)

type Component struct {
	client client.Client
	log    logr.Logger
}

func NewComponent(c client.Client, log logr.Logger) *Component {
	return &Component{client: c, log: log}
}

// EnsureResources checks user-managed credentials first, then creates all collector resources.
func (c *Component) EnsureResources(ctx context.Context, namespace string, config neteye.NetEyeElasticStackSpec, identityHostname, gatewayNamespace, gatewayName string, owner metav1.OwnerReference) (bool, string, error) {
	apiKey := &corev1.Secret{}
	if ready, message, err := c.requireSecret(ctx, namespace, config.APIKeySecret.Name, apiKey); err != nil || !ready {
		return ready, message, err
	}
	if len(apiKey.Data[config.APIKeySecret.Key]) == 0 {
		return false, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", config.APIKeySecret.Name, config.APIKeySecret.Key, namespace), nil
	}
	basicAuth := &corev1.Secret{}
	if ready, message, err := c.requireSecret(ctx, namespace, config.BasicAuthSecretName, basicAuth); err != nil || !ready {
		return ready, message, err
	}
	if len(basicAuth.Data["htpasswd"]) == 0 {
		return false, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", config.BasicAuthSecretName, "htpasswd", namespace), nil
	}
	rootCA := &corev1.ConfigMap{}
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: config.RootCAConfigMapName}, rootCA); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("required user-managed ConfigMap %q is missing in namespace %q", config.RootCAConfigMapName, namespace), nil
		}
		return false, "", err
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, ConfigMapName, map[string]string{"otel-collector-config.yaml": collectorConfig}, owner); err != nil {
		return false, "", err
	}
	endpoints, err := json.Marshal(config.ElasticsearchEndpoints)
	if err != nil {
		return false, "", err
	}
	issuer := config.OIDCIssuerURL
	if issuer == "" {
		issuer = "https://" + identityHostname + "/auth/realms/master"
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, VariablesConfigMapName, map[string]string{"ELASTICSEARCH_ENDPOINTS": string(endpoints), "OIDC_ISSUER": issuer}, owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureDeployment(ctx, c.client, deployment(namespace, config), owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureService(ctx, c.client, service(namespace), owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureGRPCRoute(ctx, c.client, namespace, GRPCRouteName, gatewayNamespace, gatewayName, config.GRPCRouteHostname, ServiceName, 4317, &owner); err != nil {
		return false, "", err
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gatewayNamespace, gatewayName, []string{config.CrossTenantRouteHostname}, ServiceName, 4318, &owner); err != nil {
		return false, "", err
	}
	return true, "Elastic Stack OpenTelemetry Collector resources are ready", nil
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

// DeleteResources deletes only collector objects controlled by owner. User-managed
// prerequisite resources are deliberately not included.
func (c *Component) DeleteResources(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	for _, resource := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, ConfigMapName}, {schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, VariablesConfigMapName}, {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, DeploymentName}, {schema.GroupVersionKind{Version: "v1", Kind: "Service"}, ServiceName}, {schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"}, GRPCRouteName}, {schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}, HTTPRouteName},
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

func deployment(namespace string, config neteye.NetEyeElasticStackSpec) *appsv1.Deployment {
	labels := map[string]string{"app": "otel-collector"}
	mode := int32(0440)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: namespace}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "otel-collector-ca-bundle", Image: "docker.io/alpine:3.23.5", Command: []string{"/bin/sh", "-ec", caBundleCommand}, VolumeMounts: []corev1.VolumeMount{{Name: "otel-trusted-ca-bundle", MountPath: "/work"}, {Name: "host-trusted-ca-bundle", MountPath: "/input/system/tls-ca-bundle.pem", ReadOnly: true}, {Name: "neteye-root-ca", MountPath: "/input/neteye", ReadOnly: true}}}},
		Containers:     []corev1.Container{{Name: "otel-collector", Image: "docker.io/otel/opentelemetry-collector-contrib:0.156.0", Args: []string{"--config", "/etc/otel-collector-config/otel-collector-config.yaml"}, Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: 13133}, {Name: "otlp-grpc", ContainerPort: 4317}, {Name: "otlp-http", ContainerPort: 4318}}, EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: VariablesConfigMapName}}}}, Env: []corev1.EnvVar{{Name: "ELASTICSEARCH_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: config.APIKeySecret.Name}, Key: config.APIKeySecret.Key}}}}, VolumeMounts: []corev1.VolumeMount{{Name: "otel-config", MountPath: "/etc/otel-collector-config", ReadOnly: true}, {Name: "otel-trusted-ca-bundle", MountPath: "/etc/pki/tls/certs/ca-bundle.crt", SubPath: "ca-bundle.pem", ReadOnly: true}, {Name: "otel-basicauth", MountPath: "/etc/otel-collector-basicauth", ReadOnly: true}}, StartupProbe: probe(5, 30), ReadinessProbe: probe(10, 3), LivenessProbe: probe(10, 3)}},
		Volumes:        []corev1.Volume{{Name: "otel-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName}}}}, {Name: "otel-trusted-ca-bundle", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "host-trusted-ca-bundle", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", Type: ptr.To(corev1.HostPathFile)}}}, {Name: "neteye-root-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: config.RootCAConfigMapName}}}}, {Name: "otel-basicauth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: config.BasicAuthSecretName, DefaultMode: &mode}}}},
	}}}}
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
