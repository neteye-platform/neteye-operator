# ADR-0005: Component Lifecycle and Dependency Orchestration

- **Status:** Proposed
- **Date:** 2026-09-04

## Context

A complete NetEye installation contains many components. Some components can
be reconciled independently, while others require another component or API to
be ready first.

Initial installation and product upgrades do not always require the same
ordering. Upgrades can also contain data migrations or immutable-resource
changes that do not exist during installation.

A single hard-coded workflow would become difficult to understand and maintain
as the component set grows. Fully independent component controllers would have
the opposite problem: no layer would understand cross-component ordering or
the progress of the installation as a whole.

The operator must make progress after restarts and partial failures. It must
also avoid shaping its internal lifecycle around Ansible or other systems that
manage resources outside Kubernetes.

## Decision

### Component contract

Every component has a stable logical identifier and owns its component-specific
behavior. This includes:

- observing its current state;
- applying its desired Kubernetes resources;
- deciding whether it is ready;
- defining its migration steps;
- cleaning up its owned resources.

The global orchestrator does not contain component business logic. It resolves
the desired release, evaluates dependencies, and schedules eligible component
work.

Component reconcilers consume resolved release data. They do not contain
scattered checks for NetEye product versions. Behavior that exists only for a
specific forward transition is implemented as an explicit migration.

### Lifecycle graphs

Release data contains separate directed acyclic graphs for:

- initial installation of a NetEye release;
- every supported forward product upgrade.

Graph nodes use stable component identifiers. Edges express only precedence:
a node cannot perform its lifecycle transition until its prerequisites have
completed successfully.

The upgrade graph may also contain external gates. These are scheduling nodes,
not executable logic. Their ownership and acknowledgement protocol are defined
in ADR-0006.

CI and operator startup validate that:

- every referenced component and migration exists;
- every dependency refers to a known node;
- the graphs contain no cycles;
- every desired component appears in the applicable graph;
- a supported upgrade has exactly one graph for its source and target.

Invalid lifecycle data prevents the operator from becoming ready.

Compatible component patches within one NetEye release use normal
reconciliation and the installation dependencies. They must remain compatible
with already-running components. A change that requires a special
cross-component migration or incompatible ordering belongs to a new NetEye
product release and its upgrade graph.

### Scheduling

On every reconciliation, the orchestrator derives eligible work from the
desired release, the lifecycle graph, and observed cluster state. It does not
depend on an in-memory queue or on having observed every earlier event.

All nodes whose prerequisites are satisfied may progress concurrently. Work
for one component is serialized so that two lifecycle operations cannot modify
the same component at the same time.

A dependency edge gates a lifecycle transition. It does not stop a component
from observing its resources or reporting status. Existing workloads are not
deleted or stopped merely because a dependency becomes unavailable.

During normal reconciliation, component controllers continue correcting drift
in the fields they own according to ADR-0002.

The maximum amount of concurrent work is an implementation and operational
tuning detail. It does not change the graph semantics.

### Migration steps

A migration has a stable identifier and belongs to one explicit forward
release transition. Every step must be safe to observe and retry.

The operator supports these execution forms:

- component-specific controller logic for Kubernetes API operations;
- a versioned Kubernetes `Job` for data or application migrations;
- waiting for an observed Kubernetes condition or API capability;
- an external gate coordinated through ADR-0006.

Migration jobs use an exact, digest-pinned image selected by the release data.
Their identity and result must be observable after a controller restart.

The operator does not include an Ansible executor, SSH client, or generic
facility for running external commands. Actions outside Kubernetes remain
owned by external automation and are represented only by explicit gates.

An immutable-resource change must have a component-specific migration. The
operator does not fall back to generic delete and recreate behavior.

### Progress and recovery

Successful step completion cannot exist only in process memory. The operator
must be able to determine it again from Kubernetes resources, durable migration
markers, and the status of the active upgrade.

Stored progress is a checkpoint and diagnostic aid, not a substitute for
idempotence. A step must remain safe if the operator cannot determine whether a
previous attempt completed.

When a step fails:

- the failed node and its descendants stop progressing;
- independent branches continue;
- the failure is reported in component and top-level status;
- the same step is retried automatically with normal controller backoff.

After the cause is corrected, reconciliation resumes from the failed step. A
user does not manually skip a required migration, restart the complete plan, or
mark a component successful.

The operator does not automatically roll back completed steps. NetEye upgrades
move forward, and recovery reconciles the observed partial state toward the
same target release.

`NetEye.status.currentVersion` advances only after every desired component is
ready and every required migration and external gate has completed. Until then,
partial target state is visible through component status and resolved images.

### Deletion order

Deletion uses the reverse of the installation dependency graph by default. A
dependent component is removed before the component on which it depends.

This ordering is subject to the ownership and `deletionPolicy` rules in
ADR-0001 and the delegated-operator cleanup rules in ADR-0004. Resources that
must be retained are not made deletable merely because they appear in the
graph.

A separate deletion graph is not introduced until a real lifecycle requires
ordering that cannot be expressed safely by reversing installation
dependencies.

## Alternatives considered

### Use one ordered list for the whole installation

This would be easy to execute but would serialize independent components and
turn every new ordering requirement into a change to one global workflow.

### Use one graph for installation and upgrades

This was not chosen because product upgrades can require ordering and
migrations that do not apply to a new installation.

### Let every component coordinate its own dependencies

This would keep the global controller small, but dependency knowledge would be
duplicated and no layer could produce a deterministic whole-installation plan.

### Stop the complete plan after any failure

This would simplify failure reporting but would prevent unrelated components
from reaching a healthy state.

### Persist and execute an imperative workflow queue

This would provide an explicit cursor, but recovery would depend on the stored
cursor matching the real cluster. Recomputing eligible work from observed state
is safer and follows Kubernetes reconciliation semantics.

### Execute Ansible or arbitrary scripts from the operator

This would allow one workflow to control every migration. It was not chosen
because it would mix ownership domains, credentials, failure models, and legacy
infrastructure behavior into the Kubernetes controller.

### Require a manual retry or allow migrations to be skipped

This would provide operator control over failures but would make recovery
procedural and could produce unsupported partial releases. Required steps retry
automatically and cannot be skipped.

### Automatically roll back the upgrade

This was not chosen because completed data and schema migrations may be
irreversible, and ADR-0003 does not support downgrades.

## Consequences

The lifecycle engine remains small as components are added. It schedules typed
component operations but does not absorb their implementation details.

Release authors must maintain and review installation and upgrade graphs. CI
must reject invalid graphs and missing migration implementations.

Independent components can install or upgrade concurrently. A partial upgrade
is therefore expected and must remain observable, restart-safe, and supported
until the failed branch recovers.

Every migration requires an idempotence strategy and durable observable state.
Database migrations normally require dedicated, versioned job images rather
than code embedded in the controller process.

External automation remains outside the operator. This creates an explicit
coordination boundary, defined in ADR-0006, instead of hiding Ansible execution
inside a component reconciler.

Deletion ordering is available without maintaining a third graph. An explicit
deletion graph can be introduced later only when a demonstrated requirement
justifies the additional lifecycle model.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- [ADR-0004: Delegated Operator Lifecycle and Ownership](0004-delegated-operator-lifecycle-and-ownership.md)
- [ADR-0006: NetEye Upgrade Coordination](0006-neteye-upgrade-coordination.md)
