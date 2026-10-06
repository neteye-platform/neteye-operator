# ADR-0004: Automated Operator Release for New NetEye Lines

- **Status:** Accepted
- **Date:** 2026-10-02

## Context

The `create-neteye-release-infrastructure` Tekton pipeline prepares a public
NetEye release line. The NetEye version supplied to that pipeline may include a
sprint suffix, while `NetEye.spec.version` and the operator catalog channel
identify only the `major.minor` release line.

The operator must support the new line and the immediately previous line
before NetEye installations can use it. Publishing that support requires an
operator minor release, a matching versioned stable catalog channel, and a
maintenance branch for the previous operator line.

## Decision

The release-infrastructure pipeline normalizes the NetEye version to
`major.minor` and dispatches the operator Release workflow with the target line
and source PipelineRun name. GitHub Actions authenticates as the operator GitHub
App, opens or reuses a pull request against `neteye-operator`, and updates
`CurrentNetEyeVersion`, `PreviousNetEyeVersion`, examples, and generated
bundle/CRD artifacts. The new line initially carries forward the existing
component image set. GitHub Actions merges the pull request only after its
status checks pass.

The Release workflow then always creates the next operator minor version for
this event and publishes the bundle to the `<neteye-version>-stable` catalog
channel. It does not authorize or perform a NetEye product upgrade.

Tekton captures the previous stable operator tag before dispatch and waits for
the Release workflow to succeed. It then creates
`release/<previous-operator-major.minor>` at that captured tag. It does not
move an existing maintenance branch.

This automated path publishes the new NetEye line directly to its stable
channel. It replaces the experimental-first promotion sequence in ADR-0003 for
lines released through `create-neteye-release-infrastructure`; other channel
and upgrade-authorization rules in ADR-0003 remain in force.

## Alternatives considered

### Keep experimental-first publication

This was not selected for release lines prepared by the release-infrastructure
pipeline. The requested process considers those lines ready for the stable
operator channel when NetEye release preparation completes.

### Release an operator patch for the new line

This was not selected because each operator minor line has an explicit NetEye
support window and forward-upgrade edge.

### Create the previous-line branch from current main

This was not selected because the maintenance branch must preserve the source
of the last operator release for that line. It is therefore created from the
previous stable tag.

## Consequences

- The GitHub App must be installed on `neteye-operator` with Contents write,
  Pull requests write, Checks read, and Actions read permissions. Its ruleset
  access must allow the automated merge after checks pass.
- Tekton needs Contents write and Actions read to create the maintenance branch
  and wait for the Release workflow. App credentials are supplied to the
  `rd-pipelines` namespace through ExternalSecrets.
- The carried-forward component images are temporary release data and must be
  replaced when the new NetEye line specifies different component images.
- Release retries are idempotent for support updates and maintenance-branch
  creation; an existing branch at a different commit causes the task to fail.

## References

- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- `si-rd-infra/apps/pipelines/rd-pipelines/neteye-minor-release/create-neteye-release-infrastructure.yaml`
