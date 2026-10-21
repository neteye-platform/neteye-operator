// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package permissionsync owns the PermissionSync lifecycle boundary: the
// rendered configuration document, the workload serving it, and the network
// boundary around it. The Keycloak objects a technical caller authenticates
// with are owned by the keycloak package, so neither package imports the
// other.
package permissionsync

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

// Component reconciles the PermissionSync service.
type Component struct{ client client.Client }

func NewComponent(c client.Client) *Component {
	return &Component{client: c}
}

// Ensure reconciles the configuration document, the workload, its Service, and
// its network boundary, then reports whether the service is serving.
//
// A missing or unusable user-managed Secret is reported as a degraded outcome
// rather than rendering a document without it: PermissionSync accepts an
// invalid component section by leaving that component unavailable, which would
// turn a provisioning mistake into silently unreconciled logins.
func (c *Component) Ensure(ctx context.Context, namespace string, spec *neteye.NetEyePermissionSyncSpec, identityHostname, image string, gateway resources.RouteParent, issuerRef resources.CertificateIssuerRef, owner metav1.OwnerReference) Outcome {
	egressTargets, err := validateSpec(spec, identityHostname)
	if err != nil {
		return degradedOutcome(ReasonInvalidConfiguration, err.Error(), nil)
	}
	if image == "" {
		return degradedOutcome(ReasonInvalidConfiguration, "permissionsync resolved image is required", nil)
	}
	input := configurationInput{spec: spec, identityHostname: identityHostname}
	if name := spec.EffectiveRootCASecretName(); name != "" {
		anchor, err := requiredSecretValue(ctx, c.client, namespace, name, "tls.crt")
		if err != nil {
			return prerequisiteOutcome(err)
		}
		input.trustAnchorsPEM = []string{anchor}
	}
	if spec.GLPI != nil {
		credentials := spec.GLPI.EffectiveCredentialsSecret()
		if input.glpiAppToken, err = requiredSecretValue(ctx, c.client, namespace, credentials.Name, credentials.AppTokenKey); err != nil {
			return prerequisiteOutcome(err)
		}
		if input.glpiUserToken, err = requiredSecretValue(ctx, c.client, namespace, credentials.Name, credentials.UserTokenKey); err != nil {
			return prerequisiteOutcome(err)
		}
	}
	rendered, err := renderConfiguration(input)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	version, err := resources.EnsureSecret(ctx, c.client, namespace, ConfigSecretName, map[string][]byte{ConfigFileName: rendered}, owner)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	annotations := map[string]string{configVersionAnnotation: version}
	if err := resources.EnsureDeployment(ctx, c.client, permissionSyncDeployment(namespace, spec, image, annotations), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureService(ctx, c.client, permissionSyncService(namespace), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := c.ensurePolicies(ctx, namespace, identityHostname, egressTargets, owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	// The listener is plaintext, so the shared Gateway terminates TLS in front
	// of it under its own name: the login-sync authenticator refuses a
	// plaintext endpoint for the credentials it sends.
	if err := resources.EnsureCertificate(ctx, c.client, namespace, TLSCertificateName, TLSSecretName, permissionsyncconfig.Hostname, []string{permissionsyncconfig.Hostname}, issuerRef, &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	if err := resources.EnsureHTTPRoute(ctx, c.client, namespace, HTTPRouteName, gateway.Namespace, gateway.Name, GatewayListenerName, []string{permissionsyncconfig.Hostname}, ServiceName, int64(ListenerPort), &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", err)
	}
	certificateReady, message, err := resources.IsCertificateReady(ctx, c.client, namespace, TLSCertificateName)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !certificateReady {
		return progressingOutcome(ReasonCertificateNotReady, message)
	}
	gateway.Section = GatewayListenerName
	routeReady, message, err := resources.IsRouteReady(ctx, c.client, namespace, HTTPRouteName, "HTTPRoute", gateway)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !routeReady {
		return progressingOutcome(ReasonRouteNotReady, message)
	}
	ready, message, err := resources.IsDeploymentReady(ctx, c.client, namespace, DeploymentName)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !ready {
		return progressingOutcome(ReasonDeploymentNotAvailable, message)
	}
	return readyOutcome("PermissionSync is ready")
}

func (c *Component) ensurePolicies(ctx context.Context, namespace, identityHostname string, targets []endpointTarget, owner metav1.OwnerReference) error {
	if _, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: ciliumPolicyGVK, Namespace: namespace, Name: IngressPolicyName, Owner: &owner, Spec: ingressPolicy()}); err != nil {
		return err
	}
	_, err := resources.Apply(ctx, c.client, resources.ObjectDefinition{GVK: ciliumPolicyGVK, Namespace: namespace, Name: EgressPolicyName, Owner: &owner, Spec: egressPolicy(identityHostname, targets)})
	return err
}

// requiredSecretValue reads one key from a user-managed Secret, reporting a
// missing Secret or an empty key as a prerequisite failure.
func requiredSecretValue(ctx context.Context, c client.Client, namespace, name, key string) (string, error) {
	if err := validateDNSName(name, "Secret name"); err != nil {
		return "", prerequisiteError{ReasonInvalidConfiguration, err.Error()}
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", prerequisiteError{ReasonSecretNotFound, fmt.Sprintf("required user-managed Secret %q is missing in namespace %q", name, namespace)}
		}
		return "", err
	}
	value := secret.Data[key]
	if len(value) == 0 {
		return "", prerequisiteError{ReasonSecretKeyMissing, fmt.Sprintf("required user-managed Secret %q is missing non-empty key %q in namespace %q", name, key, namespace)}
	}
	return string(value), nil
}
