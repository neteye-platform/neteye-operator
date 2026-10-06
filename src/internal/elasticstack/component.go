// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package elasticstack owns the Elastic Stack telemetry lifecycle boundary.
package elasticstack

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	ConfigMapName              = "otel-collector-config"
	VariablesConfigMapName     = "otel-collector-variables"
	DeploymentName             = "otel-collector"
	ServiceName                = "otel-collector-service"
	GRPCRouteName              = "otel-collector-route"
	HTTPRouteName              = "otel-collector-crosstenant-route"
	GRPCRouteHostname          = "otel-collector.neteyelocal"
	CrossTenantRouteHostname   = "otel-collector-crosstenant.neteyelocal"
	GRPCListenerName           = "otel-collector"
	CrossTenantListenerName    = "otel-collector-crosstenant"
	IngressPolicyName          = "neteye-otel-collector-ingress"
	EgressPolicyName           = "neteye-otel-collector-egress"
	GRPCTLSCertName            = "otel-collector-tls"
	GRPCTLSSecretName          = "otel-collector-tls-secret"
	CrossTenantTLSCertName     = "otel-collector-crosstenant-tls"
	CrossTenantTLSSecretName   = "otel-collector-crosstenant-tls-secret"
	DefaultBasicAuthSecretName = "otel-collector-basicauth"
	DefaultRootCASecretName    = "neteye-root-ca"
)

type managedResource struct {
	gvk  schema.GroupVersionKind
	name string
}

func collectorResourceInventory() []managedResource {
	return []managedResource{
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, ConfigMapName},
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, DeploymentName},
		{schema.GroupVersionKind{Version: "v1", Kind: "Service"}, ServiceName},
		{schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"}, GRPCRouteName},
		{schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}, HTTPRouteName},
		{schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}, GRPCTLSCertName},
		{schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}, CrossTenantTLSCertName},
		{schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, IngressPolicyName},
		{schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, EgressPolicyName},
	}
}

func edotGatewayResourceInventory() []managedResource {
	return []managedResource{
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, EDOTGatewayConfigMapName},
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, EDOTGatewayDeploymentName},
		{schema.GroupVersionKind{Version: "v1", Kind: "Service"}, EDOTGatewayServiceName},
		{schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, EDOTGatewayIngressPolicyName},
		{schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}, EDOTGatewayEgressPolicyName},
	}
}

// collectorLegacyInventory lists owned collector resources that must be pruned
// from existing installations but are intentionally excluded from the active
// desired set.
func collectorLegacyInventory() []managedResource {
	return []managedResource{
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, VariablesConfigMapName},
	}
}

// edotGatewayLegacyInventory mirrors collectorLegacyInventory for the EDOT Gateway.
func edotGatewayLegacyInventory() []managedResource {
	return []managedResource{
		{schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, EDOTGatewayVariablesConfigMapName},
	}
}

func deleteOwnedResources(ctx context.Context, c client.Client, namespace string, owner metav1.OwnerReference, inventory []managedResource) error {
	for _, resource := range inventory {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(resource.gvk)
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resource.name}, object); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if !controlledBy(object, owner) {
			continue
		}
		if err := c.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func controlledBy(object client.Object, owner metav1.OwnerReference) bool {
	return resources.ControlledBy(object, owner)
}

// ownedResources converts a managed-resource inventory into the shared deletion
// inventory. Telemetry resources hold no installation data and are rebuilt from
// the NetEye resource, so both deletion policies remove them.
func ownedResources(namespace string, inventories ...[]managedResource) []resources.OwnedResource {
	var owned []resources.OwnedResource
	for _, inventory := range inventories {
		for i := len(inventory) - 1; i >= 0; i-- {
			owned = append(owned, resources.OwnedResource{
				GVK:       inventory[i].gvk,
				Namespace: namespace,
				Name:      inventory[i].name,
				Retention: resources.RecreatableOnDelete,
			})
		}
	}
	return owned
}

// OwnedResources is the OTel Collector's deletion inventory, in reverse
// dependency order. Its basic-auth, API-key, and CA Secrets are referenced
// rather than owned and are never deleted by NetEye.
func (c *OTelCollectorComponent) OwnedResources(namespace string) []resources.OwnedResource {
	return ownedResources(namespace, collectorResourceInventory(), collectorLegacyInventory())
}

// OwnedResources is the EDOT Gateway's deletion inventory, in reverse
// dependency order. Its Elasticsearch API-key and CA Secrets are referenced
// rather than owned and are never deleted by NetEye.
func (c *EDOTGatewayComponent) OwnedResources(namespace string) []resources.OwnedResource {
	return ownedResources(namespace, edotGatewayResourceInventory(), edotGatewayLegacyInventory())
}
