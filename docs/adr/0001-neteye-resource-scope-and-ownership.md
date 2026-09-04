# ADR-0001: NetEye Resource Scope and Ownership

- **Status:** Proposed
- **Date:** 2026-09-03

## Context

NetEye is being migrated gradually from services running on RHEL and managed by
systemd, PCS, and Ansible to applications running on Kubernetes.

During this transition, Ansible installs the Kubernetes cluster and everything
required to install the NetEye Operator. Ansible also creates and updates the
`NetEye` custom resource. After that point, the operator must manage the
Kubernetes resources represented by the custom resource.

Some NetEye components are shared by the whole installation. Keycloak is one
example. Other components need a separate instance for each tenant. The exact
classification of every component is not yet complete and will change as more
components are moved to Kubernetes.

We need clear ownership boundaries. Without them, Ansible, users, and operators
could update the same Kubernetes resources. This would cause configuration
drift and repeated reconciliation conflicts.

NetEye is normally deployed as one installation per Kubernetes cluster. We do
not have a production requirement for multiple independent NetEye
installations in the same cluster.

## Decision

### NetEye installation

A Kubernetes cluster can contain only one active `NetEye` custom resource.
This resource represents the shared part of the NetEye installation.

The singleton rule applies to the whole cluster, regardless of the namespace
of the `NetEye` resource. The operator must not reconcile a second `NetEye`
resource. The enforcement mechanism is an implementation detail and is not
defined by this ADR.

There can be multiple `NetEyeTenant` custom resources. Each one represents one
tenant and its dedicated resources. Tenant resources may use shared services,
but they must not own or delete them.

Every component managed by the operator must be classified as one of the
following before it is added to reconciliation:

- **Shared:** owned by the `NetEye` resource.
- **Tenant-specific:** owned by one `NetEyeTenant` resource.
- **External:** referenced by NetEye but managed outside the NetEye Operator.

A Kubernetes resource must not be managed by both `NetEye` and
`NetEyeTenant`. The exact component classification will be documented as the
components are migrated. It is not decided by this ADR.

### Configuration ownership

During the transition, Ansible owns the installation of Kubernetes, the
installation of the operator, and the desired content of the `NetEye` resource.
The operator owns the Kubernetes resources that it creates from that desired
state. Ansible must not also manage those generated resources.

When the migration to Kubernetes is complete, users or their automation will
own the `NetEye` resource. The custom resource API is the supported interface
for configuring shared components.

Users must not edit generated resources to configure NetEye. If a required
setting is missing, it must be added to the custom resource API. This keeps the
desired state explicit, reviewable, and reproducible.

When the NetEye Operator delegates a component to another operator, the NetEye
Operator owns the component's custom resource. The delegated operator owns the
resources generated from it. For example, the NetEye Operator can own a
Keycloak custom resource without owning the Keycloak Pods or StatefulSets.

External resources are referenced, not adopted. The NetEye Operator must not
change or delete them unless a separate decision explicitly assigns that
ownership.

### Deletion

The `NetEye` custom resource exposes a `deletionPolicy`. It supports at least
the following behaviors:

- `Retain`: resources containing installation data must be preserved.
  Resources that contain no data and are safe to recreate may be deleted.
- `Delete`: resources owned by the `NetEye` installation may be deleted,
  including data-bearing resources.

`Retain` is the default because deleting a custom resource must not cause
unexpected data loss.

External resources are never deleted by either policy.

Before a component is implemented, its data-bearing resources and any
additional resources required for recovery must be identified. This produces a
component-specific deletion contract without requiring this ADR to contain a
list that will quickly become outdated.

The deletion behavior of `NetEyeTenant` resources will be defined separately.
Deleting a tenant must never delete resources owned by the shared `NetEye`
resource.

## Alternatives considered

### Allow multiple NetEye resources in one cluster

This would allow multiple independent installations to share a cluster. It was
not chosen because there is no current production requirement for it. It would
also make the ownership of cluster-wide and shared resources more complex.

### Put shared and tenant-specific resources in one custom resource

This would reduce the number of API types. It was not chosen because the shared
installation and tenant instances have different lifecycles and ownership.

### Let Ansible and the operator manage the same Kubernetes resources

This was not chosen because both systems could continuously overwrite each
other. The handover from Ansible to the operator must have a clear boundary.

### Allow direct customization of generated resources

This was not chosen because the custom resource would no longer describe the
complete desired configuration. Reconciliation could also remove direct
changes without a clear explanation to the user.

### Always delete all resources with the NetEye resource

This was not chosen because deleting the custom resource could cause accidental
and irreversible data loss.

## Consequences

The operator has one clear source of desired state for shared resources and one
clear source for each tenant.

Shared resources cannot be duplicated by creating another `NetEye` resource in
a different namespace. Development and production installations that need
independent shared components must use separate clusters.

The custom resource API will grow when users need new supported configuration.
This requires API design and compatibility work, but avoids hidden
customization outside the declared desired state.

Every new component needs an explicit ownership and deletion classification.
This adds design work, but prevents ambiguous lifecycle behavior.

The operator needs a cluster-wide singleton check and deletion handling that
implements `Retain` and `Delete` safely.

## References

None.
