# Architecture Decision Records

This directory contains the Architecture Decision Records (ADRs) for the
NetEye Operator and its OLM catalog.

An ADR records one architectural decision, its context, the alternatives, and
the consequences. An ADR gives rules. It does not replace the API or component
specifications.

Use the [ADR template](template.md) for new ADRs.

## Record structure

Each ADR has these sections:

- **Status:** The current status of the decision.
- **Date:** The date of the record, in `YYYY-MM-DD` format.
- **Context:** The problem, the constraints, and the background.
- **Decision:** The selected solution and its rules.
- **Alternatives considered:** The other options and why we did not select
  them.
- **Consequences:** The benefits, costs, and risks.
- **References:** Related ADRs and other documents.

Git history and pull-request reviews record the authors and the approvers.

## How to use the ADRs

Use this README as the start point. Do not read all ADRs for each task.

1. Find the area of the task in the index.
2. Read the accepted ADRs for that area.
3. Read a linked ADR only if the selected ADR refers to it.
4. Read superseded or deprecated ADRs only if you need the history of a
   decision.

If a change to the implementation conflicts with an accepted ADR, the ADR
applies. Change the implementation, or write a new ADR that replaces the old
decision.

## Statuses

- **Accepted:** The decision applies to the architecture and the
  implementation.
- **Superseded:** A newer ADR replaces the decision. We keep the ADR as
  history.
- **Deprecated:** The decision does not apply and no ADR replaces it.

## Lifecycle and numbering

- A new ADR has the status `Accepted` when its pull request merges. Review
  occurs in the pull request.
- Add each ADR to the index.
- Use the next free four-digit number. Do not use a number again. Do not
  change the number of an ADR.
- A material change to an accepted decision needs a new ADR. The new ADR links
  to the old ADR. Set the old ADR to `Superseded`.
- You can update an accepted ADR to make it clearer if the decision does not
  change.
- The implementation can change without a new ADR if it agrees with the
  accepted decisions.

## Index

| ADR | Decision | Scope | Status |
| --- | --- | --- | --- |
| [ADR-0001](0001-neteye-resource-scope-and-ownership.md) | NetEye Resource Scope and Ownership | Singleton `NetEye`, owned, delegated, and external resources, deletion | Accepted |
| [ADR-0002](0002-reconciliation-and-resource-application.md) | Reconciliation and Resource Application | Create and update of resources, ownership checks, readiness, status contract | Accepted |
| [ADR-0003](0003-neteye-and-operator-version-model.md) | NetEye and Operator Version Model | Product and operator versions, supported upgrades, component images, OLM channels | Accepted |
| [ADR-0004](0004-automated-neteye-line-release.md) | Automated Operator Release for New NetEye Lines | Release workflow for a new NetEye release line | Accepted |
| [ADR-0005](0005-keycloak-configuration-resources.md) | Keycloak Configuration Resources | `KeycloakRealm`, `KeycloakClient`, `KeycloakUser`, and `KeycloakAuthFlow` | Accepted |
