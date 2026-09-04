# ADR-0006: NetEye Upgrade Coordination

- **Status:** Proposed
- **Date:** 2026-09-04

## Context

A NetEye product upgrade can require changes both inside and outside
Kubernetes. The NetEye Operator owns Kubernetes components. Ansible automation
owns migrations of systems outside Kubernetes.

Neither actor should execute the other's work. They still need a durable
protocol for ordering their actions, reporting progress, and resuming after a
failure or restart.

An upgrade must also use one stable NetEye specification. If configuration
changes while migrations are running, different components could apply the
same upgrade against different desired states.

Putting the target version in `NetEye.spec` would not provide transaction
identity, external gates, progress, or a recovery protocol.

## Decision

### Upgrade resource

`NetEyeUpgrade` is the authorization and coordination resource for a NetEye
product upgrade. Authorized lifecycle automation creates it in the same
namespace as the referenced `NetEye` resource.

Its specification contains at least:

- a reference to the `NetEye` resource;
- the target NetEye release;
- acknowledgements for completed external gates.

The target release is immutable. Gate acknowledgements are append-only after
the operator accepts them.

Only one non-terminal `NetEyeUpgrade` may exist for a `NetEye` installation.
The validating webhook rejects a second active upgrade, an unsupported forward
transition, or a downgrade.

The upgrade status uses standard conditions and reports at least:

- whether the request was accepted;
- whether work is progressing, waiting, degraded, or complete;
- the source and target NetEye releases;
- the NetEye generation used by the current attempt;
- currently eligible external gates and blocked components.

Automation treats a successful completion condition as the stable indication
that the product upgrade has finished. Human-readable messages are diagnostic
and are not a machine-readable workflow API.

### Version intent and initial installation

The complete `NetEye.spec` is user-owned installation configuration. It does
not contain the NetEye product version, and neither the main controller nor the
upgrade controller writes to it.

On first installation, no `NetEyeUpgrade` is required. The operator selects the
primary installation release embedded in that exact operator version. After a
successful installation, it reports the selected release in
`NetEye.status.currentVersion`.

For an upgrade, lifecycle automation first installs an operator that supports
the current and target releases. It then creates a `NetEyeUpgrade` whose
immutable `spec.targetVersion` is the only desired release for that
transaction. The upgrade controller validates the request and coordinates the
installation toward that target without copying it into `NetEye.spec`.

### External gates

External gates are stable identifiers in the upgrade graph from ADR-0005. They
represent required work but contain no Ansible commands or implementation
details.

When an external gate becomes eligible:

1. The operator publishes its identifier and state in
   `NetEyeUpgrade.status`.
2. Ansible observes the gate and performs the corresponding external work.
3. After successful completion, Ansible appends the gate identifier to the
   acknowledgements in `NetEyeUpgrade.spec`.
4. The operator validates the acknowledgement and unblocks its dependents.

Multiple independent gates may be eligible at the same time. Their graph
dependencies, rather than list order, determine when they can run.

An acknowledgement is a trusted assertion by authorized lifecycle automation.
The operator does not claim to verify state outside Kubernetes. RBAC must limit
who can create upgrades and acknowledge gates.

If an external action fails, Ansible does not acknowledge the gate. The
operator remains in a visible waiting state and does not continue dependent
work. Retrying the external action remains Ansible's responsibility.

External automation never writes the status subresource. The operator never
executes Ansible, SSH, or commands supplied through the upgrade resource.

### Specification lock

Acceptance activates the specification lock. The upgrade controller records
the current `NetEye` generation as the generation used by the attempt. While
the upgrade is progressing or waiting, the validating webhook rejects every
update to `NetEye.spec`.

Metadata and status updates do not change the transaction generation and are
not blocked by this rule.

The controller verifies the generation again before performing lifecycle work.
If admission was bypassed and the generation changes unexpectedly, it stops
progressing and reports a concurrent modification. It does not continue with a
mixed specification.

### Recovery amendment

A complete specification lock could make an upgrade impossible to repair when
the desired configuration itself caused the failure. A controlled exception is
therefore allowed only while the upgrade is degraded.

A privileged user may update `NetEye.spec` while the active `NetEyeUpgrade`
reports a degraded condition. The target remains unchanged in the immutable
`NetEyeUpgrade.spec.targetVersion`.

The change ends the failed attempt and starts a new attempt within the same
upgrade resource. The operator records the new NetEye generation, re-observes
the partial cluster state, and resumes automatically toward the same target.
Completed work is not rolled back, and required migrations are not skipped.

Normal spec updates remain rejected while the new attempt is progressing or
waiting. The exact authorization mechanism for privileged recovery is part of
the custom-resource API and RBAC design; it must be explicit and auditable.

### Completion and lifetime

`NetEye.status.currentVersion` changes to the target only after all required
component migrations, external gates, and readiness checks have completed.
The `NetEyeUpgrade` then reports successful completion and the spec lock is
released.

An active upgrade cannot be changed to another target or deleted to request a
rollback. Operator restarts and compatible operator patch updates resume the
same transaction from its durable resources and observed cluster state.

Completed upgrade resources may be retained as audit history or removed by
lifecycle automation. Their removal does not change the achieved NetEye
release.

NetEye upgrades do not support cancellation, rollback, or downgrade. Recovery
always moves the observed installation forward toward the authorized target.

## Alternatives considered

### Put the target version in `NetEye.spec`

This would keep configuration and the upgrade target in one resource, but it
would not provide an object for external gate acknowledgement, transaction
progress, or recovery attempts. Retaining the same target in both `NetEye` and
`NetEyeUpgrade` would also create two sources of desired state.

### Let Ansible write NetEye status

This would avoid a separate acknowledgement field. It was not chosen because
status is owned by the NetEye Operator and must describe observed state rather
than accept desired workflow input.

### Use annotations or ConfigMaps for external gates

These mechanisms could carry acknowledgements, but they would create an
implicit workflow API without typed validation, lifecycle, or discoverable
status.

### Execute external migrations from the operator

This would keep orchestration in one process. It was not chosen because it
would require the operator to contain external credentials, Ansible behavior,
and failure handling outside its Kubernetes ownership boundary.

### Allow normal configuration changes during an upgrade

This would reduce admission restrictions but could let different components
execute one upgrade against different NetEye generations.

### Keep the specification locked after every failure

This would preserve the original generation without exception. It was not
chosen because an invalid configuration could permanently prevent forward
recovery.

### Support cancellation or automatic rollback

This was not chosen because already-completed data migrations can be
irreversible and NetEye downgrades are not supported.

## Consequences

Upgrade authorization exists only in a dedicated, auditable resource and never
passes through `NetEye.spec`.

Ansible and the NetEye Operator remain separate executors. They coordinate
through typed gates whose state survives restarts without sharing process
state, credentials, or implementation code.

The operator needs another CRD, admission rules, status handling, and RBAC for
the upgrade protocol. Ansible needs logic to watch eligible gates and append
acknowledgements only after successful external work.

Release authors must coordinate stable gate identifiers with the corresponding
Ansible implementation. Changing a gate is an upgrade-protocol change and must
be reviewed with both owners.

The NetEye generation remains stable during every normal upgrade attempt. A
recovery amendment is visible as a new generation and a new attempt against
the same target.

An installed operator may receive compatible patches during an upgrade. The
new process must understand and resume the same source-to-target transition and
upgrade-resource state.

The operator never writes the primary resource specification. Users and GitOps
automation retain one clear ownership boundary for all `NetEye.spec` fields.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- [ADR-0005: Component Lifecycle and Dependency Orchestration](0005-component-lifecycle-and-dependency-orchestration.md)
- [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
