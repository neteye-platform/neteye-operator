# ADR-0007: Tenant Namespace and Isolation Model

- **Status:** Proposed
- **Date:** 2026-09-04

## Context

ADR-0001 defines one cluster-wide `NetEye` resource for shared components and
multiple `NetEyeTenant` resources for tenant-specific components. The tenant
resources need a predictable placement, identity, ownership boundary, and
deletion lifecycle.

Tenant workloads process data for different customers. A mistake or compromise
in one tenant must not provide access to another tenant. Namespace separation
alone does not provide this guarantee because Kubernetes allows network traffic
between namespaces unless policies restrict it.

The tenant namespace also has a different lifecycle from the resources inside
it. A namespaced custom resource cannot exist before its namespace and cannot
own resources in another namespace. The namespace provisioning and cleanup
order must therefore be explicit.

Some technologies provide both one shared service and tenant-specific
resources. Their shared and tenant parts have different owners and lifecycles
and must remain distinguishable.

## Decision

### Resource placement

The singleton `NetEye` resource lives in the `neteye-tenant-shared` namespace.
That namespace contains the shared part of the NetEye installation.

Each tenant has one dedicated namespace and one `NetEyeTenant` resource. The
`NetEyeTenant` is namespaced and lives inside its tenant namespace. A tenant
namespace cannot contain a second active `NetEyeTenant`.

A `NetEyeTenant` binds implicitly to the singleton `NetEye` resource in
`neteye-tenant-shared`. It does not expose a user-selectable reference to a
different installation. If the singleton is missing or its required shared
components are not ready, the tenant reports that it is waiting and does not
start dependent components.

### Tenant identity

Tenant namespaces use this naming form:

```text
neteye-<tenant-id>
```

`tenant-id` is a unique, DNS-compatible, human-readable identifier selected
when the tenant is created. It is immutable. It is not recomputed from a
mutable customer or display name.

A tenant may expose a separate display name in its specification. Changing the
display name does not rename the namespace or change the tenant identity.
Kubernetes namespaces are not migrated to support a display-name change.

### Namespace lifecycle and ownership

Lifecycle automation owns the tenant namespace. It creates the namespace
before creating the `NetEyeTenant` resource. The operator does not create,
adopt, or delete the namespace.

The namespace is dedicated to one tenant. Lifecycle automation supplies any
required namespace-level configuration before creating the tenant resource.
The operator validates these prerequisites and does not create tenant
workloads when they are missing.

After the `NetEyeTenant` exists, the operator owns the Kubernetes resources
derived from it. Namespaced resources use owner references where Kubernetes
permits them. Other resources follow the explicit ownership rules in ADR-0002.

### Tenant isolation

Tenant namespaces are security boundaries between tenant workloads. A workload
in one tenant namespace must not access workloads or Kubernetes resources in
another tenant namespace by default.

Before starting tenant workloads, the operator applies and reconciles the
tenant isolation baseline. It includes at least:

- default-deny ingress and egress network policies;
- explicit network access required for DNS and shared NetEye services;
- explicit access to any other required destinations;
- tenant-specific service accounts and least-privilege RBAC;
- tenant-specific credentials for shared services.

Every cross-namespace network path must be intentional and represented by an
operator-managed policy. Allowing access to a shared service does not authorize
access to another tenant's data. Shared components must enforce tenant identity
and authorization at their own API and data boundaries.

Cluster administrators and the NetEye Operator remain trusted cluster-wide
actors. This ADR isolates tenant workloads from each other; it does not claim
to isolate them from a Kubernetes cluster administrator.

### Component scope

Each logical lifecycle component has exactly one scope: shared,
tenant-specific, or external, as defined in ADR-0001.

When one technology has both shared services and tenant-specific resources,
the two parts are represented as separate logical components. They have
separate owners, lifecycle graph nodes, and status entries. The tenant
component may depend on the shared component.

This rule allows the component list to evolve without creating a mixed
ownership boundary. The exact list of shared and tenant components is defined
as components are added to the product.

### Tenant deletion

`NetEyeTenant` exposes a `deletionPolicy` with the following behaviors:

- `Retain`: tenant data and resources required for recovery are preserved.
  Other resources may be removed according to their component-specific
  deletion contracts.
- `Delete`: all resources owned by the tenant may be deleted, including
  data-bearing resources.

`Retain` is the default. Neither policy deletes shared or external resources.

A finalizer keeps the `NetEyeTenant` present until the operator has completed
the cleanup allowed by its deletion policy. With `Retain`, lifecycle automation
must preserve the namespace. With `Delete`, lifecycle automation waits for the
tenant finalizer and resource deletion to complete, then deletes the namespace.

Deleting the namespace directly is not a supported tenant-deletion workflow.
It can bypass `deletionPolicy` and destroy resources that should have been
retained.

## Alternatives considered

### Make NetEyeTenant cluster-scoped

A cluster-scoped resource could allow the operator to create and own the tenant
namespace. It was not chosen because it would broaden the API and RBAC scope.
Lifecycle automation already has to provision the tenant boundary and can
perform the small, ordered namespace-creation step.

### Store NetEyeTenant in the shared namespace

This would let the operator observe every tenant resource in one place and
create tenant namespaces afterward. It was not chosen because namespaced owner
references cannot cross namespaces. The tenant CR and most of its resources
would have different Kubernetes ownership boundaries.

### Require an explicit NetEye reference

This would make the relationship visible in the tenant specification. It was
not chosen because ADR-0001 allows only one NetEye installation per cluster.
A selectable reference would add configuration without providing a valid
choice. The binding remains observable through tenant status.

### Use random namespace identifiers

Random identifiers avoid coupling the namespace to a customer name. They were
not chosen as the default because they make routine operations and incident
response harder. An immutable human-readable identifier provides stable
identity without following later display-name changes.

### Treat namespaces as organizational boundaries only

This would reduce network-policy and RBAC work. It was not chosen because a
compromised or misconfigured tenant workload could then reach another tenant.

### Model shared and tenant resources as one mixed component

This would keep one component name for each technology. It was not chosen
because one status entry and graph node would then have two owners and two
lifecycles.

### Let the operator delete tenant namespaces

This was not chosen because the namespace is created and owned by lifecycle
automation. Deleting the namespace from a finalizer on a resource inside that
namespace would also make cleanup and failure recovery harder to reason about.

## Consequences

Creating a tenant is a two-step operation: lifecycle automation creates the
namespace and then creates the tenant resource. The operator validates the
namespace and completes the tenant installation.

Namespace names remain useful during daily operations and stable across tenant
display-name changes. Changing the technical tenant identifier requires
creating a new tenant and performing an explicit migration.

Every tenant adds namespace, network-policy, RBAC, credential, lifecycle, and
status resources. This increases the resource count but provides a clear
security and ownership boundary.

Shared-service connectivity must be added explicitly. New tenant components
cannot assume unrestricted cluster networking.

Lifecycle automation must distinguish `Retain` from `Delete`. It must never
delete a retained tenant namespace merely because the `NetEyeTenant` resource
has disappeared.

Technologies with shared and tenant-specific behavior produce more than one
logical component entry. In return, their ownership, dependencies, failures,
and status remain unambiguous.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0005: Component Lifecycle and Dependency Orchestration](0005-component-lifecycle-and-dependency-orchestration.md)
- [Kubernetes Namespaces](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/)
- [Kubernetes Owners and Dependents](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)
- [Kubernetes Network Policies](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
