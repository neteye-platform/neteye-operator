# ADR-0001: NetEye Resource Scope and Ownership

- **Status:** Accepted
- **Date:** 2026-09-03

## Context

NetEye moves step by step from RHEL services to Kubernetes. Ansible installs
the Kubernetes cluster and the NetEye Operator. Ansible also creates the
`NetEye` custom resource. Then the operator manages the Kubernetes resources
for that `NetEye` resource.

There is one NetEye installation in each cluster. If two systems change the
same resource, the configuration drifts and the reconciliation does not stop.
Thus each resource must have one owner.

## Decision

### One NetEye per cluster

- A cluster contains a maximum of one `NetEye` resource.
- The `NetEye` resource must be in the namespace `neteye-tenant-shared`. The
  shared workloads are also in this namespace.
- The validating webhook rejects a create or an update of a `NetEye` resource
  if a different `NetEye` resource exists.
- The controller also uses the Lease `neteye-cluster-authority` in
  `neteye-tenant-shared`. The holder of the Lease is the UID of the `NetEye`
  resource. The `NetEye` resource owns the Lease.
- If a `NetEye` resource does not hold the Lease, the controller sets its
  phase to `Failed`. The controller does not change other resources for it.

### Resource classes

Each resource that the operator uses is in one of these classes:

- **Owned:** The operator creates the resource. The resource has a controller
  owner reference to the `NetEye` resource.
- **Delegated:** The operator owns a custom resource of a different operator.
  The other operator owns the resources that it makes from that custom
  resource.
- **External:** A user or other automation manages the resource. The operator
  only reads it.

The operator does not adopt resources that it did not create. ADR-0002 gives
the rules.

### Delegated components

- The operator owns the Keycloak custom resource `neteye-kc`
  (`k8s.keycloak.org`). The Keycloak Operator owns the Pods and StatefulSets.
- The operator installs the Keycloak Operator. It applies the OLM
  `ClusterExtension` `keycloak-operator` with the channel `fast` from the
  catalog `operatorhubio`, into the namespace `keycloak-system`.
- The `ClusterExtension` has no owner. The operator applies it at start and
  every 10 minutes, also if no `NetEye` resource exists. The operator does not
  delete it.

### External resources

These resources are external:

- The cert-manager `Issuer` in `spec.internalCertificateIssuerRef`.
- The Secrets that the spec refers to, for example database credentials.
- The external MariaDB database and the Elasticsearch endpoints.

The operator does not change or delete external resources. If the `Issuer` is
missing, the operator sets the phase to `Failed` and stops the reconciliation.

### Configuration interface

- The `NetEye` spec is the supported configuration interface.
- Do not edit owned resources to configure NetEye. The operator replaces the
  spec of an owned resource on the next reconciliation.
- If a necessary setting is missing, add it to the `NetEye` API.
- The webhook rejects Keycloak options, Keycloak features, and telemetry
  attributes that the operator manages, for example `service.name`.
- ADR-0005 gives a different rule for the Keycloak configuration resources
  that the operator creates.

### Deletion

- The `NetEye` resource has no finalizer and no deletion policy.
- When you delete the `NetEye` resource, Kubernetes garbage collection deletes
  all owned resources. This includes the Keycloak custom resource, the
  Gateway, the routes, the Certificates, the network policies, the telemetry
  workloads, and the Lease.
- External resources, the `ClusterExtension`, and the Keycloak configuration
  resources (ADR-0005) stay.
- When you disable `spec.elasticStack`, the operator deletes the owned
  OpenTelemetry Collector and EDOT Gateway resources.

## Alternatives considered

### Allow more than one NetEye resource in a cluster

We did not select this option. There is no production requirement for it.
Also, the owner of the shared and cluster-scoped resources is not clear.

### Use only the webhook for the singleton rule

We did not select this option. A webhook can be bypassed or not available. The
Lease makes sure that only one `NetEye` resource changes the shared resources.

### Let Ansible and the operator manage the same resources

We did not select this option. The two systems would overwrite each other.

### Allow direct changes to owned resources

We did not select this option. The custom resource would then not show the
full configuration. Also, the reconciliation would remove the changes.

## Consequences

- Each cluster has one source of desired state for the shared resources.
- Independent installations, for example development and production, need
  different clusters.
- The `NetEye` API must grow when users need new settings.
- If you delete the `NetEye` resource, there is no option to keep the owned
  resources.
- When you delete the `NetEye` resource, garbage collection also deletes the
  Lease. Then a new `NetEye` resource can get the Lease.

## References

- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0005: Keycloak Configuration Resources](0005-keycloak-configuration-resources.md)
