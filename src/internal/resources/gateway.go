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
	gatewayHTTPListenerName      = "http"
	RouteKindHTTP                = "HTTPRoute"
	RouteKindGRPC                = "GRPCRoute"
)

// GatewayListener describes one per-component HTTPS listener on the shared
// Gateway, matched by SNI hostname and terminating TLS with its own Secret.
type GatewayListener struct {
	// Name is the listener name; routes attach to it via parentRef sectionName.
	Name string
	// Hostname is the SNI hostname served by this listener.
	Hostname string
	// TLSSecretName is the TLS Secret, in the Gateway namespace, terminating TLS.
	TLSSecretName string
	// RouteKind is the single route kind allowed on this listener.
	RouteKind string
}

// EnsureGateway ensures a Gateway API Gateway exists. It exposes the cleartext
// HTTP listener used for the HTTP->HTTPS redirect and one HTTPS listener per
// supplied GatewayListener. Existing Gateways are adopted when they have no
// conflicting controller owner.
func EnsureGateway(ctx context.Context, client client.Client, namespace string, name string, gatewayClassName string, annotations map[string]string, listeners []GatewayListener, owner metav1.OwnerReference) error {
	desiredListeners := []any{
		map[string]any{
			"name":     gatewayHTTPListenerName,
			"protocol": "HTTP",
			"port":     int64(80),
			"allowedRoutes": map[string]any{
				"namespaces": map[string]any{
					"from": "Same",
				},
				"kinds": routeKinds(RouteKindHTTP),
			},
		},
	}
	for _, listener := range listeners {
		desiredListeners = append(desiredListeners, map[string]any{
			"name":     listener.Name,
			"protocol": "HTTPS",
			"port":     int64(443),
			"hostname": listener.Hostname,
			"allowedRoutes": map[string]any{
				"namespaces": map[string]any{
					"from": "Same",
				},
				"kinds": routeKinds(listener.RouteKind),
			},
			"tls": map[string]any{
				"mode": "Terminate",
				"certificateRefs": []any{
					map[string]any{
						"kind":  "Secret",
						"group": "",
						"name":  listener.TLSSecretName,
					},
				},
			},
		})
	}

	desiredSpec := map[string]any{
		"gatewayClassName": gatewayClassName,
		"listeners":        desiredListeners,
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
		log.V(1).Info("Gateway had no drift", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "listeners", len(desiredListeners))
	case Updated:
		log.V(1).Info("Gateway reconciled", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "listeners", len(desiredListeners))
	case Created:
		log.Info("Gateway created", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "listeners", len(desiredListeners))
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

	return ensureHTTPRouteSpec(ctx, client, namespace, HTTPToHTTPSRedirectRouteName, desiredSpec, &owner)
}

func gatewayGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "Gateway",
	}
}

func routeKinds(kinds ...string) []any {
	items := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		items = append(items, map[string]any{"group": "gateway.networking.k8s.io", "kind": kind})
	}
	return items
}

func stringMapToInterfaces(values map[string]string) map[string]any {
	items := make(map[string]any, len(values))
	for key, value := range values {
		items[key] = value
	}
	return items
}
