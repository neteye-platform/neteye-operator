/*
Copyright 2026 Wuerth IT | Italy.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	HTTPToHTTPSRedirectRouteName = "redirect-http-to-https"
	GatewayWildcardDNSName       = "*.rke2.neteyelocal"
	gatewayHTTPListenerName      = "http"
	gatewayHTTPSListenerName     = "https"
)

// EnsureGatewayTLSCertificate ensures the wildcard TLS certificate used by the
// Gateway HTTPS listener exists and writes the referenced Gateway TLS Secret.
func EnsureGatewayTLSCertificate(ctx context.Context, client client.Client, log *logr.Logger, namespace string, secretName string, issuerRef CertificateIssuerRef, owner metav1.OwnerReference) error {
	return EnsureCertificate(ctx, client, log, namespace, secretName, secretName, GatewayWildcardDNSName, []string{GatewayWildcardDNSName}, issuerRef, owner)
}

// EnsureGateway ensures a Gateway API Gateway exists in the NetEye namespace.
// Existing Gateways are adopted when they have no conflicting controller owner.
func EnsureGateway(ctx context.Context, client client.Client, log *logr.Logger, namespace string, name string, gatewayClassName string, annotations map[string]string, tlsSecretName string, owner metav1.OwnerReference) error {
	desiredSpec := map[string]interface{}{
		"gatewayClassName": gatewayClassName,
		"listeners": []interface{}{
			map[string]interface{}{
				"name":     gatewayHTTPListenerName,
				"protocol": "HTTP",
				"port":     int64(80),
				"allowedRoutes": map[string]interface{}{
					"namespaces": map[string]interface{}{
						"from": "Same",
					},
					"kinds": routeKinds(),
				},
			},
			map[string]interface{}{
				"name":     gatewayHTTPSListenerName,
				"protocol": "HTTPS",
				"port":     int64(443),
				"allowedRoutes": map[string]interface{}{
					"namespaces": map[string]interface{}{
						"from": "Same",
					},
					"kinds": routeKinds(),
				},
				"tls": map[string]interface{}{
					"mode": "Terminate",
					"certificateRefs": []interface{}{
						map[string]interface{}{
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
		desiredSpec["infrastructure"] = map[string]interface{}{
			"annotations": stringMapToInterfaces(annotations),
		}
	}

	gateway := &unstructured.Unstructured{}
	gateway.SetGroupVersionKind(gatewayGVK())
	key := types.NamespacedName{Name: name, Namespace: namespace}
	if err := client.Get(ctx, key, gateway); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(gateway.Object, "spec")
		ownerChanged, err := SetOwnerReference(gateway, owner)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(currentSpec, desiredSpec) && !ownerChanged {
			log.V(1).Info("Gateway had no drift", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "tlsSecret", tlsSecretName)
			return nil
		}
		if !reflect.DeepEqual(currentSpec, desiredSpec) {
			if err := unstructured.SetNestedMap(gateway.Object, desiredSpec, "spec"); err != nil {
				return err
			}
		}
		if err := client.Update(ctx, gateway); err != nil {
			return err
		}
		log.V(1).Info("Gateway reconciled", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "tlsSecret", tlsSecretName)
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	gateway = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if _, err := SetOwnerReference(gateway, owner); err != nil {
		return err
	}
	if err := client.Create(ctx, gateway); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	log.Info("Gateway created", "namespace", namespace, "gateway", name, "gatewayClassName", gatewayClassName, "tlsSecret", tlsSecretName)
	return nil
}

// EnsureHTTPToHTTPSRedirectRoute ensures the default Gateway HTTP listener
// redirects cleartext traffic to HTTPS.
func EnsureHTTPToHTTPSRedirectRoute(ctx context.Context, client client.Client, log *logr.Logger, namespace string, gatewayName string, owner metav1.OwnerReference) error {
	desiredSpec := map[string]interface{}{
		"parentRefs": []interface{}{
			map[string]interface{}{
				"name":        gatewayName,
				"sectionName": gatewayHTTPListenerName,
			},
		},
		"rules": []interface{}{
			map[string]interface{}{
				"filters": []interface{}{
					map[string]interface{}{
						"type": "RequestRedirect",
						"requestRedirect": map[string]interface{}{
							"scheme":     "https",
							"statusCode": int64(301),
						},
					},
				},
			},
		},
	}

	return ensureHTTPRouteSpec(ctx, client, log, namespace, HTTPToHTTPSRedirectRouteName, desiredSpec, owner)
}

func gatewayGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "Gateway",
	}
}

func routeKinds() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"kind": "HTTPRoute",
		},
		map[string]interface{}{
			"kind": "GRPCRoute",
		},
	}
}

func stringMapToInterfaces(values map[string]string) map[string]interface{} {
	items := make(map[string]interface{}, len(values))
	for key, value := range values {
		items[key] = value
	}
	return items
}
