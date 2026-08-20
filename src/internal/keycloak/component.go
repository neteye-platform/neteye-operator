// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package keycloak provides a component that manages the Keycloak Operator extension and its associated resources.
package keycloak

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	OperatorNamespace   = "keycloak-system"
	WorkloadNamespace   = "neteye-tenant-shared"
	HTTPRouteName       = "keycloak"
	TLSCertificateName  = "keycloak-tls"
	TLSSecretName       = "keycloak-tls-secret"
	InstanceName        = "neteye-kc"
	ServiceName         = "neteye-kc-service"
	EgressPolicyName    = "neteye-kc-egress"
	HTTPPort            = int64(8080)
	HTTPRelativePath    = "/auth"
	KubeSystemNamespace = "kube-system"

	extensionName = "keycloak-operator"
	channel       = "fast"
	catalogName   = "operatorhubio"
)

const (
	operatorReconcileInterval = 10 * time.Minute
	operatorRetryInterval     = 10 * time.Second
	defaultDatabasePort       = 3306
)

// Component ensures Keycloak-related cluster and namespaced resources exist.
type Component struct {
	client client.Client
	log    logr.Logger
}

func NewComponent(client client.Client, log logr.Logger) *Component {
	return &Component{client: client, log: log}
}

func (c *Component) NeedLeaderElection() bool {
	return true
}

func (c *Component) Start(ctx context.Context) error {
	log := c.log

	for attempt := 1; ; attempt++ {
		err := c.installOperator(ctx)
		if err == nil {
			log.Info("keycloak operator extension is installed", "extension", extensionName, "namespace", OperatorNamespace)
			break
		}
		log.Error(err, "keycloak operator extension installation failed; retrying", "attempt", attempt, "retryAfter", operatorRetryInterval)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(operatorRetryInterval):
		}
	}

	ticker := time.NewTicker(operatorReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.installOperator(ctx); err != nil {
				log.Error(err, "keycloak operator extension reconciliation failed")
			}
		}
	}
}

func (c *Component) installOperator(ctx context.Context) error {
	if err := c.EnsureOperatorExtension(ctx); err != nil {
		return fmt.Errorf("ensure keycloak extension: %w", err)
	}
	return nil
}

func (c *Component) EnsureOperatorExtension(ctx context.Context) error {
	outcome, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  clusterExtensionGVK(),
		Name: extensionName,
		Spec: clusterExtensionSpec(),
	})
	if err != nil {
		return err
	}
	switch outcome {
	case resources.Updated:
		c.log.Info("keycloak operator ClusterExtension reconciled", "extension", extensionName, "namespace", OperatorNamespace)
	case resources.Created:
		c.log.Info("keycloak operator ClusterExtension created", "extension", extensionName, "namespace", OperatorNamespace)
	}
	return nil
}

func clusterExtensionSpec() map[string]any {
	return map[string]any{
		"namespace": OperatorNamespace,
		"source": map[string]any{
			"sourceType": "Catalog",
			"catalog": map[string]any{
				"packageName": extensionName,
				"channels":    []any{channel},
				"selector": map[string]any{
					"matchLabels": map[string]any{
						"olm.operatorframework.io/metadata.name": catalogName,
					},
				},
			},
		},
	}
}

// EnsureResources reconciles the identity component resources owned by the Keycloak
// integration: its TLS Certificate, Keycloak instance, and HTTPRoute.
func (c *Component) EnsureResources(ctx context.Context, namespace string, image string, identity neteye.NetEyeIdentitySpec, gatewayNamespace, gatewayRef string, issuerRef resources.CertificateIssuerRef) (bool, string, error) {
	ctx = logf.IntoContext(ctx, c.log)
	managementSourceCIDRs, err := c.managementSourceCIDRs(ctx)
	if err != nil {
		return false, "", fmt.Errorf("derive keycloak management source CIDRs: %w", err)
	}
	if err := resources.EnsureCertificate(ctx, c.client, namespace, TLSCertificateName, TLSSecretName, identity.Hostname, []string{identity.Hostname}, issuerRef, nil); err != nil {
		return false, "", fmt.Errorf("ensure tls certificate: %w", err)
	}
	if err := c.EnsureWorkloadNetworkPolicy(ctx, namespace, externalDatabasePort(identity.DBConnection), nil); err != nil {
		return false, "", fmt.Errorf("ensure keycloak workload network policy: %w", err)
	}
	certificateReady, certificateMessage, err := resources.IsCertificateReady(ctx, c.client, namespace, TLSCertificateName)
	if err != nil {
		return false, "", fmt.Errorf("check tls certificate readiness: %w", err)
	}
	if !certificateReady {
		return false, certificateMessage, nil
	}
	if err := c.EnsureInstance(ctx, namespace, image, identity, managementSourceCIDRs, nil); err != nil {
		return false, "", fmt.Errorf("ensure keycloak instance: %w", err)
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gatewayNamespace, gatewayRef, []string{identity.Hostname}, ServiceName, HTTPPort, nil); err != nil {
		return false, "", fmt.Errorf("ensure http route: %w", err)
	}
	return true, "Keycloak is Ready", nil
}

// IsReady reports whether the Keycloak Operator marked the Keycloak instance
// Ready. It does not wait; callers should requeue and check again later.
func (c *Component) IsReady(ctx context.Context, namespace string) (bool, string, error) {
	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(keycloakGVK())
	key := types.NamespacedName{Name: InstanceName, Namespace: namespace}
	if err := c.client.Get(ctx, key, kc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "waiting for Keycloak CR to be created", nil
		}
		return false, "", err
	}
	observedGeneration, found, err := unstructured.NestedInt64(kc.Object, "status", "observedGeneration")
	if err != nil {
		return false, "", err
	}
	if !found || observedGeneration < kc.GetGeneration() {
		return false, "waiting for Keycloak status to observe the latest generation", nil
	}

	return resources.ReadyConditionMessage(kc, "Keycloak")
}

func (c *Component) EnsureInstance(ctx context.Context, namespace, image string, identity neteye.NetEyeIdentitySpec, managementSourceCIDRs []string, owner *metav1.OwnerReference) error {
	outcome, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:       keycloakGVK(),
		Name:      InstanceName,
		Namespace: namespace,
		Spec:      keycloakInstanceSpec(image, identity, managementSourceCIDRs),
		Owner:     owner,
	})
	if err != nil {
		return err
	}
	switch outcome {
	case resources.Updated:
		c.log.Info("keycloak instance reconciled", "namespace", namespace, "instance", InstanceName, "image", image)
	case resources.Created:
		c.log.Info("keycloak instance created", "namespace", namespace, "instance", InstanceName, "image", image)
	}
	return nil
}

func keycloakInstanceSpec(image string, identity neteye.NetEyeIdentitySpec, managementSourceCIDRs []string) map[string]any {
	database := identity.DBConnection
	spec := map[string]any{
		"instances": int64(identityReplicas(identity)),
		"image":     image,
		"db": map[string]any{
			"vendor":   "mariadb",
			"host":     database.Host,
			"port":     int64(externalDatabasePort(database)),
			"database": database.DBName,
			"usernameSecret": map[string]any{
				"name": database.UsernameSecret.Name,
				"key":  database.UsernameSecret.Key,
			},
			"passwordSecret": map[string]any{
				"name": database.PasswordSecret.Name,
				"key":  database.PasswordSecret.Key,
			},
		},
		"http": map[string]any{
			"httpEnabled": true,
		},
		"ingress": map[string]any{
			"enabled": false,
		},
		"networkPolicy": keycloakNativeNetworkPolicy(managementSourceCIDRs),
		"hostname": map[string]any{
			"hostname":           resourceURI(identity.Hostname),
			"strict":             true,
			"backchannelDynamic": true,
		},
		"proxy": map[string]any{
			"headers": "xforwarded",
		},
		"additionalOptions": []any{
			map[string]any{
				"name":  "http-relative-path",
				"value": HTTPRelativePath,
			},
		},
	}
	if env := podExtraEnvVars(identity.PodExtraEnvVars); len(env) > 0 {
		spec["env"] = env
	}
	return spec
}

// EnsureWorkloadNetworkPolicy creates the egress policy. Ingress is delegated
// to Keycloak's native networkPolicy.
func (c *Component) EnsureWorkloadNetworkPolicy(ctx context.Context, namespace string, databasePort int32, owner *metav1.OwnerReference) error {
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"},
		Name: EgressPolicyName, Namespace: namespace, Owner: owner,
		Spec: keycloakEgressNetworkPolicySpec(databasePort),
	})
	return err
}

func keycloakNativeNetworkPolicy(managementSourceCIDRs []string) map[string]any {
	return map[string]any{
		"enabled": true,
		"http": []any{map[string]any{
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": KubeSystemNamespace}},
			"podSelector":       map[string]any{"matchLabels": map[string]any{"k8s-app": "cilium-envoy"}},
		}},
		"management": ipBlockSources(managementSourceCIDRs),
	}
}

func keycloakEgressNetworkPolicySpec(databasePort int32) map[string]any {
	return map[string]any{
		"podSelector": map[string]any{"matchLabels": keycloakWorkloadLabels()},
		"policyTypes": []any{"Egress"},
		"egress": []any{
			// Standard NetworkPolicy cannot select an external database by DNS name.
			// Restrict the interim rule to the configured database TCP port only.
			map[string]any{"ports": []any{networkPort(databasePort, "TCP")}},
			map[string]any{"to": []any{namespaceAndPodSelector(KubeSystemNamespace, map[string]any{"k8s-app": "kube-dns"})}, "ports": []any{networkPort(53, "TCP"), networkPort(53, "UDP")}},
			map[string]any{"to": []any{map[string]any{"podSelector": map[string]any{"matchLabels": keycloakWorkloadLabels()}}}, "ports": []any{networkPort(7800, "TCP"), networkPort(57800, "TCP")}},
		},
	}
}

func ipBlockSources(cidrs []string) []any {
	sources := make([]any, 0, len(cidrs))
	for _, cidr := range cidrs {
		sources = append(sources, map[string]any{"ipBlock": map[string]any{"cidr": cidr}})
	}
	return sources
}

func networkPort(port int32, protocol string) map[string]any {
	return map[string]any{"protocol": protocol, "port": int64(port)}
}

func keycloakWorkloadLabels() map[string]any {
	return map[string]any{
		"app":                          "keycloak",
		"app.kubernetes.io/instance":   InstanceName,
		"app.kubernetes.io/managed-by": "keycloak-operator",
	}
}

func namespaceAndPodSelector(namespace string, podLabels map[string]any) map[string]any {
	return map[string]any{
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": namespace}},
		"podSelector":       map[string]any{"matchLabels": podLabels},
	}
}

func (c *Component) managementSourceCIDRs(ctx context.Context) ([]string, error) {
	nodes := &corev1.NodeList{}
	if err := c.client.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("list Kubernetes nodes: %w", err)
	}

	prefixes := make(map[string]struct{})
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type != corev1.NodeInternalIP {
				continue
			}
			ip, err := netip.ParseAddr(address.Address)
			if err != nil || !ip.IsValid() {
				continue
			}
			ip = ip.Unmap()
			prefix := netip.PrefixFrom(ip, ip.BitLen()).String()
			prefixes[prefix] = struct{}{}
		}
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("no valid InternalIP addresses found on Kubernetes nodes")
	}

	result := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		result = append(result, prefix)
	}
	sort.Strings(result)
	return result, nil
}

func identityReplicas(identity neteye.NetEyeIdentitySpec) int32 {
	if identity.Replicas == 0 {
		return 1
	}
	return identity.Replicas
}

func resourceURI(hostname string) string {
	return "https://" + hostname + HTTPRelativePath
}

func externalDatabasePort(database neteye.NetEyeDBConnectionSpec) int32 {
	if database.Port == 0 {
		return defaultDatabasePort
	}
	return database.Port
}

func podExtraEnvVars(values []string) []any {
	env := make([]any, 0, len(values))
	for _, raw := range values {
		name, value, hasValue := strings.Cut(raw, "=")
		entry := map[string]any{"name": name}
		if hasValue {
			entry["value"] = value
		}
		env = append(env, entry)
	}
	return env
}

func keycloakGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "k8s.keycloak.org",
		Version: "v2beta1",
		Kind:    "Keycloak",
	}
}

func clusterExtensionGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	}
}
