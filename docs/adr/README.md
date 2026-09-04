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

1. [ADR-0001](0001-neteye-resource-scope-and-ownership.md): NetEye Resource
   Scope and Ownership (Proposed)
2. [ADR-0002](0002-reconciliation-and-resource-application.md): Reconciliation
   and Resource Application (Proposed)
3. [ADR-0003](0003-neteye-and-operator-version-model.md): NetEye and Operator
   Version Model (Proposed)
