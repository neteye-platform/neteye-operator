# Architecture Decision Records

This directory contains the Architecture Decision Records (ADRs) for the
NetEye Operator and its OLM catalog.

ADRs capture durable architectural decisions, their context, the alternatives
considered, and their consequences. They define architectural rules. They do
not describe the current implementation or replace detailed API and component
specifications.

New ADRs must use the [ADR template](template.md).

## Record structure

Every ADR contains:

- **Status:** The current standing of the decision.
- **Date:** The record date in `YYYY-MM-DD` format.
- **Context:** The problem, constraints, and relevant background.
- **Decision:** The selected approach and the rules it establishes.
- **Alternatives considered:** Relevant options and why they were not chosen.
- **Consequences:** Expected benefits, costs, risks, and follow-up work.
- **References:** Related ADRs, specifications, or external material.

Authors and approvers are recorded by Git history and pull-request reviews.

## How to use the ADRs

Use this README as the entry point. Do not read every ADR for every task.

1. Find the area affected by the task in the index below.
2. Read the accepted ADRs whose scope matches that area.
3. Follow links to other ADRs only when the selected ADR depends on them or
   more decision history is needed.
4. Read proposed ADRs only when reviewing them, working on the decision they
   describe, or when the task explicitly refers to them.
5. Read superseded or deprecated ADRs only when historical rationale is
   required.

If an implementation change conflicts with an accepted ADR, the ADR remains
authoritative. Change the implementation or propose a new ADR that replaces
the old decision.

## Statuses

The following statuses are used:

- **Proposed:** Under review. It does not yet guide the architecture.
- **Accepted:** Approved and active. It guides the architecture and
  implementation.
- **Superseded:** Replaced by a newer ADR and retained as decision history.
- **Deprecated:** No longer applicable and without a replacement ADR.

## Lifecycle and numbering

- New ADRs start as `Proposed`.
- The approving pull request changes the status to `Accepted` before merge.
- Every ADR is added to the index below.
- Use the next available four-digit number. Never reuse or renumber an ADR.
- A material change to an accepted decision requires a new ADR.
- A replacement ADR links to the old ADR and marks it as `Superseded`.
- Small clarifications may update an accepted ADR when they do not change its
  decision.
- The exact implementation may change without a new ADR as long as it remains
  consistent with the accepted decisions.

## Index

| ADR | Decision | Scope | Status |
| --- | --- | --- | --- |
| [ADR-0001](0001-neteye-resource-scope-and-ownership.md) | NetEye Resource Scope and Ownership | Singleton `NetEye`, tenants, resource ownership, and deletion policy | Proposed |
| [ADR-0002](0002-reconciliation-and-resource-application.md) | Reconciliation and Resource Application | Server-Side Apply, field ownership, delegated resources, readiness, and component status | Proposed |
| [ADR-0003](0003-neteye-and-operator-version-model.md) | NetEye and Operator Version Model | Product and operator versions, OLM channels, upgrade authorization, and resolved images | Proposed |
| [ADR-0004](0004-delegated-operator-lifecycle-and-ownership.md) | Delegated Operator Lifecycle and Ownership | Dependent catalogs and extensions, tested versions, readiness, drift, and deletion | Proposed |
| [ADR-0005](0005-component-lifecycle-and-dependency-orchestration.md) | Component Lifecycle and Dependency Orchestration | Installation and upgrade graphs, scheduling, migrations, failure recovery, and deletion order | Proposed |
| [ADR-0006](0006-neteye-upgrade-coordination.md) | NetEye Upgrade Coordination | Upgrade authorization, Ansible gates, spec locking, recovery amendments, and completion | Proposed |
| [ADR-0007](0007-tenant-namespace-and-isolation-model.md) | Tenant Namespace and Isolation Model | Tenant placement, identity, namespace lifecycle, isolation, component scope, and deletion | Proposed |
| [ADR-0008](0008-custom-resource-api-evolution-and-compatibility.md) | Custom Resource API Evolution and Compatibility | API maturity, compatible schema changes, validation, conversion, storage versions, and deprecation | Proposed |
| [ADR-0009](0009-secrets-and-credentials-lifecycle.md) | Secrets and Credentials Lifecycle | Credential ownership, Secret references, generation, rotation, tenant scope, retention, and recovery | Proposed |
