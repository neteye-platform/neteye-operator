// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package keycloak provides a component that manages the Keycloak Operator extension and its associated resources.
package keycloak

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
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
	RouteHostname       = "keycloak.neteyelocal"
	GatewayListenerName = "keycloak"
	TLSCertificateName  = "keycloak-tls"
	TLSSecretName       = "keycloak-tls-secret"
	InstanceName        = "neteye-kc"
	ServiceName         = "neteye-kc-service"
	EgressPolicyName    = "neteye-kc-egress"
	IngressPolicyName   = "neteye-kc-ingress"
	HostPolicyName      = "neteye-kc-host-management"
	CacheConfigMapName  = "neteye-kc-cache-config"
	CacheConfigMapKey   = "cache-ispn.xml"
	// InfinispanClusterName isolates this Keycloak instance's JGroups/Infinispan
	// cluster from any other Keycloak sharing the same database schema (e.g. a
	// bare-metal instance kept alive during a migration). Both instances write
	// JDBC_PING discovery rows keyed by cluster name; a shared default name
	// ("ISPN") makes each side's independently-formed view register its own
	// coordinator row, which the "cluster health check" reads as a split brain.
	InfinispanClusterName = "neteye-k8s-ispn"
	HTTPPort              = int64(8080)
	HTTPRelativePath      = "/auth"
	KubeSystemNamespace   = "kube-system"
	// OperatorSystemNamespace runs the NetEye operator itself, which reaches the
	// Keycloak Admin API in-cluster to reconcile KeycloakClient resources.
	OperatorSystemNamespace = "neteye-system"
	// AdminSecretName is the Secret the Keycloak Operator creates with the
	// bootstrap admin credentials of the Keycloak instance.
	AdminSecretName        = InstanceName + "-initial-admin"
	AdminSecretUsernameKey = "username"
	AdminSecretPasswordKey = "password"

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

	// AdminAPIFactory builds the Admin API client. Tests substitute it to point
	// at a stub server; when nil, NewAdminAPI is used.
	AdminAPIFactory AdminAPIFactory

	adminOnce     sync.Once
	adminProvider *AdminProvider
}

// admin returns the shared Admin API provider, built on first use so that
// AdminAPIFactory can still be set after construction.
func (c *Component) admin(namespace string) *AdminProvider {
	c.adminOnce.Do(func() {
		c.adminProvider = NewAdminProvider(c.client, namespace, c.AdminAPIFactory)
	})
	return c.adminProvider
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
func (c *Component) EnsureResources(ctx context.Context, namespace string, image string, identity neteye.NetEyeIdentitySpec, gatewayNamespace, gatewayRef string, issuerRef resources.CertificateIssuerRef, owner metav1.OwnerReference) (bool, string, error) {
	ctx = logf.IntoContext(ctx, c.log)
	if err := resources.EnsureCertificate(ctx, c.client, namespace, TLSCertificateName, TLSSecretName, RouteHostname, []string{RouteHostname}, issuerRef, &owner); err != nil {
		return false, "", fmt.Errorf("ensure tls certificate: %w", err)
	}
	if err := c.EnsureWorkloadNetworkPolicy(ctx, namespace, externalDatabasePort(identity.DBConnection), &owner); err != nil {
		return false, "", fmt.Errorf("ensure keycloak workload network policy: %w", err)
	}
	if err := c.EnsureCacheConfig(ctx, namespace, &owner); err != nil {
		return false, "", fmt.Errorf("ensure keycloak cache config: %w", err)
	}
	if err := c.EnsureIngressNetworkPolicy(ctx, namespace, &owner); err != nil {
		return false, "", fmt.Errorf("ensure keycloak ingress network policy: %w", err)
	}
	if err := c.EnsureHostManagementPolicy(ctx, namespace, &owner); err != nil {
		return false, "", fmt.Errorf("ensure keycloak host management policy: %w", err)
	}
	certificateReady, certificateMessage, err := resources.IsCertificateReady(ctx, c.client, namespace, TLSCertificateName)
	if err != nil {
		return false, "", fmt.Errorf("check tls certificate readiness: %w", err)
	}
	if !certificateReady {
		return false, certificateMessage, nil
	}
	if err := c.EnsureInstance(ctx, namespace, image, identity, &owner); err != nil {
		return false, "", fmt.Errorf("ensure keycloak instance: %w", err)
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gatewayNamespace, gatewayRef, GatewayListenerName, []string{RouteHostname}, ServiceName, HTTPPort, &owner); err != nil {
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

func (c *Component) EnsureInstance(ctx context.Context, namespace, image string, identity neteye.NetEyeIdentitySpec, owner *metav1.OwnerReference) error {
	outcome, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:       keycloakGVK(),
		Name:      InstanceName,
		Namespace: namespace,
		Spec:      keycloakInstanceSpec(image, identity),
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

func keycloakInstanceSpec(image string, identity neteye.NetEyeIdentitySpec) map[string]any {
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
		"networkPolicy": map[string]any{"enabled": false},
		"cache": map[string]any{
			"configMapFile": map[string]any{
				"name": CacheConfigMapName,
				"key":  CacheConfigMapKey,
			},
		},
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

func (c *Component) EnsureIngressNetworkPolicy(ctx context.Context, namespace string, owner *metav1.OwnerReference) error {
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  nativeNetworkPolicyGVK(),
		Name: IngressPolicyName, Namespace: namespace,
		Spec:  keycloakIngressNetworkPolicySpec(),
		Owner: owner,
	})
	return err
}

func keycloakIngressNetworkPolicySpec() map[string]any {
	return map[string]any{
		"podSelector": map[string]any{"matchLabels": keycloakWorkloadLabels()},
		"policyTypes": []any{"Ingress"},
		"ingress": []any{
			map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": keycloakWorkloadLabels()}}},
				"ports": []any{networkPort(7800, "TCP"), networkPort(57800, "TCP")},
			},
			// The operator calls the Keycloak Admin API to reconcile KeycloakClient
			// resources, so it needs its own way in through the default deny.
			map[string]any{
				"from":  []any{namespaceSelector(OperatorSystemNamespace)},
				"ports": []any{networkPort(int32(HTTPPort), "TCP")},
			},
		},
	}
}

func namespaceSelector(namespace string) map[string]any {
	return map[string]any{
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": namespace}},
	}
}

// EnsureCacheConfig ensures the ConfigMap holding a cache-ispn.xml that pins
// this instance's Infinispan/JGroups cluster name away from the shared
// default, so it never collides with another Keycloak (e.g. a legacy
// bare-metal instance kept alive during a migration) reusing the same
// database schema for JDBC_PING discovery.
func (c *Component) EnsureCacheConfig(ctx context.Context, namespace string, owner *metav1.OwnerReference) error {
	return resources.EnsureConfigMap(ctx, c.client, namespace, CacheConfigMapName, map[string]string{
		CacheConfigMapKey: cacheConfigXML(),
	}, *owner)
}

func cacheConfigXML() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<infinispan
        xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
        xsi:schemaLocation="urn:infinispan:config:16.0 https://infinispan.org/schemas/infinispan-config-16.0.xsd"
        xmlns="urn:infinispan:config:16.0">

    <cache-container name="keycloak">
        <transport lock-timeout="60000" cluster="%s"/>
    </cache-container>
</infinispan>
`, InfinispanClusterName)
}

func (c *Component) EnsureHostManagementPolicy(ctx context.Context, namespace string, owner *metav1.OwnerReference) error {
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  ciliumNetworkPolicyGVK(),
		Name: HostPolicyName, Namespace: namespace,
		Spec:  keycloakHostManagementPolicySpec(),
		Owner: owner,
	})
	return err
}

func keycloakHostManagementPolicySpec() map[string]any {
	return map[string]any{
		"endpointSelector": map[string]any{"matchLabels": keycloakCiliumWorkloadLabels()},
		"ingress": []any{
			map[string]any{
				"fromEntities": []any{"ingress"},
				"toPorts":      []any{map[string]any{"ports": []any{map[string]any{"port": "8080", "protocol": "TCP"}}}},
			},
			map[string]any{
				"fromEntities": []any{"host", "remote-node"},
				"toPorts":      []any{map[string]any{"ports": []any{map[string]any{"port": "9000", "protocol": "TCP"}}}},
			},
		},
	}
}

// EnsureWorkloadNetworkPolicy creates the Keycloak egress policy.
func (c *Component) EnsureWorkloadNetworkPolicy(ctx context.Context, namespace string, databasePort int32, owner *metav1.OwnerReference) error {
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  nativeNetworkPolicyGVK(),
		Name: EgressPolicyName, Namespace: namespace, Owner: owner,
		Spec: keycloakEgressNetworkPolicySpec(databasePort),
	})
	return err
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

func keycloakCiliumWorkloadLabels() map[string]any {
	return map[string]any{
		"k8s:app":                          "keycloak",
		"k8s:app.kubernetes.io/instance":   InstanceName,
		"k8s:app.kubernetes.io/managed-by": "keycloak-operator",
	}
}

func namespaceAndPodSelector(namespace string, podLabels map[string]any) map[string]any {
	return map[string]any{
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": namespace}},
		"podSelector":       map[string]any{"matchLabels": podLabels},
	}
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

func nativeNetworkPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"}
}

func ciliumNetworkPolicyGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}
}
