# ADR-0003: NetEye and Operator Version Model

- **Status:** Accepted
- **Date:** 2026-09-03

## Context

The NetEye product, the operator, the OLM bundle, and the NetEye components
have different versions. They change for different reasons.

OLM must install operator fixes and component patches automatically. An
automatic operator update must not start a NetEye product upgrade. Only the
administrator can start a product upgrade.

A NetEye release has a tested set of component images. This set must be
reproducible and easy to audit.

## Decision

### Version identities

- `NetEye.spec.version` is the NetEye release line in the format
  `major.minor`, for example `"4.51"`. The CRD rejects other formats, for
  example `4.51-sr1`.
- The operator has its own Semantic Versioning sequence. Its version does not
  come from the NetEye version.
- The OLM bundle has the same version as the operator image.
- Each operator minor line supports one NetEye release line. More than one
  operator minor line can support the same NetEye release line. For example,
  `0.1.x` supports `4.50` and `0.2.x` supports `4.51`.
- The constants `CurrentNetEyeVersion` and `PreviousNetEyeVersion` in the
  operator code give the supported lines.
- A new NetEye release line always gives a new operator minor version
  (ADR-0004). Other releases get a minor version if a merged pull request has
  the label `feature` or `major`. Else they get a patch version.

### Supported versions and upgrades

- The webhook accepts a new `NetEye` resource only with `CurrentNetEyeVersion`.
- The webhook accepts an update only from `CurrentNetEyeVersion` to
  `CurrentNetEyeVersion`, or from `PreviousNetEyeVersion` to
  `CurrentNetEyeVersion`. It rejects all other updates, also downgrades.
- If `spec.version` is `PreviousNetEyeVersion`, the controller sets the phase
  to `PendingUpgrades`. It does not reconcile the resource.
- If `spec.version` is a different unsupported value, the controller sets the
  phase to `Failed`. It does not change workloads. The controller does this
  check because a webhook can be bypassed.
- Only a change to `spec.version` starts a product upgrade. An operator update
  through OLM does not change `spec.version`.
- Downgrades are not supported.

### Component images

- The operator code contains the component image set for each supported
  release line. Each image has a digest.
- Users cannot set component images in the `NetEye` resource.
- The environment variables `RELATED_IMAGE_KEYCLOAK`,
  `RELATED_IMAGE_OTEL_COLLECTOR`, `RELATED_IMAGE_EDOT_GATEWAY`, and
  `RELATED_IMAGE_CA_BUNDLE` on the operator Deployment replace the images in
  the code. The ClusterServiceVersion sets these variables and lists the
  images in `relatedImages`.
- A component patch is an operator release, also if the Go code does not
  change. The release builds a new operator image and a new bundle.
- The status shows the selected image of each service in `resolvedImage`
  (ADR-0002).

### OLM channels

- The catalog has one stable channel and one nightly channel for each NetEye
  release line: `<line>-stable` and `<line>-nightly`, for example
  `4.51-stable`.
- A channel always stays on its NetEye release line.
- The default channel is the newest stable channel.
- A new stable entry must have a higher version than the channel head. A
  bundle in the catalog does not change.
- The nightly build runs each day at 02:00 UTC if `main` has new commits. It
  publishes the version `<version>-nightly-<commit>` to the nightly channel. A
  new nightly entry replaces the old head and skips all older entries.
- Channels move forward only. A new release fixes a defective release.
- The OLM `ClusterExtension` of the NetEye Operator is managed outside the
  operator. The operator does not change it.

## Alternatives considered

### Let an operator update start a NetEye upgrade

We did not select this option. Automatic OLM updates could then start product
upgrades. Operator delivery and upgrade approval need different controls.

### Put each service release in `spec.version`

We did not select this option. A value such as `4.51-SR1` makes a change to
the desired state necessary for each patch. Patches come through the channel
of the release line.

### Use the generic channels `stable` and `alpha`

We did not select this option. A generic channel can move to a different
NetEye release line. Then OLM can install an operator that does not support
the current installation.

### Give the operator the same version as NetEye

We did not select this option. It mixes the product lifecycle with the
operator lifecycle. Independent versions allow operator-only fixes.

### Allow component images in the `NetEye` resource

We did not select this option. It makes combinations that nobody tested.
Nightly bundles give a path to test new images.

## Consequences

- A NetEye upgrade is always an intentional change to `spec.version`.
- Automatic OLM updates can deliver fixes. They cannot start an upgrade.
- Before a NetEye upgrade, the automation must install an operator that
  supports the target line. Then it can change `spec.version`.
- An operator line does not manage the previous release line. It only waits
  for the upgrade.
- There is no migration logic. An upgrade applies the image set of the new
  release line.
- Each component change needs a new operator image and bundle.
- An operator that supports a new line cannot support release lines older
  than the previous line.

## References

- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0004: Automated Operator Release for New NetEye Lines](0004-automated-neteye-line-release.md)
