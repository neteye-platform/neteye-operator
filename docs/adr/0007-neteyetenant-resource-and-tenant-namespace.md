# ADR-0007: NetEyeTenant Resource and Tenant Namespace

- **Status:** Accepted
- **Date:** 2026-10-29

## Context

ADR-0001 introduces the `NetEyeTenant` custom resource but does not define its
shape. NetEye already has tenants outside Kubernetes, and the next components
that move to Kubernetes are tenant-specific. The operator is in production, so
the first tenant release must not be a breaking change, and later components
must be additive.

The operator must not treat any tenant as special. The first release only
introduces the tenant and its namespace. Tenant components come later.

## Decision

### Names

A tenant is one namespace that contains one `NetEyeTenant`. The namespace and
the `NetEyeTenant` have the same name: `neteye-tenant-<id>`. The id has
exactly 8 characters from `abcdefghjkmnpqrstuvwxyz23456789`, so characters
that look alike are never used. The id is generated once, by whoever creates
the tenant, and never changes.

The fixed format keeps every name derived from a tenant short enough for the
63 character limit of a DNS label, and it excludes the shared namespace
`neteye-tenant-shared`. Accepting more formats later is an additive change;
accepting fewer is not.

The CRD schema enforces the name format. The admission webhook enforces the
rules that the schema cannot express: the tenant lives in the namespace with
its own name, which also allows only one tenant in each namespace. The
controller checks both rules again and changes nothing for a tenant that
breaks them.

### Display name

`spec.displayName` is the name that people see. It can change at any time.
Derived names use `metadata.name`, so a change of the display name moves
nothing. The display name is unique among tenants, ignoring case and Unicode
normalization. The webhook checks it on creation and when the display name
changes; it does not lock the resource, so two concurrent creates can still
produce a duplicate. No other update is refused, so that an existing conflict
never blocks unrelated changes.

No tenant has a special status. The code reads fields of the resource, never
the identity of a tenant.

### Namespace metadata

The operator marks the tenant namespace with the label `neteye.cloud/tenant`,
whose value is the tenant name, and shows the display name in the annotation
`neteye.cloud/tenant-display-name`. Scripts select tenant namespaces with the
label, not with the name prefix.

Only the operator sets these two keys. A `ValidatingAdmissionPolicy` refuses
any other user who creates a namespace with them or changes them on an
existing namespace. So a namespace becomes a tenant namespace only through a
`NetEyeTenant`. The API server evaluates the policy, so it keeps working while
the operator is down.

To change the protected metadata by hand, stop the operator first, then delete
the policy binding. While the operator runs, it puts the binding back.

### Network baseline

The operator puts the default-deny `CiliumNetworkPolicy` that the shared
namespace already has into every tenant namespace. It denies all ingress and
all egress. A tenant component that the operator adds later opens only the
traffic it needs, with its own allow policies.

### Status

The status follows ADR-0002 and uses the same component status types as
`NetEye`: `observedGeneration`, the `Ready`, `Progressing` and `Degraded`
conditions, and a `components` map. Components run in dependency order, so a
later component can wait for the tenant namespace.

### Ownership

Classified according to ADR-0001:

- **Tenant namespace:** External. Whoever creates the tenant creates and owns
  the namespace. The operator owns only the tenant label and annotation on it.
- **Default-deny policy in the tenant namespace:** Tenant-specific, owned by
  the `NetEyeTenant`.
- **Admission policy and its binding:** part of the `NetEyeTenant` API. The
  `NetEyeTenant` CRD owns them, so they exist exactly as long as the API.

### Deletion

Deleting a tenant never deletes shared resources and never deletes the
namespace. Garbage collection removes what the tenant owns. A finalizer removes
the tenant label and annotation from the namespace, so the namespace no longer
counts as a tenant namespace.

The first tenant component that holds data, or that lives outside the tenant
namespace, must add a `deletionPolicy` field to `NetEyeTenant` with the default
`Retain`, as ADR-0001 requires. Adding it then is an additive change.

### Deferred decisions

This ADR does not decide:

- the Keycloak realm of a tenant and the credentials to manage it;
- RBAC for each tenant, and who may create tenants;
- optional fields that make a tenant use services outside Kubernetes;
- where PermissionSync runs;
- the `deletionPolicy` of a tenant.

## Alternatives considered

### Keep the tenant in the shared namespace

This was not chosen because a namespaced owner and its dependents must be in
the same namespace. Tenant workloads in their own namespace could not have the
tenant as owner, and Kubernetes RBAC could not isolate one tenant.

### Accept any id after the prefix

This was not chosen because derived names could exceed the length of a DNS
label, and restricting the format after release would invalidate existing
tenants.

### Protect the namespace metadata with a webhook

This was not chosen because a webhook on namespaces depends on the operator.
When the operator is down, it either blocks changes to every namespace in the
cluster or stops protecting the metadata.

### Show the display name in a namespace label

A label works with `kubectl get -L` and with selectors. This was not chosen
because a label value cannot hold free text.

### Leave the tenant namespace open until a component needs a policy

This was not chosen because a workload placed in the namespace would then run
without any network restriction. Starting closed makes every later allow rule
explicit.

### Delete the namespace together with the tenant

This was not chosen because the namespace is not owned by the tenant and will
hold data of later components. Deleting a namespace deletes everything in it.

## Consequences

Every tenant has its own namespace, closed by default, with tenant metadata
that only the operator can set.

The operator watches namespaces and the network policies it owns, so drift is
corrected at once.

Later tenant components add optional sections to `NetEyeTenant`, new keys to
the status `components` map, and allow policies in the tenant namespace.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
