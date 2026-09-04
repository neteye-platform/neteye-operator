# Coding Agent Guide

## Sources of truth

- Inspect the repository before assuming that an implementation, path, or
  command exists.
- Start with the [ADR index](docs/adr/README.md) when a task can affect the
  architecture.
- Use the scope column and loading instructions in the index to identify the
  relevant ADRs. Do not load the complete ADR set by default.
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

## Before finishing

- Confirm the change is consistent with all relevant accepted ADRs.
- Run the checks relevant to the changed files.
- Inspect the final diff and report any checks that were not run.
