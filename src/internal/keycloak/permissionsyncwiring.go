// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package keycloak

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloakconfig"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// RootCATrustConfigMapName carries only the public certificate of the
	// trusted CA, for the Keycloak truststore. The CA Secret also holds the
	// CA private key, which must never be mounted into Keycloak.
	RootCATrustConfigMapName = "neteye-root-ca-trust"
	// rootCATrustKey is a .crt file name, the form the Keycloak truststore
	// directory loads.
	rootCATrustKey = "neteye-root-ca.crt"
	// truststoreName names the truststore in the Keycloak instance.
	truststoreName = "neteye-root-ca"
	// GatewayEgressPolicyName lets Keycloak call PermissionSync, and itself,
	// through the shared Gateway.
	GatewayEgressPolicyName = "neteye-kc-gateway-egress"
	// callerClientSecretBytes is the entropy of the generated client secret.
	callerClientSecretBytes = 32
)

// loginSyncWiring is what the Keycloak instance needs to run the login-sync
// authenticator against PermissionSync.
type loginSyncWiring struct {
	// overallDeadlineMilliseconds is PermissionSync's request deadline, which
	// the authenticator's own timeout must exceed.
	overallDeadlineMilliseconds int64
	// rootCASecretName is the Secret whose tls.crt signs the Gateway
	// certificates, or empty to trust only the system roots.
	rootCASecretName string
}

func loginSyncWiringFor(spec neteye.NetEyePermissionSyncSpec) loginSyncWiring {
	return loginSyncWiring{
		overallDeadlineMilliseconds: spec.Request.EffectiveOverallDeadlineMilliseconds(),
		rootCASecretName:            spec.EffectiveRootCASecretName(),
	}
}

// ensureCallerClientSecret generates the login-sync client secret once. An
// existing value is never replaced: the authenticator and the Keycloak client
// both hold it, and rotating it is an explicit act, not a side effect of a
// reconciliation.
func (c *Component) ensureCallerClientSecret(ctx context.Context, namespace string, owner *metav1.OwnerReference) error {
	key := types.NamespacedName{Namespace: namespace, Name: permissionsyncconfig.CallerClientSecretName}
	if err := c.client.Get(ctx, key, &corev1.Secret{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get the login-sync client secret: %w", err)
	}
	random := make([]byte, callerClientSecretBytes)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate the login-sync client secret: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: permissionsyncconfig.CallerClientSecretName},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{permissionsyncconfig.CallerClientSecretKey: []byte(base64.RawURLEncoding.EncodeToString(random))},
	}
	if owner != nil {
		secret.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	if err := c.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the login-sync client secret: %w", err)
	}
	return nil
}

// ensureRootCATrust copies the public certificate of the trusted CA into the
// ConfigMap the Keycloak truststore reads. It reports false while the CA
// Secret is missing.
func (c *Component) ensureRootCATrust(ctx context.Context, namespace, rootCASecretName string, owner metav1.OwnerReference) (bool, string, error) {
	ca := &corev1.Secret{}
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: rootCASecretName}, ca); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("waiting for the trusted CA Secret %q", rootCASecretName), nil
		}
		return false, "", fmt.Errorf("get the trusted CA Secret %q: %w", rootCASecretName, err)
	}
	certificate := ca.Data[corev1.TLSCertKey]
	if len(certificate) == 0 {
		return false, fmt.Sprintf("the trusted CA Secret %q has no %s", rootCASecretName, corev1.TLSCertKey), nil
	}
	if err := resources.EnsureConfigMap(ctx, c.client, namespace, RootCATrustConfigMapName, map[string]string{rootCATrustKey: string(certificate)}, owner); err != nil {
		return false, "", fmt.Errorf("ensure the trusted CA ConfigMap: %w", err)
	}
	return true, "", nil
}

// loginSyncOptions configures the login-sync authenticator. The client secret
// is a Secret reference, so it never appears in the Keycloak resource.
//
// Both endpoints are HTTPS through the shared Gateway: the authenticator
// refuses plaintext credential-bearing endpoints, and allow-insecure-http is
// a managed option the operator never sets.
func loginSyncOptions(wiring loginSyncWiring) []any {
	tokenEndpoint := "https://" + RouteHostname + HTTPRelativePath + "/realms/" + masterRealm + "/protocol/openid-connect/token"
	timeout := wiring.overallDeadlineMilliseconds + permissionsyncconfig.CallerTimeoutMarginMilliseconds
	return []any{
		nameValue(keycloakconfig.LoginSyncServiceEndpointOption, permissionsyncconfig.SyncURL()),
		nameValue(keycloakconfig.LoginSyncClientIDOption, permissionsyncconfig.CallerClientID),
		map[string]any{
			"name": keycloakconfig.LoginSyncClientSecretOption,
			"secret": map[string]any{
				"name": permissionsyncconfig.CallerClientSecretName,
				"key":  permissionsyncconfig.CallerClientSecretKey,
			},
		},
		nameValue(keycloakconfig.LoginSyncTokenEndpointOption, tokenEndpoint),
		nameValue(keycloakconfig.LoginSyncHTTPTimeoutOption, strconv.FormatInt(timeout, 10)),
	}
}

// loginSyncTruststores trusts the CA that signs the Gateway certificates.
func loginSyncTruststores(wiring loginSyncWiring) map[string]any {
	if wiring.rootCASecretName == "" {
		return nil
	}
	return map[string]any{
		truststoreName: map[string]any{"configMap": map[string]any{"name": RootCATrustConfigMapName}},
	}
}

// EnsureGatewayEgressPolicy lets the Keycloak pods reach PermissionSync and
// their own token endpoint through the shared Gateway.
//
// Cilium checks a pod's traffic through the Gateway twice: against the pod's
// egress to the Gateway itself (the ingress entity) and against its egress to
// the backend the Gateway routes to. Both are needed, which is why the
// backends appear here even though the pods never reach them directly.
func (c *Component) EnsureGatewayEgressPolicy(ctx context.Context, namespace string, owner *metav1.OwnerReference) error {
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{
		GVK:  ciliumNetworkPolicyGVK(),
		Name: GatewayEgressPolicyName, Namespace: namespace,
		Spec:  keycloakGatewayEgressPolicySpec(namespace),
		Owner: owner,
	})
	return err
}

func keycloakGatewayEgressPolicySpec(namespace string) map[string]any {
	port := func(value int64) map[string]any {
		return map[string]any{"ports": []any{map[string]any{"port": strconv.FormatInt(value, 10), "protocol": "TCP"}}}
	}
	return map[string]any{
		"endpointSelector": map[string]any{"matchLabels": keycloakCiliumWorkloadLabels()},
		"egress": []any{
			map[string]any{"toEntities": []any{"ingress"}, "toPorts": []any{port(443)}},
			map[string]any{
				"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{
					"k8s:app":                         permissionsyncconfig.WorkloadAppLabel,
					"k8s:io.kubernetes.pod.namespace": namespace,
				}}},
				"toPorts": []any{port(int64(permissionsyncconfig.ListenerPort))},
			},
			map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": keycloakCiliumWorkloadLabels()}}, "toPorts": []any{port(HTTPPort)}},
		},
	}
}
