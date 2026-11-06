// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/keycloak"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// RootCASecretName is the root CA of NetEye in the shared namespace. Its
	// certificate is public, so the configuration file carries it.
	RootCASecretName = "neteye-root-ca"
	rootCASecretKey  = corev1.TLSCertKey
)

// Input is what one reconciliation of the PermissionSync of a tenant needs.
type Input struct {
	// Tenant is the tenant. PermissionSync runs in its namespace.
	Tenant *neteye.NetEyeTenant
	// IdentityHostname is the public hostname of Keycloak, from the NetEye
	// resource. The trusted issuer is built from it.
	IdentityHostname string
	// Image is the PermissionSync image of the NetEye release.
	Image string
	// CABundle is the optional ConfigMap with extra CA certificates in the
	// shared namespace, from the NetEye resource.
	CABundle resources.CABundleRef
}

// State is the result of one reconciliation.
type State string

const (
	// StateReady means that the Pods are ready: they serve and can verify
	// tokens of the realm.
	StateReady State = "Ready"
	// StateWaiting means that a prerequisite or the rollout is not complete.
	StateWaiting State = "Waiting"
	// StateInvalid means that the configuration cannot be deployed.
	StateInvalid State = "Invalid"
)

// Outcome describes the state of the PermissionSync of a tenant.
type Outcome struct {
	State   State
	Reason  string
	Message string
}

// Ensure deploys the PermissionSync of a tenant: the configuration ConfigMap,
// the Deployment, the Service and the network policy, all owned by the tenant.
// Keycloak does not call PermissionSync yet, so nothing changes in Keycloak.
func Ensure(ctx context.Context, c client.Client, in Input) (Outcome, error) {
	tenant := in.Tenant
	spec := &tenant.Spec.PermissionSync
	if errors := neteye.ValidatePermissionSync(spec); len(errors) > 0 {
		return Outcome{State: StateInvalid, Reason: "InvalidConfiguration", Message: errors.ToAggregate().Error()}, nil
	}
	ports := endpointPorts(spec)

	rootCA, missing, err := secretValue(ctx, c, keycloak.WorkloadNamespace, RootCASecretName, rootCASecretKey)
	if err != nil {
		return Outcome{}, err
	}
	if missing != "" {
		return Outcome{State: StateWaiting, Reason: "WaitingForRootCA", Message: missing}, nil
	}
	trust := []string{string(rootCA)}
	bundle, err := caBundleCertificates(ctx, c, in.CABundle)
	if err != nil {
		return Outcome{}, err
	}
	if bundle != "" {
		trust = append(trust, bundle)
	}
	hash := sha256.New()
	if spec.GLPI != nil {
		credentials := spec.GLPI.CredentialsSecretRef
		for _, key := range []string{credentials.EffectiveAppTokenKey(), credentials.EffectiveUserTokenKey()} {
			value, missing, err := secretValue(ctx, c, tenant.Namespace, credentials.Name, key)
			if err != nil {
				return Outcome{}, err
			}
			if missing != "" {
				return Outcome{State: StateWaiting, Reason: "WaitingForGLPICredentials", Message: missing}, nil
			}
			hashField(hash, value)
		}
	}

	realm := spec.EffectiveRealm(tenant.Name)
	config, err := renderConfiguration(spec, in.IdentityHostname, realm, trust)
	if err != nil {
		return Outcome{}, err
	}
	hashField(hash, config)
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)

	if _, err := resources.Apply(ctx, c, resources.ObjectDefinition{
		GVK: resources.NetworkPolicyGVK(), Name: NetworkPolicyName, Namespace: tenant.Namespace, Spec: networkPolicySpec(ports), Owner: &owner,
	}); err != nil {
		return Outcome{}, fmt.Errorf("apply the PermissionSync network policy: %w", err)
	}
	if err := resources.EnsureConfigMap(ctx, c, tenant.Namespace, ConfigMapName, map[string]string{ConfigFileName: string(config)}, owner); err != nil {
		return Outcome{}, fmt.Errorf("apply the PermissionSync configuration: %w", err)
	}
	if err := resources.EnsureService(ctx, c, service(tenant.Namespace), owner); err != nil {
		return Outcome{}, fmt.Errorf("apply the PermissionSync Service: %w", err)
	}
	if err := resources.EnsureDeployment(ctx, c, deployment(tenant.Namespace, spec, in.Image, hex.EncodeToString(hash.Sum(nil))), owner); err != nil {
		return Outcome{}, fmt.Errorf("apply the PermissionSync Deployment: %w", err)
	}
	ready, message, err := resources.IsDeploymentReady(ctx, c, tenant.Namespace, DeploymentName)
	if err != nil {
		return Outcome{}, err
	}
	if !ready {
		return Outcome{State: StateWaiting, Reason: "Deploying", Message: fmt.Sprintf("%s; a Pod becomes ready when it can read the keys of the realm %q from Keycloak", message, realm)}, nil
	}
	return Outcome{State: StateReady, Reason: "Available", Message: fmt.Sprintf("PermissionSync is ready and trusts the tokens of the realm %q", realm)}, nil
}

// endpointPorts returns the ports of the configured endpoints. The validation
// before it has already refused an endpoint without a valid port.
func endpointPorts(spec *neteye.NetEyeTenantPermissionSyncSpec) []int32 {
	var endpoints []string
	if spec.Provider != nil {
		endpoints = append(endpoints, spec.Provider.Endpoint)
	}
	if spec.GLPI != nil {
		endpoints = append(endpoints, spec.GLPI.Endpoint)
	}
	ports := make([]int32, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if port, err := neteye.PermissionSyncEndpointPort(endpoint); err == nil {
			ports = append(ports, port)
		}
	}
	return ports
}

// secretValue reads one key of a Secret. A missing Secret or key is not an
// error: it returns a message that names them, never a value.
func secretValue(ctx context.Context, c client.Client, namespace, name, key string) ([]byte, string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("waiting for the Secret %s/%s", namespace, name), nil
		}
		return nil, "", fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}
	value := secret.Data[key]
	if len(value) == 0 {
		return nil, fmt.Sprintf("waiting for the key %q in the Secret %s/%s", key, namespace, name), nil
	}
	return value, "", nil
}

// caBundleCertificates returns the certificates of the optional CA bundle
// ConfigMap as one PEM bundle. A missing ConfigMap or key gives no
// certificate. Blocks that are not valid certificates are left out, because
// PermissionSync does not start with a trust anchor that it cannot read.
func caBundleCertificates(ctx context.Context, c client.Client, ref resources.CABundleRef) (string, error) {
	if ref.Name == "" {
		return "", nil
	}
	configMap := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: keycloak.WorkloadNamespace, Name: ref.Name}, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get ConfigMap %s/%s: %w", keycloak.WorkloadNamespace, ref.Name, err)
	}
	var certificates []byte
	rest := []byte(configMap.Data[ref.Key])
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			continue
		}
		certificates = append(certificates, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})...)
	}
	return string(certificates), nil
}

// hashField adds one length-prefixed value, so that two different sequences
// of values never give the same input.
func hashField(hash io.Writer, value []byte) {
	_, _ = fmt.Fprintf(hash, "%d:", len(value))
	_, _ = hash.Write(value)
}
