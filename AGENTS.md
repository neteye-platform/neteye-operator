# Coding Agent Guide

## Sources of truth

- Inspect the repository before assuming that an implementation, path, or
  command exists.
- Start with the [ADR index](docs/adr/README.md) when a task can affect the
  architecture.
- Accepted ADRs are authoritative. Read the relevant accepted ADRs before
  making architecture-facing changes.
- Proposed ADRs are under review and do not yet constrain implementation.
- When implementation conflicts with an accepted ADR, update the
  implementation or propose a new ADR. Do not rewrite the ADR merely to match
  existing code.

## Working in this repository

- Keep changes focused on the requested task.
- Do not silently resolve a decision that an ADR explicitly defers.
- Follow the public custom-resource and status contracts defined by accepted
  ADRs.
- Use the repository's documented Make targets for generation, formatting,
  linting, testing, and bundle validation.
- Do not edit generated OLM bundle files by hand.
- Do not commit credentials, tokens, private keys, or other secrets.

## ADRs

- New ADRs must use the [ADR template](docs/adr/template.md).
- Add every ADR to the [ADR index](docs/adr/README.md).
- Use the next available number and never renumber an existing ADR.
- Create an ADR for a durable architectural decision, not for a small
  implementation detail.
- A change that replaces an accepted decision requires a new ADR and marks the
  previous ADR as `Superseded`.
- Keep ADR language concise, prescriptive, and independent of temporary
  implementation details.

## Before finishing

- Confirm the change is consistent with all relevant accepted ADRs.
- Run the checks relevant to the changed files.
- Inspect the final diff and report any checks that were not run.
