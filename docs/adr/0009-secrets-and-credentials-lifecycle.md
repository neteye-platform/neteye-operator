# ADR-0009: Secrets and Credentials Lifecycle

- **Status:** Proposed
- **Date:** 2026-09-04

## Context

NetEye components need passwords, API tokens, client credentials, encryption
keys, and private keys. Some credentials exist only between Kubernetes-managed
components. Others grant access to systems owned by lifecycle automation or by
an external administrator. Delegated operators may also generate credentials
for the resources they manage.

These credentials have different ownership and recovery requirements. A
controller can safely replace a credential only when it controls every
endpoint that must accept the new value. Regenerating a missing database or
encryption credential without coordinating its consumers can make retained
data inaccessible.

ADR-0007 makes tenant namespaces security boundaries. Credentials must follow
the same shared and tenant scopes; one tenant must not receive a credential
that grants another tenant's access.

The project needs a consistent contract for credential sources, Kubernetes
Secret ownership, distribution, rotation, deletion, and recovery. The
contract must keep secret material out of the public custom-resource and
status APIs while allowing external secret-management systems to integrate
without becoming a mandatory platform dependency.

## Decision

### Credential contracts

Every credential is assigned a component-specific contract before the
component is added to reconciliation. The contract identifies at least:

- the controller or external actor that owns the credential value;
- whether the credential is generated or externally supplied;
- its shared or tenant namespace and every consumer;
- the Kubernetes Secret type, required keys, and validation rules;
- how rotation is initiated, staged, verified, and completed;
- whether safe automatic regeneration is possible;
- whether the credential is required to recover retained data.

Exactly one actor writes a credential value. Another controller may consume or
observe the Secret, but it must not compete for ownership of its data.

Credentials use one of the following ownership models:

- **Operator-generated:** The controller that owns the credential relationship
  generates the value and owns its Kubernetes Secret. The NetEye Operator uses
  this model when it controls the required relationship between NetEye
  components.
- **Delegated-operator-generated:** A delegated operator generates and owns a
  Secret defined by its API contract. The NetEye Operator observes or
  references that Secret but does not adopt or rewrite it.
- **Externally supplied:** Lifecycle automation, an administrator, or an
  external secret controller owns a pre-existing Kubernetes Secret. The NetEye
  Operator validates and consumes it without changing or deleting it.

The ownership model is fixed by the credential contract. Users do not switch a
credential between generated and external ownership through a generic option.
A change of owner requires an explicit component migration.

### Public API and references

Secret values never appear in `NetEye`, `NetEyeTenant`, or `NetEyeUpgrade`
specification or status fields. A custom resource may contain only a typed
reference to an externally supplied Secret and, when needed, its documented
key names.

Secret references are local to the custom resource's namespace:

- `NetEye` references Secrets in `neteye-tenant-shared`;
- `NetEyeTenant` references Secrets in its tenant namespace;
- `NetEyeUpgrade` does not carry secret values or act as a credential transport.

Cross-namespace Secret references are not supported. When an external secret
manager is used, it synchronizes the required value into a Secret in the
owning namespace. The NetEye Operator does not copy secret data between
namespaces.

A missing or malformed referenced Secret blocks only the components that need
it. Their status reports a stable machine-readable reason, and the owning
custom resource cannot become ready. The operator does not create a placeholder
or take ownership of the referenced Secret.

### Generation and reconciliation

Operator-generated credentials use a cryptographically secure random source
and a component-appropriate format and strength. They are not derived from
tenant names, resource identifiers, timestamps, or another predictable value.

The generated Secret is durable desired-state material. Ordinary
reconciliation, controller restarts, missing status, and operator updates do
not generate a new value. The controller first observes the existing owned
Secret and reuses it. Generated data changes only through the credential's
rotation or recovery contract.

Operator-generated Secrets are immutable by default. A normal rotation creates
a new Secret generation and switches consumers only after the accepting
endpoint is ready. A component may use a mutable generated Secret only when its
credential contract documents why versioned Secrets cannot be used and how
single-writer ownership and safe rotation remain enforced.

The ownership checks from ADR-0002 apply. If a Secret with the expected name
exists but is neither the explicitly referenced external Secret nor an object
owned by the expected controller, reconciliation stops and reports a conflict.
The operator does not adopt or overwrite it.

The controller applies Secret metadata and non-generated structure using the
normal resource-ownership rules. It never recomputes secret data as part of an
ordinary Server-Side Apply request.

### Storage, scope, and access

Kubernetes Secrets are the default storage and distribution mechanism for
credentials used by Kubernetes-managed NetEye components. An external secret
manager is optional and integrates by owning a referenced Kubernetes Secret;
it is not required by the NetEye Operator architecture.

The Kubernetes platform must encrypt Secret data at rest. Access to Secret
objects is restricted through least-privilege RBAC, and workloads receive only
the individual Secrets they require. Detailed platform encryption and operator
RBAC configuration are part of the security and authorization design.

Secret data is not copied into ConfigMaps, annotations, labels, command-line
arguments, events, logs, or status. Workloads consume it through Kubernetes
Secret references or mounted Secret volumes supported by the component.

Shared credentials live in `neteye-tenant-shared`. Tenant credentials live in
the corresponding tenant namespace and are unique to that tenant. A
tenant-facing credential for a shared service must authorize only that tenant's
identity and data; a shared administrative credential is never distributed to
tenant workloads.

Standard Kubernetes Secret types are used when their schema matches the
credential. Custom key names are stable parts of the component's API contract.

### Rotation

Rotation follows the credential's component-specific contract. There is no
generic rule that periodically replaces every NetEye Secret.

When the protocol supports overlapping credentials, rotation is staged:

1. Create a new credential generation without removing the current one.
2. Configure the accepting endpoint to accept the new generation.
3. Update all consumers and wait until they are ready with it.
4. Revoke the previous generation.
5. Delete the previous Secret when its retention contract permits it.

When overlap is not supported, the component contract defines an explicit
maintenance operation and readiness behavior. The operator must not claim a
zero-downtime rotation that the underlying protocol cannot provide.

Externally supplied credentials are rotated by their external owner. The
operator watches referenced Secrets and performs the component-specific reload
or rollout required to consume a valid update. It never rewrites the external
value.

The specification lock from ADR-0006 does not prevent an external owner from
updating a referenced Secret during a NetEye upgrade. If the affected component
cannot rotate safely while its upgrade work is active, the operator pauses or
degrades that component until rotation completes. It does not continue a
dependent migration using a mixture of credential generations.

Deleting a Secret is not a supported rotation request. Rotation state must be
reconstructable from owned Secrets and the observed state of all endpoints;
in-memory progress or status alone is insufficient.

### Missing operator-generated credentials

The operator automatically replaces a missing generated credential only when
its contract proves that every accepting endpoint and consumer is controlled
by a compatible controller and can be moved safely through the complete
rotation sequence.

If any endpoint can still require the missing value, or if the credential is
needed to decrypt or access retained data, the operator does not generate an
unrelated replacement. It reports the component as degraded with a stable
reason and waits for restoration or an explicit component recovery procedure.

This rule applies even when recreating a Secret would be technically easy. The
ability to create a value does not prove that existing state will accept it.

### Deletion, retention, and recovery

Credentials follow the deletion contract of the resource and component that
own them:

- With `Retain`, credentials required to access, decrypt, or manage retained
  resources are retained with those resources. Secrets that are safely
  recreatable and unnecessary for recovery may be revoked and deleted according
  to their component contract.
- With `Delete`, operator-owned credentials are revoked when supported and
  deleted after their consumers and protected resources have completed the
  required cleanup.
- Externally supplied Secrets are never deleted by the NetEye Operator.
- Tenant deletion never deletes a shared Secret.

The operator must prevent Kubernetes garbage collection from removing a Secret
that the `Retain` policy requires after its custom-resource owner disappears.
The exact retained-resource tracking mechanism follows ADR-0002.

Credentials required for recovery are part of the backup and restore contract
for their component. They must be restored before the dependent component is
started. Backup transport, encryption, and recovery procedures are defined by
the persistent-data and recovery design.

### Diagnostics and sensitive-data handling

Status and events may report the affected logical credential and Kubernetes
Secret reference when that metadata is not itself sensitive. They report no
secret value, encoded value, reversible derivative, or unsalted hash of secret
material.

Stable reasons distinguish at least a missing Secret, invalid Secret shape,
ownership conflict, rotation in progress, and rotation or recovery failure.
Human-readable messages remain diagnostic and are not a workflow API.

Controllers and Jobs must prevent credential values from appearing in command
output or error messages. Redaction at the logging layer is defense in depth,
not permission to pass secrets through observable fields.

TLS certificate issuance, trust distribution, and termination are defined by
the network exposure and TLS ADR. Certificate private keys and bootstrap
credentials still follow the sensitive-data, ownership, rotation, and
retention rules in this ADR.

## Alternatives considered

### Let lifecycle automation create every Secret

This would keep secret generation outside the operator. It was not chosen for
credentials that exist only between Kubernetes-managed components because it
would move component lifecycle knowledge and rotation ordering back into
Ansible.

### Put secret values in custom-resource specifications

This would make one manifest describe all inputs, but secret values would then
appear in GitOps repositories, resource histories, diffs, and custom-resource
read permissions granted to broader audiences. Typed Secret references preserve
the API boundary without making the CR a secret store.

### Require an external secret manager

This could centralize enterprise credential policy. It was not chosen as a
platform requirement because NetEye runs on isolated clusters and Kubernetes
Secrets are sufficient as the common runtime interface. External managers can
still integrate without changing component APIs.

### Allow users to select generated or external ownership for every credential

This would maximize flexibility but create field-ownership, migration, and
deletion transitions for every credential. A fixed component contract keeps
one writer and one supported lifecycle.

### Regenerate every missing generated Secret

This would maximize self-healing for stateless credentials. It was not chosen
as a general rule because a persistent service, encrypted data set, or external
endpoint may still require the missing value.

### Use one shared credential for all tenants

This would reduce the number of Secrets and server-side identities. It was not
chosen because compromise of one tenant would grant access outside that
tenant's security boundary and revocation could disrupt every tenant.

### Use Secret deletion as the rotation trigger

This would provide a simple operational action. It was not chosen because
deletion destroys the old generation before the operator has proven that every
endpoint can accept a replacement.

## Consequences

Every credential has one explicit writer and a lifecycle aligned with its
component owner. Ansible supplies credentials for external systems but does
not orchestrate internal service-to-service passwords.

Native Kubernetes Secrets keep the baseline deployment self-contained.
Platform encryption at rest, backup protection, and least-privilege Secret
RBAC become required security controls.

Tenant compromise does not expose a shared administrative credential or
another tenant's credentials. This requires separate identities and Secrets
for tenant access to shared services.

Components must implement and test their own rotation and recovery contracts.
Protocols that support overlapping credentials can rotate without downtime;
others require a visible maintenance operation.

Blind regeneration is deliberately limited. Some missing Secrets become
operator-visible recovery incidents instead of automatic self-healing, which
protects access to retained data.

Retaining data can also require retaining credentials. Backup and deletion
inventories must treat those Secrets as recovery-critical rather than as
disposable configuration.

External secret systems remain compatible by writing native Kubernetes
Secrets, but the NetEye Operator does not depend on or manage those systems.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0004: Delegated Operator Lifecycle and Ownership](0004-delegated-operator-lifecycle-and-ownership.md)
- [ADR-0006: NetEye Upgrade Coordination](0006-neteye-upgrade-coordination.md)
- [ADR-0007: Tenant Namespace and Isolation Model](0007-tenant-namespace-and-isolation-model.md)
- [ADR-0008: Custom Resource API Evolution and Compatibility](0008-custom-resource-api-evolution-and-compatibility.md)
- [Kubernetes Secrets](https://kubernetes.io/docs/concepts/configuration/secret/)
- [Encrypting confidential data at rest](https://kubernetes.io/docs/tasks/administer-cluster/encrypt-data/)
