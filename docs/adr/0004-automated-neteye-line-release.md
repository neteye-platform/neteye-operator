# ADR-0004: Automated Operator Release for New NetEye Lines

- **Status:** Accepted
- **Date:** 2026-10-02

## Context

The Tekton pipeline `create-neteye-release-infrastructure` prepares a new
NetEye release line. Before installations can use the new line, the operator
must support it (ADR-0003). This needs a new operator minor version and a new
stable channel in the catalog.

## Decision

- The Tekton pipeline sends the `repository_dispatch` event `neteye-release`
  to `neteye-operator`. The payload contains `neteye_version` and
  `source_pipeline_run`. The workflow uses `source_pipeline_run` only in the
  run name.
- The Release workflow gets a token for the operator GitHub App. The token
  has Contents write, Pull requests write, and Checks read permissions.
- The workflow opens a pull request on the branch
  `fix/bump-current-neteye-version-to-<minor>`. If this pull request is open,
  the workflow uses it again.
- In the pull request, `hack/update_neteye_support.py` moves
  `CurrentNetEyeVersion` to `PreviousNetEyeVersion` and sets the new line as
  `CurrentNetEyeVersion`. It also updates the samples. Then `make bundle`
  makes the bundle and CRD files again.
- The new line uses the same component images as the previous line.
- The workflow waits until all check runs of the pull request are successful.
  Then it merges the pull request with a squash merge. If a check fails, the
  workflow stops.
- Then the workflow creates the next operator minor version, the tag, and the
  release. It publishes the bundle to the channel `<line>-stable`.
- The new line goes directly to the stable channel. There is no experimental
  channel.
- The workflow does not change `NetEye` resources. It does not start a
  product upgrade.

## Alternatives considered

### Release an operator patch for the new line

We did not select this option. Each operator minor line supports one NetEye
release line (ADR-0003).

### Publish the new line first to an experimental channel

We did not select this option. A line that this pipeline prepares is ready for
the stable channel when the NetEye release preparation is complete.

## Consequences

- The GitHub App must be installed on `neteye-operator`. The ruleset must let
  the App merge after the checks are successful.
- The catalog update uses a different token and merges with admin rights.
- The component images of the new line come from the previous line. A
  different pull request must update them if the new line needs new images.
- If the workflow runs again, it uses the same pull request. If the support is
  already present, it does not make a pull request. Each successful run makes
  a new operator tag.

## References

- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- `si-rd-infra/apps/pipelines/rd-pipelines/neteye-minor-release/create-neteye-release-infrastructure.yaml`
