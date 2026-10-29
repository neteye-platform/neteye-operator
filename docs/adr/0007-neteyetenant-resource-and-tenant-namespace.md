# ADR-0007: NetEyeTenant Resource and Tenant Namespace

- **Status:** Proposed
- **Date:** 2026-10-29

## Context

ADR-0001 introduces the `NetEyeTenant` custom resource but does not define its
shape. NetEye already has tenants outside Kubernetes, and the next components
that move to Kubernetes are tenant-specific. The operator is in production, so
the first tenant release must not be a breaking change, and later components
must be additive.

The operator must not treat any tenant as special. The first release only
introduces the tenant and its namespace. Tenant components, such as a Keycloak
realm for each tenant, are later additions.

## Decision

### Resource, namespace, and names

A tenant is one namespace that contains one `NetEyeTenant`. The namespace and
the `NetEyeTenant` have the same name: `neteye-tenant-<id>`. The id is
generated once, outside the operator, and never changes. The namespace and
`metadata.name` of a tenant never change.

The admission webhook accepts a `NetEyeTenant` only when its name starts with
`neteye-tenant-`, is not the shared namespace `neteye-tenant-shared`, and
equals its namespace. This also allows only one tenant in each namespace.

`spec.name` is the name that people see. It is free text of 1 to 63
characters, without surrounding whitespace or control characters, and unique
among tenants without regard to case. It can change at any time. Derived names
use `metadata.name`, so a change of `spec.name` moves nothing.

No tenant has a special status. The code reads fields of the resource, never
the identity of a tenant.

### Namespace metadata

The tenant namespace has the label `neteye.cloud/tenant` with the name of the
tenant, and the annotation `neteye.cloud/tenant-display-name` with
`spec.name`. Scripts select tenant namespaces with the label, not with the
name prefix. The operator writes both.

A `ValidatingAdmissionPolicy` that the operator manages lets only the operator
service account change them on a tenant namespace. The API server evaluates
the policy, so it keeps working while the operator is down, and it never
affects other namespaces or other metadata.

### Network baseline

The operator puts the default-deny `CiliumNetworkPolicy` that the shared
namespace already has into every tenant namespace. It denies all ingress and
all egress. A tenant component that the operator adds later opens only the
traffic it needs, with its own allow policies.

### Status

The status follows ADR-0002: `observedGeneration`, the `Ready` condition,
and a `components` map. The `namespace` component reports the namespace
metadata and the network baseline.

### Deletion

Deleting a tenant never deletes shared resources. Every object the tenant
creates lives in the tenant namespace and has the tenant as owner, so garbage
collection removes it. The cluster-scoped admission policy is shared by all
tenants and stays. A `deletionPolicy` field for tenants is not part of this
decision; it can be added later as an optional field.

## Alternatives considered

### Keep the tenant in the shared namespace

This was not chosen because a namespaced owner and its dependents must be in
the same namespace. Tenant workloads in their own namespace could not have the
tenant as owner, and Kubernetes RBAC could not isolate one tenant.

### Create the Keycloak realm of the tenant in the first release

This was not chosen because no component uses a tenant realm yet. The realm,
and the credentials needed to manage it, come with the first component that
needs them.

### Protect the namespace metadata with a webhook

This was not chosen because a webhook on namespaces depends on the operator.
When the operator is down, it either blocks changes to every namespace in the
cluster or stops protecting the metadata.

### Show `spec.name` in a namespace label

A label works with `kubectl get -L` and with selectors. This was not chosen
because a label value cannot hold free text.

### Leave the tenant namespace open until a component needs a policy

This was not chosen because a workload placed in the namespace would then run
without any network restriction. Starting closed makes every later allow rule
explicit.

## Consequences

Every tenant has its own namespace, closed by default, with a display name that
only the operator can change.

The operator owns a cluster-scoped admission policy shared by all tenants. A
cluster administrator who must change the protected namespace metadata has to
remove the policy binding first.

Later tenant components add optional sections to `NetEyeTenant`, new keys to
the status `components` map, and allow policies in the tenant namespace.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
