// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package controllers

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/cnpg"
	"github.com/neteye-platform/neteye-operator/internal/resources"
)

const (
	tenantDatabaseComponentID componentID = "database"

	// tenantDatabaseName names the CloudNativePG cluster of the tenant, shared
	// by all its services. CloudNativePG derives the Secrets and the Services
	// from it.
	tenantDatabaseName = "neteye-tenant-db"
	tenantDatabasePort = 5432
	// tenantDatabaseStatusPort is the port of the instance manager, which the
	// CloudNativePG operator and the other instances reach.
	tenantDatabaseStatusPort = 8000

	// tenantDatabaseClientLabel marks the pods of the services that reach the
	// PostgreSQL of the tenant: its network policy admits them and no others.
	tenantDatabaseClientLabel = "neteye.cloud/database-client"

	// cnpgSystemNamespace is where the CloudNativePG operator runs. Its pods
	// reach the status port of every instance.
	cnpgSystemNamespace = "cnpg-system"
)

var (
	cnpgClusterGVK      = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	cnpgDatabaseGVK     = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Database"}
	cnpgDatabaseRoleGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "DatabaseRole"}
)

// tenantDatabaseAccess is the database and the role of one service on a local
// PostgreSQL of the tenant. The role has the name of the database and owns it;
// the pg_hba rules of the cluster let it reach that database and nothing else.
type tenantDatabaseAccess struct {
	DBName string
	// Secret is the kubernetes.io/basic-auth Secret of the role, generated once
	// by the operator.
	Secret string
}

// tenantDatabaseAccesses lists the database of every service of the tenant.
// A disabled service keeps its database, so that enabling it again finds its
// records.
func tenantDatabaseAccesses(tenant *neteye.NetEyeTenant) []tenantDatabaseAccess {
	var accesses []tenantDatabaseAccess
	if server := tenant.Spec.ClassisServer; server != nil {
		accesses = append(accesses, tenantDatabaseAccess{DBName: server.EffectiveDBName(), Secret: classisServerDBCredentialsSecretName})
	}
	return accesses
}

// tenantDatabaseEndpoint is where the services reach the PostgreSQL of the
// tenant, and the Secret key of the CA that signs its certificate.
type tenantDatabaseEndpoint struct {
	Host     string
	Port     int32
	CASecret neteye.NetEyeSecretKeySelector
	Local    bool
}

func tenantDatabaseEndpointOf(tenant *neteye.NetEyeTenant) tenantDatabaseEndpoint {
	database := tenant.Spec.Database
	if database.IsLocal() {
		// CloudNativePG creates the Service of the primary and the CA that signs
		// the server certificates of the instances.
		return tenantDatabaseEndpoint{Host: tenantDatabaseName + "-rw", Port: tenantDatabasePort, CASecret: neteye.NetEyeSecretKeySelector{Name: tenantDatabaseName + "-ca", Key: "ca.crt"}, Local: true}
	}
	external := database.External
	if external == nil {
		return tenantDatabaseEndpoint{}
	}
	return tenantDatabaseEndpoint{Host: external.Host, Port: external.EffectivePort(), CASecret: external.CASecret}
}

// tenantDatabaseHBA admits each service role to its own database over TLS
// with a password, and refuses every other connection over the network. The
// rules land after the fixed rules of CloudNativePG (local access, and the
// certificate rules of its replication user) and before its catch-all rule,
// which the final reject makes unreachable.
func tenantDatabaseHBA(accesses []tenantDatabaseAccess) []any {
	rules := make([]any, 0, len(accesses)+1)
	for _, access := range accesses {
		rules = append(rules, fmt.Sprintf("hostssl %s %s all scram-sha-256", access.DBName, access.DBName))
	}
	return append(rules, "host all all all reject")
}

// tenantDatabaseClusterSpec is the spec of the CloudNativePG cluster:
// PostgreSQL with the replicas of the tenant, the pg_hba rules of its
// services, and TCP keepalives, so that a connection to a dead node does not
// outlive it.
//
// The instances never share a node: the anti-affinity is required, not
// preferred. The volumes are local to their node, so two instances on one node
// would be lost together. The price is that, with two replicas on a cluster
// with a single worker, the second instance stays Pending; such a cluster
// must set replicas to 1.
func tenantDatabaseClusterSpec(tenant *neteye.NetEyeTenant, image string, accesses []tenantDatabaseAccess) map[string]any {
	database := tenant.Spec.Database
	storage := map[string]any{"size": database.EffectiveStorageSize()}
	if class := database.EffectiveStorageClassName(); class != "" {
		storage["storageClass"] = class
	}
	return map[string]any{
		"instances":             int64(database.EffectiveReplicas()),
		"imageName":             image,
		"enableSuperuserAccess": false,
		"affinity": map[string]any{
			"enablePodAntiAffinity": true,
			"podAntiAffinityType":   "required",
			"topologyKey":           "kubernetes.io/hostname",
		},
		"storage": storage,
		"postgresql": map[string]any{
			"parameters": map[string]any{
				"tcp_keepalives_idle":     "30",
				"tcp_keepalives_interval": "10",
				"tcp_keepalives_count":    "3",
			},
			"pg_hba": tenantDatabaseHBA(accesses),
		},
	}
}

// tenantDatabaseNetworkPolicySpec opens the PostgreSQL instances: the pods
// marked as database clients on the database port, the instances themselves
// and the CloudNativePG operator on the database and status ports, and the
// Kubernetes API for the instance manager. A native policy cannot name the
// Kubernetes API, so the egress on its ports (443 through the Service, 6443 on
// the node) is open to any destination.
func tenantDatabaseNetworkPolicySpec() map[string]any {
	instances := podSelector(map[string]string{"cnpg.io/cluster": tenantDatabaseName})
	ports := resources.NetworkPolicyPorts("TCP", tenantDatabasePort, tenantDatabaseStatusPort)
	egress := []any{
		dnsEgress,
		map[string]any{"to": []any{map[string]any{"podSelector": instances}}, "ports": ports},
		anyPeerPorts(resources.NetworkPolicyPorts("TCP", 443, 6443)),
	}
	return map[string]any{
		"podSelector": instances,
		"policyTypes": []any{"Ingress", "Egress"},
		"ingress": []any{
			map[string]any{"from": []any{map[string]any{"podSelector": podSelector(map[string]string{tenantDatabaseClientLabel: "true"})}}, "ports": resources.NetworkPolicyPorts("TCP", tenantDatabasePort)},
			map[string]any{"from": []any{map[string]any{"podSelector": instances}}, "ports": ports},
			map[string]any{"from": []any{map[string]any{"namespaceSelector": namespaceNamed(cnpgSystemNamespace)}}, "ports": ports},
		},
		"egress": egress,
	}
}

// tenantStorageComponents returns the lifecycle nodes and operations of the
// database and of the object store of the tenant, those it has.
func (r *NetEyeTenantReconciler) tenantStorageComponents(ctx context.Context, tenant *neteye.NetEyeTenant) ([]lifecycleNode, map[componentID]componentOperation) {
	var nodes []lifecycleNode
	operations := map[componentID]componentOperation{}
	if tenant.Spec.Database != nil {
		nodes = append(nodes, lifecycleNode{ID: tenantDatabaseComponentID, Dependencies: []componentID{tenantNamespaceComponentID}})
		operations[tenantDatabaseComponentID] = func() (componentResult, error) { return r.reconcileTenantDatabase(ctx, tenant) }
	}
	if tenant.Spec.ObjectStorage != nil {
		nodes = append(nodes, lifecycleNode{ID: tenantObjectStorageComponentID, Dependencies: []componentID{tenantNamespaceComponentID}})
		operations[tenantObjectStorageComponentID] = func() (componentResult, error) { return r.reconcileTenantObjectStorage(ctx, tenant) }
	}
	return nodes, operations
}

// reconcileTenantDatabase creates the CloudNativePG cluster of the tenant, its
// network policy, and the database and the role of each service, and waits for
// all of them. An external database creates nothing: its databases and roles
// are the business of whoever runs it.
// TODO: back up the cluster before an upgrade. The backup needs an object
// store, which the tenant now has.
func (r *NetEyeTenantReconciler) reconcileTenantDatabase(ctx context.Context, tenant *neteye.NetEyeTenant) (componentResult, error) {
	if !tenant.Spec.Database.IsLocal() {
		return readyResult(tenantDatabaseComponentID, "External", "The database is external")
	}
	owner := resources.OwnerReference(neteye.GroupVersion.String(), "NetEyeTenant", tenant)
	images, err := r.tenantImages(ctx)
	if err != nil {
		return r.imagesResult(tenantDatabaseComponentID, err)
	}
	// The CloudNativePG operator is installed once for the cluster, by the first
	// tenant that needs it, and stays when that tenant goes. neteye-setup creates
	// its namespace, as it does for Keycloak; this only asks OLM for the operator.
	operator := cnpg.NewComponent(r.Client, r.Log)
	if err := operator.EnsureOperatorExtension(ctx); errors.Is(err, cnpg.ErrOperatorNamespaceMissing) {
		return progressingResult(tenantDatabaseComponentID, "WaitingForCNPGNamespace", "waiting for neteye-setup to create the cnpg-system namespace, where the CloudNativePG operator is installed", r.progressingRequeue())
	} else if err != nil {
		return degradedResult(tenantDatabaseComponentID, "CloudNativePGInstallFailed", fmt.Sprintf("failed to install the CloudNativePG operator: %v", err), r.failureRequeue(), err)
	}
	if ready, message, err := operator.IsReady(ctx); err != nil {
		return degradedResult(tenantDatabaseComponentID, "CloudNativePGInstallFailed", err.Error(), r.failureRequeue(), err)
	} else if !ready {
		return progressingResult(tenantDatabaseComponentID, "WaitingForCloudNativePG", message, r.progressingRequeue())
	}
	accesses := tenantDatabaseAccesses(tenant)
	if _, err := resources.Apply(ctx, r.Client, resources.ObjectDefinition{GVK: cnpgClusterGVK, Name: tenantDatabaseName, Namespace: tenant.Namespace, Spec: tenantDatabaseClusterSpec(tenant, images.PostgreSQLImage, accesses), Owner: &owner}); err != nil {
		if meta.IsNoMatchError(err) {
			return progressingResult(tenantDatabaseComponentID, "WaitingForCloudNativePG", "waiting for the CloudNativePG Cluster API", r.progressingRequeue())
		}
		return degradedResult(tenantDatabaseComponentID, "DatabaseFailed", fmt.Sprintf("failed to apply the CloudNativePG cluster: %v", err), r.failureRequeue(), err)
	}
	if err := resources.EnsureNetworkPolicy(ctx, r.Client, tenant.Namespace, tenantDatabaseName, tenantDatabaseNetworkPolicySpec(), owner); err != nil {
		return degradedResult(tenantDatabaseComponentID, "NetworkPolicyFailed", fmt.Sprintf("failed to apply the database network policy: %v", err), r.failureRequeue(), err)
	}
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: tenantDatabaseName}, cluster); err != nil {
		return degradedResult(tenantDatabaseComponentID, "DatabaseFailed", err.Error(), r.failureRequeue(), err)
	}
	ready, message, err := resources.ReadyConditionMessage(cluster, "CloudNativePG cluster")
	if err != nil {
		return degradedResult(tenantDatabaseComponentID, "DatabaseFailed", err.Error(), r.failureRequeue(), err)
	}
	if !ready {
		return progressingResult(tenantDatabaseComponentID, "WaitingForDatabase", message, r.progressingRequeue())
	}
	for _, access := range accesses {
		applied, err := r.ensureTenantDatabaseAccess(ctx, tenant, access, owner)
		if err != nil {
			return degradedResult(tenantDatabaseComponentID, "DatabaseAccessFailed", fmt.Sprintf("failed to set up the database %q: %v", access.DBName, err), r.failureRequeue(), err)
		}
		if applied != "" {
			return progressingResult(tenantDatabaseComponentID, "WaitingForDatabaseAccess", applied, r.progressingRequeue())
		}
	}
	return readyResult(tenantDatabaseComponentID, "Available", "The database cluster and the databases of the services are ready")
}

// ensureTenantDatabaseAccess creates the credentials, the role and the
// database of one service. It returns a message while CloudNativePG has not
// applied them yet, and an empty one once both are in PostgreSQL. The role and
// the database are retained when their resources go: deleting the records of a
// service takes a person.
func (r *NetEyeTenantReconciler) ensureTenantDatabaseAccess(ctx context.Context, tenant *neteye.NetEyeTenant, access tenantDatabaseAccess, owner metav1.OwnerReference) (string, error) {
	if err := r.ensureDatabaseCredentials(ctx, tenant.Namespace, access, owner); err != nil {
		return "", err
	}
	name := tenantDatabaseName + "-" + access.DBName
	cluster := map[string]any{"name": tenantDatabaseName}
	role := map[string]any{"cluster": cluster, "name": access.DBName, "login": true, "ensure": "present", "passwordSecret": map[string]any{"name": access.Secret}, "databaseRoleReclaimPolicy": "retain"}
	database := map[string]any{"cluster": cluster, "name": access.DBName, "owner": access.DBName, "ensure": "present", "databaseReclaimPolicy": "retain"}
	for _, object := range []struct {
		gvk  schema.GroupVersionKind
		spec map[string]any
	}{{cnpgDatabaseRoleGVK, role}, {cnpgDatabaseGVK, database}} {
		if _, err := resources.Apply(ctx, r.Client, resources.ObjectDefinition{GVK: object.gvk, Name: name, Namespace: tenant.Namespace, Spec: object.spec, Owner: &owner}); err != nil {
			return "", fmt.Errorf("apply the CloudNativePG %s %q: %w", object.gvk.Kind, name, err)
		}
	}
	for _, gvk := range []schema.GroupVersionKind{cnpgDatabaseRoleGVK, cnpgDatabaseGVK} {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(gvk)
		if err := r.Get(ctx, types.NamespacedName{Namespace: tenant.Namespace, Name: name}, live); err != nil {
			return "", fmt.Errorf("get the CloudNativePG %s %q: %w", gvk.Kind, name, err)
		}
		if applied, _, _ := unstructured.NestedBool(live.Object, "status", "applied"); !applied {
			message, _, _ := unstructured.NestedString(live.Object, "status", "message")
			if message == "" {
				message = "not applied yet"
			}
			return fmt.Sprintf("waiting for CloudNativePG to apply the %s %q: %s", gvk.Kind, access.DBName, message), nil
		}
	}
	return "", nil
}

// ensureDatabaseCredentials creates the basic-auth Secret of the role of a
// service once, with a random password, and never overwrites it. CloudNativePG
// reads it for the role, and reloads the role when it changes.
func (r *NetEyeTenantReconciler) ensureDatabaseCredentials(ctx context.Context, namespace string, access tenantDatabaseAccess, owner metav1.OwnerReference) error {
	existing, err := r.getSecretIfPresent(ctx, namespace, access.Secret)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Type != corev1.SecretTypeBasicAuth || len(existing.Data[corev1.BasicAuthUsernameKey]) == 0 || len(existing.Data[corev1.BasicAuthPasswordKey]) == 0 {
			return fmt.Errorf("the Secret %q is not a basic-auth Secret with a username and a password: delete it to have it generated again", access.Secret)
		}
		return nil
	}
	password, err := randomPassword(classisServerAdminPasswordLength)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: access.Secret, Labels: map[string]string{"cnpg.io/reload": "true"}, OwnerReferences: []metav1.OwnerReference{owner}},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{corev1.BasicAuthUsernameKey: []byte(access.DBName), corev1.BasicAuthPasswordKey: []byte(password)},
	}
	if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the Secret %q: %w", access.Secret, err)
	}
	return nil
}
