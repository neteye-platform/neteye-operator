// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package alyvix owns the services of the Alyvix feature module that run in
// the cluster.
//
// The RDP access lets an administrator of an Alyvix node open an RDP session
// to the node in the browser. Every tenant runs its own Apache Guacamole web
// application and proxy daemon (guacd) in the tenant namespace:
//
//   - The Alyvix module on the NetEye host sends the connection data, signed
//     and encrypted with the Guacamole JSON authentication key of the tenant,
//     to the web application, and receives a Guacamole authToken.
//   - The browser opens the WebSocket tunnel to the web application through
//     the web server of the NetEye host.
//   - guacd opens the RDP connection to the Alyvix node.
//
// The route of the RDP access exposes, on the listener "guacamole" of the
// Gateway of the tenant (see the package tenantgateway), only the few paths of
// the web application that the module and the browser use.
package alyvix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neteye-platform/neteye-operator/internal/resources"
	"github.com/neteye-platform/neteye-operator/internal/tenantgateway"
)

const (
	// ServiceName is the service of the RDP access on the Gateway of the
	// tenant: its listener and its hostname, guacamole.tenant-<tenant>.neteyelocal.
	// The Gateway of the tenant has the listener while the tenant enables
	// Alyvix; the route of the RDP access attaches to it.
	ServiceName = "guacamole"

	// JSONAuthSecretName holds the Guacamole JSON authentication key of a
	// tenant, under JSONAuthSecretKey: 32 hexadecimal characters (128 bits).
	// The Alyvix module signs and encrypts the connection data with the same
	// key. The operator generates the key when the Secret does not exist and
	// never changes it afterwards.
	JSONAuthSecretName = "alyvix-rdp-json-auth"
	JSONAuthSecretKey  = "json-secret-key"

	GuacamoleName = "alyvix-guacamole"
	GuacdName     = "alyvix-guacd"
	HTTPRouteName = "alyvix-guacamole"

	GuacamoleIngressPolicyName = "neteye-alyvix-guacamole-ingress"
	GuacamoleEgressPolicyName  = "neteye-alyvix-guacamole-egress"
	GuacdIngressPolicyName     = "neteye-alyvix-guacd-ingress"
	GuacdEgressPolicyName      = "neteye-alyvix-guacd-egress"

	// GuacamolePort is the HTTP port of the web application, and ContextPath
	// its path.
	GuacamolePort = 8080
	ContextPath   = "/guacamole"
	// GuacdPort is the port of the Guacamole protocol of guacd.
	GuacdPort = 4822

	// The users of the images: guacamole/guacamole runs as guacamole (1001),
	// guacamole/guacd as guacd (1000). The image users are names, so
	// runAsNonRoot needs the numeric ids.
	guacamoleUserID int64 = 1001
	guacdUserID     int64 = 1000

	// userCAMountPath holds the CA bundle of the tenant in the guacd pod.
	userCAMountPath = "/etc/neteye/user-ca"

	jsonAuthKeyVersionAnnotation = "neteye.cloud/json-auth-resource-version"
	partOfLabel                  = "app.kubernetes.io/part-of"
	partOfValue                  = "alyvix-rdp"
)

// Phase is the state of the RDP access of a tenant.
type Phase string

const (
	PhaseReady       Phase = "Ready"
	PhaseProgressing Phase = "Progressing"
	PhaseDegraded    Phase = "Degraded"
)

const (
	ReasonAvailable              = "Available"
	ReasonInvalidConfiguration   = "InvalidConfiguration"
	ReasonInvalidSecret          = "InvalidSecret"
	ReasonSecretNotCached        = "SecretNotCached"
	ReasonDeploymentNotAvailable = "DeploymentNotAvailable"
	ReasonDeploymentStalled      = "DeploymentStalled"
	ReasonListenerNotProgrammed  = "ListenerNotProgrammed"
	ReasonRouteNotReady          = "RouteNotReady"
	ReasonReconcileFailed        = "ReconcileFailed"
)

// Outcome is the result of one reconciliation of the RDP access of a tenant.
type Outcome struct {
	Phase   Phase
	Reason  string
	Message string
	Err     error
}

func readyOutcome(message string) Outcome {
	return Outcome{Phase: PhaseReady, Reason: ReasonAvailable, Message: message}
}

func progressingOutcome(reason, message string) Outcome {
	return Outcome{Phase: PhaseProgressing, Reason: reason, Message: message}
}

func degradedOutcome(reason, message string, err error) Outcome {
	if err == nil {
		err = errors.New(message)
	}
	if message == "" {
		message = err.Error()
	}
	return Outcome{Phase: PhaseDegraded, Reason: reason, Message: message, Err: err}
}

// RDPInputs is the desired RDP access of one tenant.
type RDPInputs struct {
	// Namespace is the tenant namespace, which has the name of the tenant.
	Namespace      string
	GuacamoleImage string
	GuacdImage     string
	// Ports are the TCP ports of RDP on the Alyvix nodes.
	Ports []int32
	// CABundle is the optional ConfigMap key of the tenant namespace with the
	// extra PEM CA certificates that guacd trusts for the RDP certificates
	// of the Alyvix nodes.
	CABundle resources.CABundleRef
	// ManagedGateway tells that the operator manages the Gateway of the
	// tenant, which has the listener of the RDP access: the route attaches to
	// it. Otherwise the operator creates no route.
	ManagedGateway bool
}

// Hostname returns the hostname of the RDP access of a tenant namespace:
// guacamole.tenant-<tenant>.neteyelocal.
func Hostname(namespace string) string {
	return tenantgateway.ServiceHostname(namespace, ServiceName)
}

// RDPComponent reconciles the RDP access of a tenant.
type RDPComponent struct{ client client.Client }

func NewRDPComponent(c client.Client) *RDPComponent {
	return &RDPComponent{client: c}
}

// Ensure reconciles the RDP access of a tenant and reports its readiness.
func (c *RDPComponent) Ensure(ctx context.Context, in RDPInputs, owner metav1.OwnerReference) Outcome {
	if in.GuacamoleImage == "" || in.GuacdImage == "" {
		return degradedOutcome(ReasonInvalidConfiguration, "the resolved Guacamole and guacd images are required", nil)
	}
	if in.CABundle.Name == "" || in.CABundle.Key == "" {
		return degradedOutcome(ReasonInvalidConfiguration, "the CA bundle ConfigMap name and key are required", nil)
	}
	ports, err := validatedPorts(in.Ports)
	if err != nil {
		return degradedOutcome(ReasonInvalidConfiguration, err.Error(), nil)
	}
	// The tenant namespace denies all traffic, so the policies come first.
	for _, policy := range []struct {
		name string
		spec map[string]any
	}{
		{GuacamoleIngressPolicyName, guacamoleIngressPolicy()},
		{GuacamoleEgressPolicyName, guacamoleEgressPolicy()},
		{GuacdIngressPolicyName, guacdIngressPolicy()},
		{GuacdEgressPolicyName, guacdEgressPolicy(ports)},
	} {
		if err := resources.EnsureNetworkPolicy(ctx, c.client, in.Namespace, policy.name, policy.spec, owner); err != nil {
			return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply network policy %s: %w", policy.name, err))
		}
	}
	keyVersion, outcome := c.ensureJSONAuthSecret(ctx, in.Namespace, owner)
	if outcome != nil {
		return *outcome
	}
	if err := resources.EnsureDeployment(ctx, c.client, guacdDeployment(in.Namespace, in.GuacdImage, in.CABundle), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Deployment %s: %w", GuacdName, err))
	}
	if err := resources.EnsureService(ctx, c.client, service(in.Namespace, GuacdName, "guacd", GuacdPort), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Service %s: %w", GuacdName, err))
	}
	if err := resources.EnsureDeployment(ctx, c.client, guacamoleDeployment(in.Namespace, in.GuacamoleImage, keyVersion), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Deployment %s: %w", GuacamoleName, err))
	}
	if err := resources.EnsureService(ctx, c.client, service(in.Namespace, GuacamoleName, "http", GuacamolePort), owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply Service %s: %w", GuacamoleName, err))
	}
	if !in.ManagedGateway {
		if err := resources.DeleteHTTPRoute(ctx, c.client, in.Namespace, HTTPRouteName, owner); err != nil {
			return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("delete HTTPRoute %s: %w", HTTPRouteName, err))
		}
	} else if err := resources.EnsureHTTPRouteRules(ctx, c.client, in.Namespace, HTTPRouteName, gatewayRef(in.Namespace), ServiceName, []string{Hostname(in.Namespace)}, routeRules(), &owner); err != nil {
		return degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("apply HTTPRoute %s: %w", HTTPRouteName, err))
	}

	for _, name := range []string{GuacdName, GuacamoleName} {
		if outcome := deploymentOutcome(ctx, c.client, in.Namespace, name); outcome != nil {
			return *outcome
		}
	}
	if !in.ManagedGateway {
		return readyOutcome(fmt.Sprintf("Guacamole and guacd are ready; the Gateway is managed outside the operator, so route %s to the Service %s yourself", Hostname(in.Namespace), GuacamoleName))
	}
	if outcome := c.gatewayOutcome(ctx, in.Namespace); outcome != nil {
		return *outcome
	}
	return readyOutcome(fmt.Sprintf("RDP access is ready on %s", Hostname(in.Namespace)))
}

// Delete removes the RDP access of a tenant. The Secret with the JSON
// authentication key stays, so the key does not change when the access is
// enabled again; the garbage collector removes it with the tenant.
//
// Every object is tried even if another one fails, so a route that cannot be
// deleted does not keep the workloads that serve it.
func (c *RDPComponent) Delete(ctx context.Context, namespace string, owner metav1.OwnerReference) error {
	var errs []error
	for _, resource := range resourceInventory() {
		if err := c.deleteOwned(ctx, namespace, resource, owner); err != nil {
			errs = append(errs, fmt.Errorf("delete %s %s: %w", resource.gvk.Kind, resource.name, err))
		}
	}
	return errors.Join(errs...)
}

func (c *RDPComponent) deleteOwned(ctx context.Context, namespace string, resource managedResource, owner metav1.OwnerReference) error {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(resource.gvk)
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resource.name}, object); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}
	if !controlledBy(object, owner) {
		return nil
	}
	// Delete only the object that passed the ownership check: if it was
	// replaced in between, the precondition fails and the next pass checks the
	// new one.
	uid := object.GetUID()
	if err := c.client.Delete(ctx, object, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

type managedResource struct {
	gvk  schema.GroupVersionKind
	name string
}

func resourceInventory() []managedResource {
	deployment := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	service := schema.GroupVersionKind{Version: "v1", Kind: "Service"}
	return []managedResource{
		{schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}, HTTPRouteName},
		{deployment, GuacamoleName},
		{service, GuacamoleName},
		{deployment, GuacdName},
		{service, GuacdName},
		{resources.NetworkPolicyGVK(), GuacamoleIngressPolicyName},
		{resources.NetworkPolicyGVK(), GuacamoleEgressPolicyName},
		{resources.NetworkPolicyGVK(), GuacdIngressPolicyName},
		{resources.NetworkPolicyGVK(), GuacdEgressPolicyName},
	}
}

func controlledBy(object client.Object, owner metav1.OwnerReference) bool {
	for _, reference := range object.GetOwnerReferences() {
		if reference.UID == owner.UID && reference.Controller != nil && *reference.Controller {
			return true
		}
	}
	return false
}

var jsonAuthKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// ensureJSONAuthSecret creates the Secret with a new random key when it does
// not exist, and otherwise only checks the key. It returns the resourceVersion
// of the Secret: the web application restarts when it changes, as the other
// workloads of the operator do for the Secrets they read. A change of the key
// always restarts it, also when an administrator edits the Secret in place.
func (c *RDPComponent) ensureJSONAuthSecret(ctx context.Context, namespace string, owner metav1.OwnerReference) (string, *Outcome) {
	secret := &corev1.Secret{}
	err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: JSONAuthSecretName}, secret)
	if apierrors.IsNotFound(err) {
		key, err := newJSONAuthKey()
		if err != nil {
			outcome := degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("generate the Guacamole JSON authentication key: %w", err))
			return "", &outcome
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       namespace,
				Name:            JSONAuthSecretName,
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "neteye-operator", partOfLabel: partOfValue},
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{JSONAuthSecretKey: []byte(key)},
		}
		if err := c.client.Create(ctx, secret); err != nil {
			// The cache has not seen a Secret that exists: its key stays, and the
			// next reconciliation reads it.
			if apierrors.IsAlreadyExists(err) {
				outcome := progressingOutcome(ReasonSecretNotCached, fmt.Sprintf("waiting to read Secret %s/%s", namespace, JSONAuthSecretName))
				return "", &outcome
			}
			outcome := degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("create Secret %s: %w", JSONAuthSecretName, err))
			return "", &outcome
		}
		return secret.ResourceVersion, nil
	}
	if err != nil {
		outcome := degradedOutcome(ReasonReconcileFailed, "", fmt.Errorf("get Secret %s: %w", JSONAuthSecretName, err))
		return "", &outcome
	}
	if !jsonAuthKeyPattern.Match(secret.Data[JSONAuthSecretKey]) {
		message := fmt.Sprintf("Secret %s/%s must hold 32 hexadecimal characters in key %q; delete the Secret to generate a new key", namespace, JSONAuthSecretName, JSONAuthSecretKey)
		outcome := degradedOutcome(ReasonInvalidSecret, message, nil)
		return "", &outcome
	}
	return secret.ResourceVersion, nil
}

// newJSONAuthKey returns 128 random bits as 32 hexadecimal characters, the
// key format of the Guacamole JSON authentication extension.
func newJSONAuthKey() (string, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func validatedPorts(ports []int32) ([]int64, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("at least one RDP port is required")
	}
	values := make([]int64, 0, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid RDP port %d", port)
		}
		values = append(values, int64(port))
	}
	slices.Sort(values)
	return slices.Compact(values), nil
}

func appSelector(app string) map[string]any {
	return map[string]any{"matchLabels": map[string]any{"app": app}}
}

// guacamoleIngressPolicy admits the Gateway to the web application. A
// NetworkPolicy can't select the Gateway's proxy, so the port is open to every
// source.
func guacamoleIngressPolicy() map[string]any {
	return map[string]any{
		"podSelector": appSelector(GuacamoleName),
		"policyTypes": []any{"Ingress"},
		"ingress":     []any{map[string]any{"ports": resources.NetworkPolicyPorts("TCP", GuacamolePort)}},
	}
}

// guacamoleEgressPolicy lets the web application reach only guacd.
func guacamoleEgressPolicy() map[string]any {
	return map[string]any{
		"podSelector": appSelector(GuacamoleName),
		"policyTypes": []any{"Egress"},
		"egress": []any{
			resources.DNSEgressRule(),
			map[string]any{"to": []any{map[string]any{"podSelector": appSelector(GuacdName)}}, "ports": resources.NetworkPolicyPorts("TCP", GuacdPort)},
		},
	}
}

// guacdIngressPolicy admits only the web application to guacd.
func guacdIngressPolicy() map[string]any {
	return map[string]any{
		"podSelector": appSelector(GuacdName),
		"policyTypes": []any{"Ingress"},
		"ingress":     []any{map[string]any{"from": []any{map[string]any{"podSelector": appSelector(GuacamoleName)}}, "ports": resources.NetworkPolicyPorts("TCP", GuacdPort)}},
	}
}

// guacdEgressPolicy lets guacd open RDP connections to the Alyvix nodes. The
// addresses of the nodes are not known in the cluster, so the RDP ports are
// open to any destination.
func guacdEgressPolicy(ports []int64) map[string]any {
	return map[string]any{
		"podSelector": appSelector(GuacdName),
		"policyTypes": []any{"Egress"},
		"egress":      []any{resources.DNSEgressRule(), map[string]any{"ports": resources.NetworkPolicyPorts("TCP", ports...)}},
	}
}

// routeRules expose only what the Alyvix module and the browser use: the
// module creates the authToken, the browser loads the client library, opens
// the WebSocket tunnel, and deletes its Guacamole session when the RDP page
// closes. The login page and the rest of the REST API stay unreachable.
func routeRules() []any {
	match := func(path, method string) map[string]any {
		return map[string]any{"path": map[string]any{"type": "Exact", "value": ContextPath + path}, "method": method}
	}
	return []any{map[string]any{
		"matches": []any{
			match("/api/tokens", "POST"),
			match("/api/session", "DELETE"),
			match("/websocket-tunnel", "GET"),
			match("/guacamole-common-js/all.min.js", "GET"),
		},
		"backendRefs": []any{resources.HTTPRouteServiceBackend(GuacamoleName, GuacamolePort)},
	}}
}

func labels(app string) map[string]string {
	return map[string]string{"app": app, partOfLabel: partOfValue}
}

func restrictedSecurityContext(userID int64) *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To(userID),
		RunAsGroup:               ptr.To(userID),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// podSpec has no service account token, and no service environment variables:
// the image reads GUACD_PORT, which the Service links of a Service named guacd
// would overwrite.
func podSpec(container corev1.Container, volumes []corev1.Volume) corev1.PodSpec {
	return corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		Containers:                   []corev1.Container{container},
		Volumes:                      volumes,
	}
}

func deployment(namespace, name string, annotations map[string]string, container corev1.Container, volumes ...corev1.Volume) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels(name)},
		Spec: appsv1.DeploymentSpec{
			// The web application keeps the Guacamole sessions in memory, so it
			// runs once.
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(name), Annotations: annotations},
				Spec:       podSpec(container, volumes),
			},
		},
	}
}

// guacdDeployment mounts the optional CA bundle of the tenant and points
// SSL_CERT_FILE to it. FreeRDP verifies the RDP certificate of a node against
// the default OpenSSL paths: SSL_CERT_FILE replaces only the single-file
// bundle, the hashed system CA folder stays. A missing ConfigMap leaves the
// file absent, which OpenSSL ignores.
//
// FreeRDP loads the CAs again for every connection, so a changed bundle
// applies to the next RDP sessions once the kubelet refreshes the volume. The
// pod is not rolled, which would close the open sessions.
func guacdDeployment(namespace, image string, caBundle resources.CABundleRef) *appsv1.Deployment {
	probe := func(period, failures int32) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("guacd")}}, PeriodSeconds: period, FailureThreshold: failures, TimeoutSeconds: 2}
	}
	return deployment(namespace, GuacdName, nil, corev1.Container{
		Name:            "guacd",
		Image:           image,
		Ports:           []corev1.ContainerPort{{Name: "guacd", ContainerPort: GuacdPort, Protocol: corev1.ProtocolTCP}},
		Env:             []corev1.EnvVar{{Name: "SSL_CERT_FILE", Value: userCAMountPath + "/" + resources.UserCABundleFile}},
		VolumeMounts:    []corev1.VolumeMount{{Name: resources.UserCABundleVolumeName, MountPath: userCAMountPath, ReadOnly: true}},
		StartupProbe:    probe(2, 30),
		ReadinessProbe:  probe(10, 3),
		LivenessProbe:   probe(20, 3),
		SecurityContext: restrictedSecurityContext(guacdUserID),
	}, corev1.Volume{Name: resources.UserCABundleVolumeName, VolumeSource: resources.UserCABundleVolumeSource(caBundle)})
}

func guacamoleDeployment(namespace, image, keyVersion string) *appsv1.Deployment {
	probe := func(period, failures int32) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: ContextPath + "/", Port: intstr.FromString("http")}}, PeriodSeconds: period, FailureThreshold: failures, TimeoutSeconds: 2}
	}
	return deployment(namespace, GuacamoleName, map[string]string{jsonAuthKeyVersionAnnotation: keyVersion}, corev1.Container{
		Name:  "guacamole",
		Image: image,
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: GuacamolePort, Protocol: corev1.ProtocolTCP}},
		Env: []corev1.EnvVar{
			{Name: "GUACD_HOSTNAME", Value: GuacdName},
			{Name: "GUACD_PORT", Value: fmt.Sprint(GuacdPort)},
			{Name: "JSON_SECRET_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: JSONAuthSecretName}, Key: JSONAuthSecretKey}}},
			// Every authToken request comes through the web server of the
			// NetEye host, so a ban after failed requests would block the RDP
			// access of everyone. The JSON authentication has no password to
			// guess.
			{Name: "BAN_ENABLED", Value: "false"},
		},
		// Tomcat needs some time to deploy the web application.
		StartupProbe:    probe(5, 36),
		ReadinessProbe:  probe(10, 3),
		LivenessProbe:   probe(20, 3),
		SecurityContext: restrictedSecurityContext(guacamoleUserID),
	})
}

func service(namespace, name, portName string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels(name)},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Name: portName, Protocol: corev1.ProtocolTCP, Port: port, TargetPort: intstr.FromString(portName)}},
		},
	}
}

func deploymentOutcome(ctx context.Context, c client.Client, namespace, name string) *Outcome {
	ready, message, err := resources.IsDeploymentReady(ctx, c, namespace, name)
	var outcome Outcome
	switch {
	case errors.Is(err, resources.ErrDeploymentStalled):
		outcome = degradedOutcome(ReasonDeploymentStalled, message, err)
	case err != nil:
		outcome = degradedOutcome(ReasonReconcileFailed, message, err)
	case !ready:
		outcome = progressingOutcome(ReasonDeploymentNotAvailable, message)
	default:
		return nil
	}
	return &outcome
}

// gatewayRef is the Gateway of the tenant, which has the listener of the RDP
// access.
func gatewayRef(namespace string) resources.GatewayRef {
	return resources.GatewayRef{Namespace: namespace, Name: tenantgateway.GatewayName}
}

// gatewayOutcome waits for the Gateway of the tenant to program the listener
// of the RDP access, and to accept the route. The Gateway can accept the
// route before the listener is programmed.
func (c *RDPComponent) gatewayOutcome(ctx context.Context, namespace string) *Outcome {
	gateway := gatewayRef(namespace)
	ready, message, err := resources.GatewayListenerProgrammed(ctx, c.client, gateway, ServiceName)
	if err != nil {
		outcome := degradedOutcome(ReasonReconcileFailed, message, err)
		return &outcome
	}
	if !ready {
		outcome := progressingOutcome(ReasonListenerNotProgrammed, message)
		return &outcome
	}
	ready, message, err = resources.RouteReady(ctx, c.client, namespace, HTTPRouteName, resources.RouteKindHTTP, resources.RouteParent{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: gateway.Namespace, Name: gateway.Name, Section: ServiceName})
	if err != nil {
		outcome := degradedOutcome(ReasonReconcileFailed, message, err)
		return &outcome
	}
	if !ready {
		outcome := progressingOutcome(ReasonRouteNotReady, message)
		return &outcome
	}
	return nil
}
