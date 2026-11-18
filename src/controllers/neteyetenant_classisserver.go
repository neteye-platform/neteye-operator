// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

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

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/cnpg"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	classisServerDatabaseComponentID componentID = "classis-server-database"
	classisServerComponentID         componentID = "classis-server"

	// classisServerName names the Deployment, the Service, the ConfigMap and the
	// policies of the server, and is the host label of its DNS name.
	classisServerName = "classis-server"
	// classisServerDatabaseName names the CloudNativePG cluster of the tenant.
	// CloudNativePG derives the Secrets and the Services from it.
	classisServerDatabaseName = "classis-server-db"

	classisServerAgentPort = 4320
	classisServerAdminPort = 4321
	// classisServerUID is the UID of the non-root user of the distroless image.
	classisServerUID int64 = 65532

	// classisServerEnvPrefix prefixes the environment variables that override the
	// configuration file.
	classisServerEnvPrefix = "CLASSIS_SERVER__"

	// cnpgSystemNamespace is where the CloudNativePG operator runs. Its pods
	// reach the status port of every instance.
	cnpgSystemNamespace = "cnpg-system"

	// classisServerSecretMode makes mounted Secrets readable by the group of the
	// pod only (fsGroup): the server refuses a secret file readable by others.
	classisServerSecretMode int32 = 0o440

	classisServerConfigVersionAnnotation = "neteye.cloud/classis-server-config-resource-version"
	classisServerConfigFile              = "server.toml"
)

var cnpgClusterGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}

// classisServerSelector selects the pods of the server.
func classisServerSelector() map[string]string {
	return map[string]string{"app.kubernetes.io/name": classisServerName}
}

// classisServerDatabaseConnection is how the server reaches PostgreSQL.
type classisServerDatabaseConnection struct {
	Host, DBName, User string
	Port               int32
	// PasswordSecret and CASecret name the Secrets that hold the password key and
	// the ca.crt key.
	PasswordSecret, CASecret string
}

func classisServerDatabase(tenant *neteye.NetEyeTenant) classisServerDatabaseConnection {
	if tenant.Spec.Database != nil && tenant.Spec.Database.Type == neteye.TenantDatabaseExternal {
		port := tenant.Spec.Database.Port
		if port == 0 {
			port = 5432
		}
		connection := classisServerDatabaseConnection{Host: tenant.Spec.Database.Hostname, Port: port}
		if external := classisServerExternalDatabase(tenant); external != nil {
			connection.DBName, connection.User = external.DBName, external.User
			connection.PasswordSecret, connection.CASecret = external.CredentialsSecretName, external.CredentialsSecretName
		}
		return connection
	}
	// CloudNativePG creates the application user and its Secret, the Services of
	// the primary, and the CA that signs the server certificates.
	return classisServerDatabaseConnection{Host: classisServerDatabaseName + "-rw", Port: 5432, DBName: "classis", User: "classis", PasswordSecret: classisServerDatabaseName + "-app", CASecret: classisServerDatabaseName + "-ca"}
}

func classisServerExternalDatabase(tenant *neteye.NetEyeTenant) *neteye.ClassisServerExternalDatabaseSpec {
	if spec := tenant.Spec.ClassisServer; spec != nil && spec.Database != nil {
		return spec.Database.External
	}
	return nil
}

func classisServerInternalDatabase(tenant *neteye.NetEyeTenant) *neteye.ClassisServerInternalDatabaseSpec {
	if spec := tenant.Spec.ClassisServer; spec != nil && spec.Database != nil {
		return spec.Database.Internal
	}
	return nil
}

// classisServerLocalDatabase tells whether the operator runs the database.
func classisServerLocalDatabase(tenant *neteye.NetEyeTenant) bool {
	return tenant.Spec.Database == nil || tenant.Spec.Database.Type == neteye.TenantDatabaseLocal
}

// classisServerConfig renders the TOML configuration. Secrets are paths of the
// files the Deployment mounts; the S3 access key is the only value that comes
// from the environment.
func classisServerConfig(tenant *neteye.NetEyeTenant) string {
	spec := tenant.Spec.ClassisServer
	database := classisServerDatabase(tenant)
	var b strings.Builder
	logFormat := spec.LogFormat
	if logFormat == "" {
		logFormat = "json"
	}
	fmt.Fprintf(&b, "listen = \"0.0.0.0:%d\"\n\n[log]\nformat = %q\n\n", classisServerAgentPort, logFormat)
	// The server speaks plain HTTP: a reverse proxy in front of it ends TLS and
	// authenticates the agents. The server itself admits every request of the agent
	// plane and refuses an [agents] section, so none is rendered.
	fmt.Fprintf(&b, "[rest]\nlisten = \"0.0.0.0:%d\"\n\n", classisServerAdminPort)
	fmt.Fprintf(&b, "[rest.auth]\nbasic_users_file = \"%s/basic-users\"\n\n", classisServerMount("admin-users"))
	fmt.Fprintf(&b, "[database]\nstore = \"postgres\"\n\n[database.postgres]\nhost = %q\nport = %d\ndbname = %q\nuser = %q\npassword_file = \"%s/password\"\nca_file = \"%s/ca.crt\"\ntls = true\n\n", database.Host, database.Port, database.DBName, database.User, classisServerMount("db-password"), classisServerMount("db-ca"))
	blobs := spec.Blobs
	fmt.Fprintf(&b, "[blobs]\nstore = \"s3\"\n\n[blobs.s3]\nendpoint = %q\nbucket = %q\nregion = %q\nprefix = %q\nsecret_access_key_file = \"%s/secret-access-key\"\n", blobs.Endpoint, blobs.Bucket, blobs.Region, blobs.Prefix, classisServerMount("s3-credentials"))
	if ca := tenant.Spec.CABundle; ca != nil {
		fmt.Fprintf(&b, "ca_file = \"%s/%s\"\n", classisServerMount("ca-bundle"), ca.Key)
	}
	return b.String()
}

func classisServerMount(volume string) string { return "/etc/classis/" + volume }

func secretVolume(name, secretName string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: ptr.To(classisServerSecretMode)}}}
}

// classisServerDeployment builds the Deployment of the server. One replica and
// the Recreate strategy: the database lock lets only one server run, so the old
// pod stops before the new one starts. The root filesystem is read-only and the
// only writable volume is an emptyDir for /tmp: the server keeps its records in
// PostgreSQL and its artifacts in S3.
func classisServerDeployment(tenant *neteye.NetEyeTenant, configVersion, secretsVersions string) *appsv1.Deployment {
	spec := tenant.Spec.ClassisServer
	database := classisServerDatabase(tenant)
	labels := classisServerSelector()

	volumes := []corev1.Volume{
		{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: classisServerName}}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		secretVolume("admin-users", classisServerAdminUsersSecret(spec)),
		secretVolume("db-password", database.PasswordSecret),
		secretVolume("db-ca", database.CASecret),
		secretVolume("s3-credentials", spec.Blobs.CredentialsSecretName),
	}
	mounts := []corev1.VolumeMount{
		{Name: "config", MountPath: "/etc/classis/config", ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}
	for _, name := range []string{"admin-users", "db-password", "db-ca", "s3-credentials"} {
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: classisServerMount(name), ReadOnly: true})
	}
	if ca := tenant.Spec.CABundle; ca != nil {
		volumes = append(volumes, corev1.Volume{Name: "ca-bundle", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ca.Name}, Items: []corev1.KeyToPath{{Key: ca.Key, Path: ca.Key}}}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "ca-bundle", MountPath: classisServerMount("ca-bundle"), ReadOnly: true})
	}

	// /livez and /readyz are served on the operator plane, outside its Basic
	// authentication, over plain HTTP. The startup probe allows 300 s (60 times
	// 5 s): at startup the server retries PostgreSQL and S3 for up to 180 s, and
	// the kubelet must not kill it while it does.
	probe := func(path string) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Scheme: corev1.URISchemeHTTP, Port: intstr.FromString("admin")}}, PeriodSeconds: 10, TimeoutSeconds: 2, FailureThreshold: 3}
	}
	startup := probe("/readyz")
	startup.PeriodSeconds, startup.FailureThreshold = 5, 60

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: classisServerName, Namespace: tenant.Namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{classisServerConfigVersionAnnotation: configVersion, classisServerSecretsVersionsAnnotation: secretsVersions}},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  ptr.To(false),
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To(classisServerUID),
						RunAsGroup:     ptr.To(classisServerUID),
						FSGroup:        ptr.To(classisServerUID),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:  classisServerName,
						Image: spec.Image,
						Args:  []string{"--config", "/etc/classis/config/" + classisServerConfigFile},
						Ports: []corev1.ContainerPort{
							{Name: "agent", ContainerPort: classisServerAgentPort},
							{Name: "admin", ContainerPort: classisServerAdminPort},
						},
						Env: []corev1.EnvVar{
							{Name: "RUST_LOG", Value: spec.LogLevel},
							{Name: classisServerEnvPrefix + "BLOBS__S3__ACCESS_KEY_ID", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: spec.Blobs.CredentialsSecretName}, Key: "access-key-id"}}},
						},
						VolumeMounts:   mounts,
						LivenessProbe:  probe("/livez"),
						ReadinessProbe: probe("/readyz"),
						StartupProbe:   startup,
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   ptr.To(true),
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// classisServerService exposes the two planes inside the cluster. The proxies of
// the PCS nodes publish the agent plane; the operator plane stays internal, and
// the network policy decides who reaches it.
func classisServerService(tenant *neteye.NetEyeTenant) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: classisServerName, Namespace: tenant.Namespace, Labels: classisServerSelector()},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: classisServerSelector(),
			Ports: []corev1.ServicePort{
				{Name: "agent", Port: classisServerAgentPort, TargetPort: intstr.FromString("agent"), Protocol: corev1.ProtocolTCP},
				{Name: "admin", Port: classisServerAdminPort, TargetPort: intstr.FromString("admin"), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// classisServerDatabaseSpec is the spec of the CloudNativePG cluster: PostgreSQL 18
// with the instances of the tenant, one database and its owner, and TCP
// keepalives, so that a connection to a dead node does not outlive it.
//
// The instances never share a node: the anti-affinity is required, not
// preferred. The volumes are local to their node, so two instances on one node
// would be lost together. The price is that, with two instances on a cluster
// with a single worker, the second instance stays Pending; such a cluster
// must set instances to 1.
func classisServerDatabaseSpec(spec *neteye.ClassisServerInternalDatabaseSpec) map[string]any {
	instances, image, size := int64(2), "ghcr.io/cloudnative-pg/postgresql:18", "5Gi"
	storage := map[string]any{}
	if spec != nil {
		if spec.Instances > 0 {
			instances = int64(spec.Instances)
		}
		if spec.ImageName != "" {
			image = spec.ImageName
		}
		if spec.StorageSize != "" {
			size = spec.StorageSize
		}
		if spec.StorageClassName != "" {
			storage["storageClass"] = spec.StorageClassName
		}
	}
	storage["size"] = size
	return map[string]any{
		"instances":             instances,
		"imageName":             image,
		"enableSuperuserAccess": false,
		"affinity": map[string]any{
			"enablePodAntiAffinity": true,
			"podAntiAffinityType":   "required",
			"topologyKey":           "kubernetes.io/hostname",
		},
		"bootstrap": map[string]any{"initdb": map[string]any{"database": "classis", "owner": "classis"}},
		"storage":   storage,
		"postgresql": map[string]any{"parameters": map[string]any{
			"tcp_keepalives_idle":     "30",
			"tcp_keepalives_interval": "10",
			"tcp_keepalives_count":    "3",
		}},
	}
}

func cidrPeers(cidrs []string) []any {
	peers := make([]any, 0, len(cidrs))
	for _, cidr := range cidrs {
		peers = append(peers, map[string]any{"ipBlock": map[string]any{"cidr": cidr}})
	}
	return peers
}

func podSelector(labels map[string]string) map[string]any {
	match := map[string]any{}
	for k, v := range labels {
		match[k] = v
	}
	return map[string]any{"matchLabels": match}
}

func namespaceNamed(name string) map[string]any {
	return map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": name}}
}

var dnsEgress = map[string]any{
	"to":    []any{map[string]any{"namespaceSelector": namespaceNamed("kube-system"), "podSelector": podSelector(map[string]string{"k8s-app": "kube-dns"})}},
	"ports": append(resources.NetworkPolicyPorts("UDP", 53), resources.NetworkPolicyPorts("TCP", 53)...),
}

// anyPeerPorts is a rule that names no peer: the ports are open to any source
// or destination. A native policy cannot name the Kubernetes API, the nodes or
// a service outside the cluster, so these rules are widened to the ports, as in
// the policies of the other components.
func anyPeerPorts(ports []any) map[string]any {
	return map[string]any{"ports": ports}
}

// s3Port is the port of the S3 endpoint, 443 when the URL names none.
func s3Port(endpoint string) int64 {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return 443
	}
	port, err := strconv.ParseInt(parsed.Port(), 10, 32)
	if err != nil || port <= 0 {
		return 443
	}
	return port
}

// classisServerNetworkPolicySpec opens, over the default deny of the tenant, the
// planes of the server, its way to PostgreSQL, S3 and DNS.
//
// DECISION PENDING, SECURITY: the Server authenticates NO agent. The agent plane
// (4320) admits every request, and any peer that reaches the port can present
// itself as any host (and, as the Server trusts X-Forwarded-For from any source,
// under any address). The port MUST be reachable only through the proxy that
// authenticates the agents. Today this policy admits ANY source on 4320, so any
// pod of the cluster can talk to the server without passing the proxy: the
// sources still have to be narrowed (the proxy of the PCS nodes, or the external
// authentication library) before agents are connected.
//
// The operator plane (4321) is open to any source too, unless adminIngressCIDRs
// narrows it: a native policy cannot select the nodes, and the proxy and the
// Basic credentials of the plane limit who gets in. The policy lists no
// rule for the kubelet: the kubelet probes 4321 from the node, and Cilium
// admits the traffic of the local node to its pods whatever the policies say.
// The egress to S3 and to an external database is open to any destination on
// its ports, because the service may be inside the cluster or outside it.
func classisServerNetworkPolicySpec(tenant *neteye.NetEyeTenant) map[string]any {
	spec := tenant.Spec.ClassisServer
	network := spec.Network
	ingress := []any{anyPeerPorts(resources.NetworkPolicyPorts("TCP", classisServerAgentPort))}
	if len(network.AdminIngressCIDRs) > 0 {
		ingress = append(ingress, map[string]any{"from": cidrPeers(network.AdminIngressCIDRs), "ports": resources.NetworkPolicyPorts("TCP", classisServerAdminPort)})
	} else {
		ingress = append(ingress, anyPeerPorts(resources.NetworkPolicyPorts("TCP", classisServerAdminPort)))
	}
	database := classisServerDatabase(tenant)
	egress := []any{dnsEgress}
	ports := []int64{443}
	if port := s3Port(spec.Blobs.Endpoint); port != 443 {
		ports = append(ports, port)
	}
	if classisServerLocalDatabase(tenant) {
		egress = append(egress, map[string]any{
			"to":    []any{map[string]any{"podSelector": podSelector(map[string]string{"cnpg.io/cluster": classisServerDatabaseName})}},
			"ports": resources.NetworkPolicyPorts("TCP", int64(database.Port)),
		})
	} else {
		ports = append(ports, int64(database.Port))
	}
	egress = append(egress, anyPeerPorts(resources.NetworkPolicyPorts("TCP", ports...)))
	return map[string]any{
		"podSelector": podSelector(classisServerSelector()),
		"policyTypes": []any{"Ingress", "Egress"},
		"ingress":     ingress,
		"egress":      egress,
	}
}

// classisServerDatabaseNetworkPolicySpec opens the PostgreSQL instances: the
// server and the instances themselves on the database port, the CloudNativePG
// operator on the status port, and the Kubernetes API for the instance manager.
// A native policy cannot name the Kubernetes API, so the egress on its ports
// (443 through the Service, 6443 on the node) is open to any destination.
func classisServerDatabaseNetworkPolicySpec() map[string]any {
	instances := podSelector(map[string]string{"cnpg.io/cluster": classisServerDatabaseName})
	ports := resources.NetworkPolicyPorts("TCP", 5432, 8000)
	egress := []any{
		dnsEgress,
		map[string]any{"to": []any{map[string]any{"podSelector": instances}}, "ports": ports},
		anyPeerPorts(resources.NetworkPolicyPorts("TCP", 443, 6443)),
	}
	return map[string]any{
		"podSelector": instances,
		"policyTypes": []any{"Ingress", "Egress"},
		"ingress": []any{
			map[string]any{"from": []any{map[string]any{"podSelector": podSelector(classisServerSelector())}}, "ports": resources.NetworkPolicyPorts("TCP", 5432)},
			map[string]any{"from": []any{map[string]any{"podSelector": instances}}, "ports": ports},
			map[string]any{"from": []any{map[string]any{"namespaceSelector": namespaceNamed(cnpgSystemNamespace)}}, "ports": ports},
		},
		"egress": egress,
	}
}

// classisServerComponents returns the lifecycle nodes and operations of the
// server, or none when the tenant has no server.
func (r *NetEyeTenantReconciler) classisServerComponents(ctx context.Context, tenant *neteye.NetEyeTenant) ([]lifecycleNode, map[componentID]componentOperation) {
	spec := tenant.Spec.ClassisServer
	if spec == nil {
		return nil, nil
	}
	nodes := []lifecycleNode{{ID: classisServerComponentID, Dependencies: []componentID{tenantNamespaceComponentID}}}
	operations := map[componentID]componentOperation{
		classisServerComponentID: func() (componentResult, error) { return r.reconcileClassisServer(ctx, tenant) },
	}
	if spec.Enabled && classisServerLocalDatabase(tenant) {
		nodes = append(nodes, lifecycleNode{ID: classisServerDatabaseComponentID, Dependencies: []componentID{tenantNamespaceComponentID}})
		nodes[0].Dependencies = append(nodes[0].Dependencies, classisServerDatabaseComponentID)
		operations[classisServerDatabaseComponentID] = func() (componentResult, error) { return r.reconcileClassisServerDatabase(ctx, tenant) }
	}
	return nodes, operations
}

// reconcileClassisServerDatabase creates the CloudNativePG cluster of the tenant
// and its network policy, and waits for the cluster to be ready. An External
// database creates nothing.
// TODO: back up the cluster before an upgrade of the server. The backup needs an
// object store, which the tenant does not have yet.
func (r *NetEyeTenantReconciler) reconcileClassisServerDatabase(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	// The CloudNativePG operator is installed once for the cluster, by the first
	// tenant that needs it, and stays when that tenant goes. neteye-setup creates
	// its namespace, as it does for Keycloak; this only asks OLM for the operator.
	operator := cnpg.NewComponent(r.Client, r.Log)
	if err := operator.EnsureOperatorExtension(ctx); errors.Is(err, cnpg.ErrOperatorNamespaceMissing) {
		return progressingResult(classisServerDatabaseComponentID, "WaitingForCNPGNamespace", "waiting for neteye-setup to create the cnpg-system namespace, where the CloudNativePG operator is installed", r.progressingRequeue())
	} else if err != nil {
		return degradedResult(classisServerDatabaseComponentID, "CloudNativePGInstallFailed", fmt.Sprintf("failed to install the CloudNativePG operator: %v", err), r.failureRequeue(), err)
	}
	if ready, message, err := operator.IsReady(ctx); err != nil {
		return degradedResult(classisServerDatabaseComponentID, "CloudNativePGInstallFailed", err.Error(), r.failureRequeue(), err)
	} else if !ready {
		return progressingResult(classisServerDatabaseComponentID, "WaitingForCloudNativePG", message, r.progressingRequeue())
	}
	if _, err := resources.Apply(ctx, r.Client, resources.ObjectDefinition{GVK: cnpgClusterGVK, Name: classisServerDatabaseName, Namespace: tenant.Namespace, Spec: classisServerDatabaseSpec(classisServerInternalDatabase(tenant)), Owner: &owner}); err != nil {
		if meta.IsNoMatchError(err) {
			return progressingResult(classisServerDatabaseComponentID, "WaitingForCloudNativePG", "waiting for the CloudNativePG Cluster API", r.progressingRequeue())
		}
		reason := "DatabaseFailed"
		return degradedResult(classisServerDatabaseComponentID, reason, fmt.Sprintf("failed to apply the CloudNativePG cluster: %v", err), r.failureRequeue(), err)
	}
	if err := resources.EnsureNetworkPolicy(ctx, r.Client, tenant.Namespace, classisServerDatabaseName, classisServerDatabaseNetworkPolicySpec(), owner); err != nil {
		return degradedResult(classisServerDatabaseComponentID, "NetworkPolicyFailed", fmt.Sprintf("failed to apply the database network policy: %v", err), r.failureRequeue(), err)
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: classisServerDatabaseName}, cluster); err != nil {
		return degradedResult(classisServerDatabaseComponentID, "DatabaseFailed", err.Error(), r.failureRequeue(), err)
	}
	ready, message, err := resources.ReadyConditionMessage(cluster, "CloudNativePG cluster")
	if err != nil {
		return degradedResult(classisServerDatabaseComponentID, "DatabaseFailed", err.Error(), r.failureRequeue(), err)
	}
	if !ready {
		return progressingResult(classisServerDatabaseComponentID, "WaitingForDatabase", message, r.progressingRequeue())
	}
	return readyResult(classisServerDatabaseComponentID, "Available", "The database cluster is ready")
}

// reconcileClassisServer applies the configuration, the Service, the network
// policy and the Deployment of the server, then reports the Deployment state.
// A disabled server loses its Deployment and nothing else.
func (r *NetEyeTenantReconciler) reconcileClassisServer(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	spec := tenant.Spec.ClassisServer
	if !spec.Enabled {
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: classisServerName, Namespace: tenant.Namespace}}
		if err := r.Delete(ctx, deployment); err != nil && !apierrors.IsNotFound(err) {
			return degradedResult(classisServerComponentID, "DeploymentFailed", err.Error(), r.failureRequeue(), err)
		}
		return readyResult(classisServerComponentID, "Disabled", "The Classis Server is disabled")
	}
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	if err := r.ensureGeneratedAdminSecrets(ctx, tenant); err != nil {
		return degradedResult(classisServerComponentID, "AdminSecretFailed", err.Error(), r.failureRequeue(), err)
	}
	if missing, err := r.missingClassisServerSecrets(ctx, tenant); err != nil {
		return degradedResult(classisServerComponentID, "SecretCheckFailed", err.Error(), r.failureRequeue(), err)
	} else if len(missing) > 0 {
		return progressingResult(classisServerComponentID, "WaitingForSecrets", "waiting for Secrets: "+strings.Join(missing, ", "), r.progressingRequeue())
	}

	config := classisServerConfig(tenant)
	configVersion, err := resources.EnsureConfigMap(ctx, r.Client, tenant.Namespace, classisServerName, map[string]string{classisServerConfigFile: config}, owner)
	if err != nil {
		return degradedResult(classisServerComponentID, "ConfigFailed", err.Error(), r.failureRequeue(), err)
	}
	if err := resources.EnsureService(ctx, r.Client, classisServerService(tenant), owner); err != nil {
		return degradedResult(classisServerComponentID, "ServiceFailed", err.Error(), r.failureRequeue(), err)
	}
	if err := resources.EnsureNetworkPolicy(ctx, r.Client, tenant.Namespace, classisServerName, classisServerNetworkPolicySpec(tenant), owner); err != nil {
		return degradedResult(classisServerComponentID, "NetworkPolicyFailed", err.Error(), r.failureRequeue(), err)
	}
	secretsVersions, err := r.classisServerSecretsVersions(ctx, tenant)
	if err != nil {
		return degradedResult(classisServerComponentID, "SecretCheckFailed", err.Error(), r.failureRequeue(), err)
	}
	if err := resources.EnsureDeployment(ctx, r.Client, classisServerDeployment(tenant, configVersion, secretsVersions), owner); err != nil {
		return degradedResult(classisServerComponentID, "DeploymentFailed", err.Error(), r.failureRequeue(), err)
	}
	ready, message, err := resources.IsDeploymentReady(ctx, r.Client, tenant.Namespace, classisServerName)
	if err != nil {
		return degradedResult(classisServerComponentID, "DeploymentFailed", err.Error(), r.failureRequeue(), err)
	}
	if !ready {
		return progressingResult(classisServerComponentID, "WaitingForDeployment", message, r.progressingRequeue())
	}
	return readyResult(classisServerComponentID, "Available", "The Classis Server is ready")
}

// missingClassisServerSecrets names the Secrets the pod mounts that do not exist
// yet, so that the pod is not created only to stay in ContainerCreating.
func (r *NetEyeTenantReconciler) missingClassisServerSecrets(ctx context.Context, tenant *neteye.NetEyeTenant) ([]string, error) {
	spec := tenant.Spec.ClassisServer
	names := map[string]struct{}{}
	for _, name := range []string{classisServerAdminUsersSecret(spec), spec.Blobs.CredentialsSecretName} {
		if name != "" {
			names[name] = struct{}{}
		}
	}
	if !classisServerLocalDatabase(tenant) {
		names[classisServerDatabase(tenant).PasswordSecret] = struct{}{}
	}
	var missing []string
	for name := range names {
		err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: name}, &corev1.Secret{})
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, name)
		case err != nil:
			return nil, fmt.Errorf("get Secret %q: %w", name, err)
		}
	}
	sort.Strings(missing)
	return missing, nil
}
