# ADR-0004: Delegated Operator Lifecycle and Ownership

- **Status:** Proposed
- **Date:** 2026-09-04

## Context

Some NetEye components are managed through dedicated Kubernetes operators.
Keycloak is one example. The NetEye Operator creates the custom resource that
describes the required component, while the delegated operator creates and
manages its workloads.

These delegated operators must be installed before their custom resources can
be reconciled. Their API, behavior, and application support are part of the
tested NetEye software set.

NetEye is deployed on a dedicated Kubernetes cluster. Supporting independent
or customer-managed versions of the same delegated operator is therefore not a
requirement.

The installation must remain deterministic. An automatic update of a delegated
operator must not introduce a combination that was not tested with the active
NetEye release.

## Decision

### Ownership

The NetEye Operator manages the OLM `ClusterCatalog` and `ClusterExtension`
resources required to install delegated operators.

These resources are shared dependencies of the cluster-wide `NetEye` resource.
`NetEyeTenant` resources may use APIs provided by them, but must not create,
change, or delete their OLM resources.

The NetEye Operator does not manage the `ClusterCatalog` or `ClusterExtension`
that installs the NetEye Operator itself. That bootstrap lifecycle remains
owned by installation automation, as defined in ADR-0003.

The ownership rules from ADR-0002 apply. The NetEye Operator uses Server-Side
Apply for the fields it owns and restores those fields when they drift. It
recreates a managed dependency resource if it is deleted.

An existing object with the expected name is not silently adopted. If the
operator cannot establish that the object belongs to the NetEye installation,
it reports an ownership conflict and leaves the object unchanged. Such a
collision is an unsupported installation state.

### Dependency descriptors

Each embedded NetEye release manifest contains a descriptor for every required
delegated operator. The descriptor identifies at least:

- the catalog source;
- the OLM package;
- the installation namespace;
- the exact tested bundle version;
- the API resources that must be available before use.

The exact version is authoritative. The `ClusterExtension` must not permit an
untested update through a broad version range.

A compatible delegated-operator update is delivered by changing the embedded
descriptor in a NetEye Operator patch. A breaking update belongs to a new
NetEye product release and is applied only after a `NetEyeUpgrade` authorizes
the corresponding product upgrade.

NetEye does not maintain a compatibility promise for arbitrary delegated
operator versions. Testing an alternative version requires a purpose-built or
experimental NetEye Operator bundle.

### Reconciliation order

The NetEye Operator reconciles a delegated component in this order:

1. Apply the required `ClusterCatalog`.
2. Wait until the catalog is available to OLM.
3. Apply the required `ClusterExtension`.
4. Wait until OLM reports a successful installation.
5. Verify that the required API resources are served by the Kubernetes API.
6. Apply the delegated custom resource.
7. Observe the delegated operator's status to determine component readiness.

Creating the delegated custom resource before its API is available is not an
installation strategy. Reconciliation remains level-based and resumes from the
current cluster state after restarts or transient failures.

During an update, existing delegated custom resources remain in place while
OLM updates their operator. A dependency is not removed until no managed
component requires its APIs and all required migrations or deletions have
completed.

### Failure and drift

The NetEye Operator must not ignore a missing, modified, or failed dependency.
It first attempts to restore its declared state.

If the catalog or extension cannot become ready, the operator does not create
or update custom resources that require it. The affected component reports a
stable dependency failure in its status. Independent components may continue
reconciling, but the top-level `Ready` condition remains false.

The same behavior applies if the installed API does not match the descriptor,
even when OLM reports the extension as installed. The operator does not guess
that an unknown API is compatible.

Kubernetes events and logs provide additional installation details, but the
custom-resource status remains the stable interface for users and automation.

### Deletion

Delegated operators follow the lifecycle of the shared `NetEye` resource.

With `deletionPolicy: Retain`, the operator retains:

- delegated custom resources;
- their `ClusterExtension` resources;
- the `ClusterCatalog` resources needed to keep those extensions manageable.

This allows retained workloads and data to continue being reconciled by their
delegated operators.

With `deletionPolicy: Delete`, cleanup follows dependency order:

1. Delete delegated custom resources and wait for their deletion to complete.
2. Delete their `ClusterExtension` resources.
3. Delete a `ClusterCatalog` only after no retained or managed extension uses
   it.

The operator must not remove a delegated operator while custom resources that
require its finalizers still exist. A failed cleanup remains visible and keeps
the `NetEye` finalizer in place rather than abandoning partially deleted
resources.

Cluster-scoped OLM resources cannot be owned by a namespaced `NetEye` resource
through a normal owner reference. The operator therefore tracks them explicitly
as required by ADR-0002.

## Alternatives considered

### Let installation automation manage delegated operators

This would keep cluster-scoped OLM permissions outside the NetEye Operator. It
was not chosen because dependency versions would need to be synchronized
separately with every NetEye release, and component readiness would depend on
an external installation step.

### Allow OLM to select any version in a compatible range

This would deliver delegated-operator updates without a NetEye Operator
release. It was not chosen because a version satisfying a SemVer range is not
necessarily a version tested with NetEye.

### Reuse customer-managed delegated operators

This would make NetEye coexist with another owner of the dependency lifecycle.
It was not chosen because NetEye clusters are dedicated and the additional
compatibility and ownership model is unnecessary.

### Ignore unexpected dependency versions

This was not chosen because reconciliation could then use an unknown API or
behavior. The operator restores the desired version or reports a visible
failure.

### Include delegated controllers in the NetEye Operator

This would remove the OLM dependency resources. It was not chosen because it
would merge independent controllers, permissions, release cycles, and upstream
responsibilities into one process.

### Tie dependencies to the NetEye Operator installation

This would install delegated operators even when no `NetEye` resource exists
and would bypass its deletion policy. Tying them to the shared resource gives
the dependency set one explicit desired-state and cleanup root.

## Consequences

The NetEye Operator needs cluster-wide permissions to manage the required
`ClusterCatalog` and `ClusterExtension` resources.

Creating a `NetEye` resource starts an asynchronous bootstrap process. NetEye
cannot become ready until OLM installs every operator required by its enabled
components.

Every delegated-operator version is reviewed and tested as part of the NetEye
release set. Updating one requires a new NetEye Operator image and bundle, even
when NetEye controller code does not change.

Automatic delegated-operator updates cannot move beyond the version selected
by the installed NetEye Operator. This reduces unexpected changes but makes
NetEye responsible for publishing dependency security updates promptly.

An installation cannot bring its own version of a required delegated operator.
This is acceptable for a dedicated NetEye cluster and avoids a wide
compatibility matrix.

Dependency installation and failure state must be represented in the relevant
component status. Independent components continue to reconcile, while global
readiness remains false until all required dependencies are available.

Deletion requires an explicit inventory and ordered cleanup of cluster-scoped
OLM resources. `Retain` keeps the delegated controllers needed by retained
resources; `Delete` removes them only after their custom resources are gone.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- [ADR-0006: NetEye Upgrade Coordination](0006-neteye-upgrade-coordination.md)
- [OLM v1 architecture](https://operator-framework.github.io/operator-controller/project/olmv1_architecture/)
- [OLM v1 version ranges](https://operator-framework.github.io/operator-controller/concepts/version-ranges/)
