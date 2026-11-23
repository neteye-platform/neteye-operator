// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package tenantgateway reconciles the Gateway of a NetEye tenant, in the
// tenant namespace, and the CA that signs the certificates of its listeners.
//
// The services of a tenant have the hostname
// <service>.tenant-<tenant>.neteyelocal, where <tenant> is the name of the
// tenant namespace without the prefix "neteye-tenant-". The Gateway has the
// listener "tenant" on the domain of the tenant, tenant-<tenant>.neteyelocal,
// which it always has, and one listener for each service that the tenant
// enables, on the hostname of the service. Each listener accepts only the
// routes of the tenant namespace.
//
// The CA of the tenant is an intermediate CA that the internal Issuer of the
// NetEye resource signs, in the shared namespace. A critical name constraint
// limits it to the domain of the tenant, so it cannot sign a certificate that
// the clients accept for the hostname of another tenant. The operator copies
// its Secret into the tenant namespace, where an Issuer signs the certificates
// of the listeners with it; the tenant namespace never holds the key of the
// root CA.
package tenantgateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	// GatewayName is the Gateway of the tenant, in the tenant namespace.
	GatewayName = "neteye"
	// TenantListenerName is the listener on the domain of the tenant, which
	// the Gateway always has.
	TenantListenerName = "tenant"
	// HostnameDomain follows the domain of the tenant in its hostnames.
	HostnameDomain = "neteyelocal"
	// CAName is the Issuer of the tenant namespace and the Secret with the copy
	// of the CA of the tenant that it signs with.
	CAName = "neteye-tenant-ca"
	// SharedCANamePrefix and the id of the tenant name the Certificate and the
	// Secret of the CA of the tenant in the shared namespace.
	SharedCANamePrefix = "neteye-tenant-ca-"
	// listenerCertificatePrefix and the name of a listener name the Certificate
	// of the listener; the Secret has the suffix "-tls".
	listenerCertificatePrefix = "neteye-gateway-"
)

// Phase is the state of the Gateway of a tenant.
type Phase string

const (
	PhaseReady       Phase = "Ready"
	PhaseProgressing Phase = "Progressing"
	PhaseDegraded    Phase = "Degraded"
)

const (
	ReasonAvailable             = "Available"
	ReasonInvalidConfiguration  = "InvalidConfiguration"
	ReasonCertificateNotReady   = "CertificateNotReady"
	ReasonSharedGatewayNotReady = "SharedGatewayNotReady"
	ReasonListenerNotProgrammed = "ListenerNotProgrammed"
	ReasonReconcileFailed       = "ReconcileFailed"
)

// Outcome is the result of one reconciliation of the Gateway of a tenant.
type Outcome struct {
	Phase   Phase
	Reason  string
	Message string
	Err     error
}

func readyOutcome(message string) Outcome {
	return Outcome{Phase: PhaseReady, Reason: ReasonAvailable, Message: message}
}

func progressingOutcome(reason, message string) *Outcome {
	return &Outcome{Phase: PhaseProgressing, Reason: reason, Message: message}
}

func degradedOutcome(reason, message string, err error) *Outcome {
	if err == nil {
		err = errors.New(message)
	}
	if message == "" {
		message = err.Error()
	}
	return &Outcome{Phase: PhaseDegraded, Reason: reason, Message: message, Err: err}
}

// Inputs is the desired Gateway of one tenant.
type Inputs struct {
	// Namespace is the tenant namespace, which has the name of the tenant.
	Namespace string
	// ClassName is the GatewayClass of the Gateway.
	ClassName string
	// IssuerName is the Issuer of the shared namespace that signs the CA of
	// the tenant.
	IssuerName string
	// Shared is the shared Gateway of the NetEye resource. It can ask the load
	// balancer pool for a fixed address, so the Gateway of the tenant is created
	// only after the shared Gateway has its address: otherwise the pool could
	// give that address to the Gateway of the tenant.
	Shared resources.GatewayRef
	// Services tells, for each service that can have a listener, whether the
	// tenant enables it. A service that the tenant does not enable has no
	// listener, and its certificate is removed.
	Services map[string]bool
}

// TenantID returns the id of the tenant of a tenant namespace: its name
// without the prefix "neteye-tenant-".
func TenantID(namespace string) string {
	return strings.TrimPrefix(namespace, neteye.TenantNamePrefix)
}

// Domain returns the domain of a tenant namespace: tenant-<tenant>.neteyelocal.
func Domain(namespace string) string {
	return "tenant-" + TenantID(namespace) + "." + HostnameDomain
}

// ServiceHostname returns the hostname of a service of a tenant namespace:
// <service>.tenant-<tenant>.neteyelocal. It is also the hostname of the
// listener of the service.
func ServiceHostname(namespace, service string) string {
	return service + "." + Domain(namespace)
}

// SharedCAName returns the name of the Certificate and of the Secret of the CA
// of a tenant namespace, in the shared namespace.
func SharedCAName(namespace string) string {
	return SharedCANamePrefix + TenantID(namespace)
}

// ListenerCertificateName and ListenerSecretName return the Certificate of a
// listener of the Gateway, in the tenant namespace, and its Secret.
func ListenerCertificateName(listener string) string {
	return listenerCertificatePrefix + listener
}

func ListenerSecretName(listener string) string {
	return ListenerCertificateName(listener) + "-tls"
}

// Component reconciles the Gateway of a tenant.
type Component struct{ client client.Client }

func NewComponent(c client.Client) *Component {
	return &Component{client: c}
}

type listener struct {
	name     string
	hostname string
}

// listeners returns the listeners of the Gateway: the listener of the tenant,
// then those of the enabled services, in the order of their names.
func (in Inputs) listeners() []listener {
	result := []listener{{name: TenantListenerName, hostname: Domain(in.Namespace)}}
	for _, service := range sortedServices(in.Services) {
		if in.Services[service] {
			result = append(result, listener{name: service, hostname: ServiceHostname(in.Namespace, service)})
		}
	}
	return result
}

// Ensure reconciles the Gateway of a tenant and reports its readiness.
func (c *Component) Ensure(ctx context.Context, in Inputs, owner metav1.OwnerReference) Outcome {
	if in.ClassName == "" || in.IssuerName == "" || in.Shared.Name == "" {
		return *degradedOutcome(ReasonInvalidConfiguration, "the Gateway, its GatewayClass and the internal certificate Issuer of the NetEye resource are required for the Gateway of the tenant", nil)
	}
	for service := range in.Services {
		if service == "" || service == TenantListenerName {
			return *degradedOutcome(ReasonInvalidConfiguration, fmt.Sprintf("invalid service %q for a listener of the Gateway of the tenant", service), nil)
		}
	}
	if outcome := c.ensureCA(ctx, in.Namespace, in.IssuerName, owner); outcome != nil {
		return *outcome
	}
	if err := resources.EnsureCAIssuer(ctx, c.client, in.Namespace, CAName, CAName, owner); err != nil {
		return *degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Issuer %s: %w", CAName, err))
	}
	listeners := in.listeners()
	for _, l := range listeners {
		if err := resources.EnsureCertificate(ctx, c.client, in.Namespace, ListenerCertificateName(l.name), ListenerSecretName(l.name), l.hostname, []string{l.hostname}, resources.CertificateIssuerRef{Name: CAName}, &owner); err != nil {
			return *degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Certificate %s: %w", ListenerCertificateName(l.name), err))
		}
	}
	// The certificate of a service that the tenant no longer enables, and its
	// private key, go with its listener.
	for _, service := range sortedServices(in.Services) {
		if !in.Services[service] {
			if err := resources.DeleteManagedCertificate(ctx, c.client, in.Namespace, ListenerCertificateName(service), ListenerSecretName(service)); err != nil {
				return *degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("delete Certificate %s: %w", ListenerCertificateName(service), err))
			}
		}
	}

	if address, err := resources.GatewayAddress(ctx, c.client, in.Shared); err != nil {
		return *degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("read the address of the Gateway %s/%s: %w", in.Shared.Namespace, in.Shared.Name, err))
	} else if address == "" {
		return *progressingOutcome(ReasonSharedGatewayNotReady, fmt.Sprintf("waiting for the Gateway %s/%s to get its address before the Gateway of the tenant asks the load balancer pool for one", in.Shared.Namespace, in.Shared.Name))
	}
	// The Gateway gets its address from the load balancer pool, so it does not
	// take the annotations of the shared Gateway, which can ask for an address.
	// It has only HTTPS listeners: nothing redirects HTTP.
	gatewayListeners := make([]resources.GatewayListener, 0, len(listeners))
	for _, l := range listeners {
		gatewayListeners = append(gatewayListeners, resources.GatewayListener{Name: l.name, Hostname: l.hostname, TLSSecretName: ListenerSecretName(l.name), RouteKind: resources.RouteKindHTTP})
	}
	if err := resources.EnsureHTTPSGateway(ctx, c.client, in.Namespace, GatewayName, in.ClassName, gatewayListeners, owner); err != nil {
		return *degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Gateway %s: %w", GatewayName, err))
	}

	gateway := resources.GatewayRef{Namespace: in.Namespace, Name: GatewayName}
	for _, l := range listeners {
		if outcome := c.listenerOutcome(ctx, gateway, l.name); outcome != nil {
			return *outcome
		}
	}
	return readyOutcome(fmt.Sprintf("Gateway %s/%s is ready for %s", in.Namespace, GatewayName, Domain(in.Namespace)))
}

// ensureCA keeps the CA of the tenant: a Certificate of the shared namespace,
// which the internal Issuer signs for the domain of the tenant only, and the
// copy of its Secret in the tenant namespace. It returns nil when the copy is
// current.
func (c *Component) ensureCA(ctx context.Context, namespace, issuerName string, owner metav1.OwnerReference) *Outcome {
	name := SharedCAName(namespace)
	if err := resources.EnsureCACertificate(ctx, c.client, neteye.NetEyeNamespace, name, name, name, []string{Domain(namespace)}, resources.CertificateIssuerRef{Name: issuerName}); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply the CA Certificate %s/%s: %w", neteye.NetEyeNamespace, name, err))
	}
	ready, message, err := resources.IsCertificateReady(ctx, c.client, neteye.NetEyeNamespace, name)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !ready {
		return progressingOutcome(ReasonCertificateNotReady, message)
	}
	copied, message, err := resources.CopyTLSSecret(ctx, c.client, client.ObjectKey{Namespace: neteye.NetEyeNamespace, Name: name}, client.ObjectKey{Namespace: namespace, Name: CAName}, owner)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("copy the CA Secret into %s: %w", namespace, err))
	}
	if !copied {
		return progressingOutcome(ReasonCertificateNotReady, message)
	}
	return nil
}

// listenerOutcome waits for the certificate of a listener and for the Gateway
// to program the listener.
func (c *Component) listenerOutcome(ctx context.Context, gateway resources.GatewayRef, name string) *Outcome {
	ready, message, err := resources.IsCertificateReady(ctx, c.client, gateway.Namespace, ListenerCertificateName(name))
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !ready {
		return progressingOutcome(ReasonCertificateNotReady, message)
	}
	ready, message, err = resources.GatewayListenerProgrammed(ctx, c.client, gateway, name)
	if err != nil {
		return degradedOutcome(ReasonReconcileFailed, message, err)
	}
	if !ready {
		return progressingOutcome(ReasonListenerNotProgrammed, message)
	}
	return nil
}

// Delete removes the Gateway of a tenant: the Gateway, the certificates of
// the listener of the tenant and of the services in services, the Issuer, and
// the CA of the tenant in both namespaces. cert-manager gives the Secrets of
// the certificates no owner, so the garbage collector would leave them, with
// their private keys, in the namespace, which stays with the tenant. Every
// object is tried even if another one fails.
func (c *Component) Delete(ctx context.Context, namespace string, services map[string]bool, owner metav1.OwnerReference) error {
	var errs []error
	if err := resources.DeleteOwnedGateways(ctx, c.client, namespace, owner); err != nil {
		errs = append(errs, fmt.Errorf("delete Gateway %s: %w", GatewayName, err))
	}
	for _, name := range append([]string{TenantListenerName}, sortedServices(services)...) {
		if err := resources.DeleteManagedCertificate(ctx, c.client, namespace, ListenerCertificateName(name), ListenerSecretName(name)); err != nil {
			errs = append(errs, fmt.Errorf("delete Certificate %s: %w", ListenerCertificateName(name), err))
		}
	}
	if err := resources.DeleteIssuer(ctx, c.client, namespace, CAName, owner); err != nil {
		errs = append(errs, fmt.Errorf("delete Issuer %s: %w", CAName, err))
	}
	if err := resources.DeleteOwnedSecret(ctx, c.client, namespace, CAName, owner); err != nil {
		errs = append(errs, fmt.Errorf("delete Secret %s: %w", CAName, err))
	}
	name := SharedCAName(namespace)
	if err := resources.DeleteManagedCertificate(ctx, c.client, neteye.NetEyeNamespace, name, name); err != nil {
		errs = append(errs, fmt.Errorf("delete the CA Certificate %s/%s: %w", neteye.NetEyeNamespace, name, err))
	}
	return errors.Join(errs...)
}

func sortedServices(services map[string]bool) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	// A stable order keeps the listeners of the Gateway in place.
	sort.Strings(names)
	return names
}
