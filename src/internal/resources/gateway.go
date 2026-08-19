// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package resources

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	HTTPToHTTPSRedirectRouteName = "redirect-http-to-https"
	GatewayWildcardDNSName       = "*.rke2.neteyelocal"
	gatewayHTTPListenerName      = "http"
	gatewayHTTPSListenerName     = "https"
)

// EnsureGatewayTLSCertificate ensures the wildcard TLS certificate used by the
// Gateway HTTPS listener exists and writes the referenced Gateway TLS Secret.
func EnsureGatewayTLSCertificate(ctx context.Context, client client.Client, namespace string, secretName string, issuerRef CertificateIssuerRef, owner metav1.OwnerReference) error {
	return EnsureCertificate(ctx, client, namespace, secretName, secretName, GatewayWildcardDNSName, []string{GatewayWildcardDNSName}, issuerRef, owner)
}

// EnsureGateway ensures a Gateway API Gateway exists in the NetEye namespace.
// Existing Gateways are adopted when they have no conflicting controller owner.
func EnsureGateway(ctx context.Context, client client.Client, namespace string, name string, gatewayClassName string, annotations map[string]string, tlsSecretName string, owner metav1.OwnerReference) error {
	desiredSpec := map[string]any{
		"gatewayClassName": gatewayClassName,
		"listeners": []any{
			map[string]any{
				"name":     gatewayHTTPListenerName,
				"protocol": "HTTP",
				"port":     int64(80),
				"allowedRoutes": map[string]any{
					"namespaces": map[string]any{
						"from": "Same",
					},
					"kinds": routeKinds(),
				},
			},
			map[string]any{
				"name":     gatewayHTTPSListenerName,
				"protocol": "HTTPS",
				"port":     int64(443),
				"allowedRoutes": map[string]any{
					"namespaces": map[string]any{
						"from": "Same",
					},
					"kinds": routeKinds(),
				},
				"tls": map[string]any{
					"mode": "Terminate",
					"certificateRefs": []any{
						map[string]any{
							"kind":  "Secret",
							"group": "",
							"name":  tlsSecretName,
						},
					},
				},
			},
		},
	}
	if len(annotations) > 0 {
		desiredSpec["infrastructure"] = map[string]any{
			"annotations": stringMapToInterfaces(annotations),
		}
	}

	outcome, err := Apply(ctx, client, ObjectDefinition{
		GVK:       gatewayGVK(),
		Name:      name,
		Namespace: namespace,
		Spec:      desiredSpec,
		Owner:     &owner,
	})
	if err != nil {
		return err
	}
	log := logf.FromContext(ctx)
	switch outcome {
	case Unchanged:
		log.V(1).Info("Gateway had no drift", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "tlsSecret", tlsSecretName)
	case Updated:
		log.V(1).Info("Gateway reconciled", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "tlsSecret", tlsSecretName)
	case Created:
		log.Info("Gateway created", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "tlsSecret", tlsSecretName)
	}
	return nil
}

// EnsureHTTPToHTTPSRedirectRoute ensures the default Gateway HTTP listener
// redirects cleartext traffic to HTTPS.
func EnsureHTTPToHTTPSRedirectRoute(ctx context.Context, client client.Client, namespace string, gatewayName string, owner metav1.OwnerReference) error {
	desiredSpec := map[string]any{
		"parentRefs": []any{
			map[string]any{
				"name":        gatewayName,
				"sectionName": gatewayHTTPListenerName,
			},
		},
		"rules": []any{
			map[string]any{
				"filters": []any{
					map[string]any{
						"type": "RequestRedirect",
						"requestRedirect": map[string]any{
							"scheme":     "https",
							"statusCode": int64(301),
						},
					},
				},
			},
		},
	}

	return ensureHTTPRouteSpec(ctx, client, namespace, HTTPToHTTPSRedirectRouteName, desiredSpec, owner)
}

func gatewayGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "Gateway",
	}
}

func routeKinds() []any {
	return []any{
		map[string]any{
			"kind": "HTTPRoute",
		},
		map[string]any{
			"kind": "GRPCRoute",
		},
	}
}

func stringMapToInterfaces(values map[string]string) map[string]any {
	items := make(map[string]any, len(values))
	for key, value := range values {
		items[key] = value
	}
	return items
}
