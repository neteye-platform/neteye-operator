// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package elasticstack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

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
	EDOTGatewayServiceName = "otel-edot-gateway"
	collectorAppLabel      = "otel-collector"
)

// OTelCollectorComponent reconciles only the authenticated telemetry ingress.
// It deliberately does not own Elasticsearch credentials or endpoint egress.
type OTelCollectorComponent struct{ client client.Client }

func NewOTelCollectorComponent(c client.Client) *OTelCollectorComponent {
	return &OTelCollectorComponent{client: c}
}

func (c *OTelCollectorComponent) Ensure(ctx context.Context, namespace string, spec *neteye.NetEyeOtelCollectorSpec, identityHostname, gatewayNamespace, gatewayName, image, caBundleImage string, issuerRef resources.CertificateIssuerRef, owner metav1.OwnerReference) Outcome {
	if spec == nil {
		return degradedOutcome(ReasonInvalidConfiguration, "otel collector configuration is required", nil)
	}
	if spec.Replicas < 0 {
		return degradedOutcome(ReasonInvalidConfiguration, "otel collector replicas must be at least one", nil)
	}
	if err := validateDNSName(identityHostname, "identity hostname"); err != nil {
		return degradedOutcome(ReasonInvalidConfiguration, err.Error(), nil)
	}
	basicAuthVersion, err := requiredSecretResourceVersion(ctx, c.client, namespace, spec.EffectiveBasicAuthSecretName(), "htpasswd")
	if err != nil {
		return prerequisiteOutcome(err)
	}
	rootCAVersion, err := requiredSecretResourceVersion(ctx, c.client, namespace, spec.EffectiveRootCASecretName(), "tls.crt")
	if err != nil {
		return prerequisiteOutcome(err)
	}
	if image == "" {
		return degradedOutcome(ReasonInvalidConfiguration, "otel collector resolved image is required", nil)
	}
	issuer := "https://" + identityHostname + "/auth/realms/master"
	if _, err := url.Parse(issuer); err != nil { // identity hostname was validated; retain defensive failure semantics.
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, ConfigMapName, map[string]string{"otel-collector-config.yaml": collectorConfig}, owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	versions, err := collectorInputVersions(ctx, c.client, namespace, basicAuthVersion, rootCAVersion)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureDeployment(ctx, c.client, collectorDeployment(namespace, spec, image, caBundleImage, issuer, versions), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := deleteOwnedResources(ctx, c.client, namespace, owner, collectorLegacyInventory()); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureService(ctx, c.client, collectorService(namespace), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureCertificate(ctx, c.client, namespace, GRPCTLSCertName, GRPCTLSSecretName, GRPCRouteHostname, []string{GRPCRouteHostname}, issuerRef, &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureCertificate(ctx, c.client, namespace, CrossTenantTLSCertName, CrossTenantTLSSecretName, CrossTenantRouteHostname, []string{CrossTenantRouteHostname}, issuerRef, &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureGRPCRoute(ctx, c.client, namespace, GRPCRouteName, gatewayNamespace, gatewayName, GRPCListenerName, GRPCRouteHostname, ServiceName, 4317, &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gatewayNamespace, gatewayName, CrossTenantListenerName, []string{CrossTenantRouteHostname}, ServiceName, 4318, &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := c.ensurePolicies(ctx, namespace, identityHostname, owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	for _, name := range []string{GRPCTLSCertName, CrossTenantTLSCertName} {
		ready, message, err := resources.IsCertificateReady(ctx, c.client, namespace, name)
		if err != nil || !ready {
			if err != nil {
				return degradedOutcome(ReasonReconcileFailed, message, err)
			}
			return progressingOutcome(ReasonCertificateNotReady, message)
		}
	}
	for _, route := range []struct{ name, kind, section string }{{GRPCRouteName, "GRPCRoute", GRPCListenerName}, {HTTPRouteName, "HTTPRoute", CrossTenantListenerName}} {
		ready, message, err := routeReady(ctx, c.client, namespace, route.name, route.kind, expectedParent{group: "gateway.networking.k8s.io", kind: "Gateway", namespace: gatewayNamespace, name: gatewayName, section: route.section})
		if err != nil || !ready {
			if err != nil {
				return degradedOutcome(ReasonReconcileFailed, message, err)
			}
			return progressingOutcome(ReasonRouteNotReady, message)
		}
	}
	ready, message, err := resources.IsDeploymentReady(ctx, c.client, namespace, DeploymentName)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !ready {
		return progressingOutcome(ReasonDeploymentNotAvailable, message)
	}
	return readyOutcome("OpenTelemetry Collector is ready")
}

// Delete removes only collector-owned objects. It never deletes EDOT resources
// or externally managed credential and CA Secrets.
func (c *OTelCollectorComponent) Delete(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	return deleteOwnedResources(ctx, c.client, namespace, owner, append(collectorResourceInventory(), collectorLegacyInventory()...))
}

func (c *OTelCollectorComponent) ensurePolicies(ctx context.Context, namespace, identityHostname string, owner metav1.OwnerReference) error {
	if _, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: ciliumPolicyGVK, Namespace: namespace, Name: IngressPolicyName, Owner: &owner, Spec: collectorIngressPolicy()}); err != nil {
		return err
	}
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: ciliumPolicyGVK, Namespace: namespace, Name: EgressPolicyName, Owner: &owner, Spec: collectorEgressPolicy(namespace, identityHostname)})
	return err
}

func collectorDeployment(namespace string, spec *neteye.NetEyeOtelCollectorSpec, image, caBundleImage, oidcIssuer string, annotations map[string]string) *appsv1.Deployment {
	labels := map[string]string{"app": collectorAppLabel}
	mode := int32(0440)
	replicas := spec.EffectiveReplicas()
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: namespace}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(replicas), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}, Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "otel-collector-ca-bundle", Image: caBundleImage, Command: []string{"/bin/sh", "-ec", caBundleCommand}, VolumeMounts: []corev1.VolumeMount{{Name: "trusted-ca", MountPath: "/work"}, {Name: "host-ca", MountPath: "/input/system/tls-ca-bundle.pem", ReadOnly: true}, {Name: "root-ca", MountPath: "/input/neteye", ReadOnly: true}}}},
		Containers:     []corev1.Container{{Name: "otel-collector", Image: image, Args: []string{"--config", "/etc/otel/config.yaml"}, Ports: collectorPorts(), Env: []corev1.EnvVar{{Name: "OIDC_ISSUER", Value: oidcIssuer}}, VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/otel", ReadOnly: true}, {Name: "trusted-ca", MountPath: "/etc/pki/tls/certs/ca-bundle.crt", SubPath: "ca-bundle.pem", ReadOnly: true}, {Name: "basic-auth", MountPath: "/etc/otel/basicauth", ReadOnly: true}}, StartupProbe: healthProbe(5, 30), ReadinessProbe: healthProbe(10, 3), LivenessProbe: healthProbe(10, 3)}},
		Volumes:        []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName}, Items: []corev1.KeyToPath{{Key: "otel-collector-config.yaml", Path: "config.yaml"}}}}}, {Name: "trusted-ca", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "host-ca", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", Type: ptr.To(corev1.HostPathFile)}}}, {Name: "root-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: spec.EffectiveRootCASecretName(), Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "ca.crt"}}}}}, {Name: "basic-auth", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: spec.EffectiveBasicAuthSecretName(), DefaultMode: &mode}}}},
	}}}}
}

func collectorPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{{Name: "health", ContainerPort: 13133}, {Name: "otlp-grpc", ContainerPort: 4317}, {Name: "otlp-http", ContainerPort: 4318}}
}

func healthProbe(period, failures int32) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromString("health")}}, PeriodSeconds: period, FailureThreshold: failures, TimeoutSeconds: 2}
}

func collectorService(namespace string) *corev1.Service {
	return telemetryService(namespace, ServiceName, collectorAppLabel)
}

func telemetryService(namespace, name, app string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": app}, Ports: []corev1.ServicePort{{Name: "otlp-grpc", Protocol: corev1.ProtocolTCP, Port: 4317, TargetPort: intstr.FromInt32(4317), AppProtocol: ptr.To("kubernetes.io/h2c")}, {Name: "otlp-http", Protocol: corev1.ProtocolTCP, Port: 4318, TargetPort: intstr.FromInt32(4318)}}}}
}

var ciliumPolicyGVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

func collectorIngressPolicy() map[string]any {
	return map[string]any{"endpointSelector": labelsFor(collectorAppLabel), "ingress": []any{map[string]any{"fromEntities": []any{"ingress"}, "toPorts": []any{tcpPorts("4317", "4318")}}, map[string]any{"fromEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts("13133")}}}}
}

func collectorEgressPolicy(namespace, identityHostname string) map[string]any {
	return map[string]any{"endpointSelector": labelsFor(collectorAppLabel), "egress": []any{collectorDNSEgress(), map[string]any{"toEndpoints": []any{namespaceScopedEndpoint(edotGatewayAppLabel, namespace)}, "toPorts": []any{tcpPorts("4317")}}, fqdnEgress(identityHostname, "443"), map[string]any{"toEntities": []any{"host", "remote-node"}, "toPorts": []any{tcpPorts("443")}}}}
}

func labelsFor(app string) map[string]any {
	return map[string]any{"matchLabels": map[string]any{"k8s:app": app}}
}

func namespaceScopedEndpoint(app, namespace string) map[string]any {
	return map[string]any{"matchLabels": map[string]any{"k8s:app": app, "k8s:io.kubernetes.pod.namespace": namespace}}
}

func tcpPorts(ports ...string) map[string]any {
	values := make([]any, 0, len(ports))
	for _, p := range ports {
		values = append(values, map[string]any{"port": p, "protocol": "TCP"})
	}
	return map[string]any{"ports": values}
}

func dnsEgress(names []string) map[string]any {
	dns := make([]any, 0, len(names))
	for _, name := range names {
		dns = append(dns, map[string]any{"matchName": name})
	}
	return map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "kube-system", "k8s:k8s-app": "kube-dns"}}}, "toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "53", "protocol": "TCP"}, map[string]any{"port": "53", "protocol": "UDP"}}, "rules": map[string]any{"dns": dns}}}}
}

func collectorDNSEgress() map[string]any {
	return map[string]any{
		"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "kube-system", "k8s:k8s-app": "kube-dns"}}},
		"toPorts":     []any{map[string]any{"ports": []any{map[string]any{"port": "53", "protocol": "TCP"}, map[string]any{"port": "53", "protocol": "UDP"}}, "rules": map[string]any{"dns": []any{map[string]any{"matchPattern": "*"}}}}},
	}
}

func fqdnEgress(host, port string) map[string]any {
	return map[string]any{"toFQDNs": []any{map[string]any{"matchName": host}}, "toPorts": []any{tcpPorts(port)}}
}

func requiredSecretResourceVersion(ctx context.Context, c client.Client, namespace, name, key string) (string, error) {
	if err := validateDNSName(name, "Secret name"); err != nil {
		return "", prerequisiteError{ReasonInvalidConfiguration, err.Error()}
	}
	if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, " \t\r\n") {
		return "", prerequisiteError{ReasonInvalidConfiguration, "secret key must be a non-empty value without whitespace"}
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", prerequisiteError{ReasonSecretNotFound, fmt.Sprintf("required user-managed Secret %q is missing in namespace %q", name, namespace)}
		}
		return "", err
	}
	if len(secret.Data[key]) == 0 {
		return "", prerequisiteError{ReasonSecretKeyMissing, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", name, key, namespace)}
	}
	return secret.ResourceVersion, nil
}

func configMapResourceVersion(ctx context.Context, c client.Client, namespace, name string) (string, error) {
	object := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		return "", err
	}
	return object.ResourceVersion, nil
}

func collectorInputVersions(ctx context.Context, c client.Client, namespace, basicAuthVersion, rootCAVersion string) (map[string]string, error) {
	configVersion, err := configMapResourceVersion(ctx, c, namespace, ConfigMapName)
	if err != nil {
		return nil, err
	}
	return map[string]string{"neteye.cloud/config-resource-version": configVersion, "neteye.cloud/basic-auth-resource-version": basicAuthVersion, "neteye.cloud/root-ca-resource-version": rootCAVersion}, nil
}

type expectedParent struct{ group, kind, namespace, name, section string }

func routeReady(ctx context.Context, c client.Client, namespace, routeName, kind string, expected expectedParent) (bool, string, error) {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: kind})
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: routeName}, route); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("%s %s is not created", kind, routeName), nil
		}
		return false, "", err
	}
	parents, found, err := unstructured.NestedSlice(route.Object, "status", "parents")
	if err != nil {
		return false, "", err
	}
	if !found || len(parents) == 0 {
		return false, fmt.Sprintf("waiting for Gateway %s/%s to report %s %s status", expected.namespace, expected.name, kind, routeName), nil
	}
	expectedGroup := expected.group
	if expectedGroup == "" {
		expectedGroup = "gateway.networking.k8s.io"
	}
	for _, rawParent := range parents {
		parent, ok := rawParent.(map[string]any)
		if !ok {
			continue
		}
		ref, ok := parent["parentRef"].(map[string]any)
		if !ok {
			continue
		}
		group, _, _ := unstructured.NestedString(ref, "group")
		if group == "" {
			group = "gateway.networking.k8s.io"
		}
		parentKind, _, _ := unstructured.NestedString(ref, "kind")
		if parentKind == "" {
			parentKind = "Gateway"
		}
		parentNamespace, _, _ := unstructured.NestedString(ref, "namespace")
		parentName, _, _ := unstructured.NestedString(ref, "name")
		section, _, _ := unstructured.NestedString(ref, "sectionName")
		if group != expectedGroup || parentKind != expected.kind || parentNamespace != expected.namespace || parentName != expected.name || section != expected.section {
			continue
		}
		conditions, _, err := unstructured.NestedSlice(parent, "conditions")
		if err != nil {
			return false, "", err
		}
		for _, conditionType := range []string{"Accepted", "ResolvedRefs"} {
			var condition map[string]any
			for _, rawCondition := range conditions {
				candidate, ok := rawCondition.(map[string]any)
				if ok && candidate["type"] == conditionType {
					condition = candidate
					break
				}
			}
			if condition == nil {
				return false, fmt.Sprintf("waiting for %s %s %s condition from Gateway %s/%s", kind, routeName, conditionType, expected.namespace, expected.name), nil
			}
			observed, found, err := unstructured.NestedInt64(condition, "observedGeneration")
			if err != nil {
				return false, "", err
			}
			if found && observed < route.GetGeneration() {
				return false, fmt.Sprintf("%s %s %s status is stale", kind, routeName, conditionType), nil
			}
			status, _, _ := unstructured.NestedString(condition, "status")
			if status != "True" {
				message, _, _ := unstructured.NestedString(condition, "message")
				if message == "" {
					message = fmt.Sprintf("waiting for %s %s %s condition from Gateway %s/%s", kind, routeName, conditionType, expected.namespace, expected.name)
				}
				return false, message, nil
			}
		}
		return true, "", nil
	}
	return false, fmt.Sprintf("waiting for Gateway %s/%s to accept %s %s", expected.namespace, expected.name, kind, routeName), nil
}

type prerequisiteError struct{ reason, message string }

func (e prerequisiteError) Error() string { return e.message }
func prerequisiteOutcome(err error) Outcome {
	var e prerequisiteError
	if errors.As(err, &e) {
		return degradedOutcome(e.reason, e.message, e)
	}
	return degradedOutcome(ReasonReconcileFailed, err.Error(), err)
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
chmod 644 "$final_bundle"`

const collectorConfig = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
        auth: {authenticator: oidc}
  otlp/crosstenant:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
        auth: {authenticator: basicauth/crosstenant}
extensions:
  oidc: {issuer_url: "${OIDC_ISSUER}", audience: account, username_claim: sub}
  basicauth/crosstenant: {htpasswd: {file: /etc/otel/basicauth/htpasswd}}
  health_check: {endpoint: 0.0.0.0:13133}
processors:
  attributes/tenant: {actions: [{key: data_stream.namespace, from_context: auth.claims.tenant, action: upsert}]}
  transform/crosstenant:
    error_mode: ignore
    metric_statements:
      - context: resource
        statements:
          - set(attributes["data_stream.namespace"], attributes["icinga2.custom.tenant"]) where attributes["icinga2.custom.tenant"] != nil
          - set(attributes["data_stream.namespace"], "master") where attributes["data_stream.namespace"] == nil
  batch:
    send_batch_size: 1000
    timeout: 1s
    send_batch_max_size: 1500
  batch/metrics:
    send_batch_max_size: 0
    timeout: 1s
exporters:
  otlp/edot: {endpoint: otel-edot-gateway:4317, tls: {insecure: true}}
service:
  extensions: [oidc, basicauth/crosstenant, health_check]
  pipelines:
    metrics: {receivers: [otlp], processors: [attributes/tenant, batch/metrics], exporters: [otlp/edot]}
    logs: {receivers: [otlp], processors: [attributes/tenant, batch], exporters: [otlp/edot]}
    traces: {receivers: [otlp], processors: [attributes/tenant, batch], exporters: [otlp/edot]}
    metrics/crosstenant: {receivers: [otlp/crosstenant], processors: [transform/crosstenant, batch/metrics], exporters: [otlp/edot]}
`
