# ADR-0003: NetEye and Operator Version Model

- **Status:** Proposed
- **Date:** 2026-09-03

## Context

The NetEye product, the NetEye Operator, its OLM bundle, and the individual
NetEye components have separate versions. These versions change for different
reasons and must not be treated as one value.

OLM must be able to install operator fixes and compatible component patches
automatically. At the same time, an automatic operator update must not start a
NetEye product upgrade. Product upgrades can contain migrations and require an
explicit action from the administrator.

NetEye has one stable release line and one in-development release line. For
example, after NetEye 4.50 is released, 4.51 is available as an experimental
line. Experimental installations receive changes continuously and move
forward until that line becomes stable.

The selected component versions are part of a tested NetEye release. They must
be reproducible and easy to audit on a running installation.

## Decision

### Version identities

The `NetEye` specification contains user-owned installation configuration. It
does not contain the NetEye product version, and the operator does not write to
it.

On first installation, the operator selects the primary installation release
embedded in that exact operator version. No `NetEyeUpgrade` is required for
this initial selection.

For later product upgrades, `NetEyeUpgrade.spec.targetVersion` is the only
desired NetEye release. It identifies a release line, such as `4.51`, rather
than an individual service release or component patch. The target is immutable
for the lifetime of the upgrade.

Compatible fixes can be delivered within a release line without changing the
`NetEye` resource or creating another product upgrade.

The operator has its own Semantic Versioning sequence. Its version is not
derived from the NetEye version. The OLM bundle uses the same version as the
operator image and pins that image by digest.

Each operator minor line supports one NetEye release line. The mapping is
explicit release metadata and must not be inferred from the version numbers.
For example, a valid mapping could be:

| Operator line | NetEye line |
| ------------- | ----------- |
| `1.4.x`       | `4.50`      |
| `1.5.x`       | `4.51`      |
| `1.6.x`       | `4.52`      |

Operator patch releases deliver operator fixes and compatible component
patches. A new operator minor line introduces support for the next NetEye
release line.

Users cannot select individual component image versions in the `NetEye`
custom resource. The tested component set selected by the operator is
authoritative.

### Operator release lines

Development of a new NetEye release is isolated from the automatic patch
stream of the current stable release. The project maintains:

- a main development line for the next experimental NetEye release;
- one maintenance line for the current stable NetEye release.

Applicable fixes are forward-ported from the maintenance line to the
development line.

An operator line supports:

- its target NetEye release during normal operation;
- the immediately previous NetEye release as an upgrade source;
- the defined forward upgrade from that previous release to its target.

The previous release must remain fully manageable while the new operator is
installed and the product upgrade is waiting to start. Older releases are not
supported by that operator line. This bounds each operator line to two NetEye
release descriptors and one forward transition.

Component reconcilers should consume resolved release data and remain
independent of product versions. Version-specific behavior belongs in explicit
migrations. It must not be spread through the reconcilers as unrelated version
checks.

### OLM channels

OLM channels are specific to a NetEye release line and its maturity. For
example:

- `stable-4.50`
- `experimental-4.51`

A channel is never changed to represent a different NetEye release line.
Updates within a channel may contain compatible operator fixes and compatible
component patches for that line.

When an experimental line becomes generally available:

- the tested bundle is published in the matching stable channel;
- the experimental channel for that release is frozen;
- the next experimental channel is created;
- existing experimental installations can switch to the stable channel
  without starting a product upgrade.

For example, `experimental-4.51` does not become 4.52. It is frozen when
`stable-4.51` is published, and development continues in
`experimental-4.52`.

Experimental channels move forward only. A broken experimental update is
fixed by a newer update; downgrade is not a recovery mechanism.

The OLM `ClusterExtension`, including its channel and version range, is owned
by installation or lifecycle automation outside the NetEye Operator. The
operator must not change its own `ClusterExtension` or trigger its own update.

### Upgrade authorization

Creating a valid `NetEyeUpgrade` resource is the only action that authorizes a
NetEye product release upgrade. Its desired target is explicit and immutable
for the lifetime of that upgrade.

Updating the OLM channel or version range only installs an operator that is
capable of managing the requested release. It does not authorize a product
upgrade and must not run cross-release migrations.

A product upgrade follows this order:

1. Installation automation selects an operator that supports both the current
   NetEye release and the target release.
2. It waits until OLM has installed that operator.
3. Authorized lifecycle automation creates a `NetEyeUpgrade` for the target
   release.
4. The upgrade controller validates and accepts the transition.
5. The upgrade controller coordinates the approved component and external
   migrations toward the immutable target.
6. After successful completion, the operator reports the target in
   `NetEye.status.currentVersion`.

The safe intermediate state is a newer operator continuing to manage the
current NetEye release. Installing that operator and creating the upgrade
resource are separate, ordered actions.

The validating webhook rejects a `NetEyeUpgrade` when:

- the installed operator does not support the requested target;
- the transition is not present in the explicit forward-upgrade graph;
- the request is a downgrade;
- another upgrade is already active for the installation.

The controller repeats these checks because admission webhooks can be bypassed
or temporarily unavailable. If it observes an invalid upgrade request or
transition, it does not change workloads and reports the problem in status.

Downgrades are not supported.

### Release manifests

The operator image contains declarative, version-controlled release manifests.
For example:

```text
releases/
├── 4.50.yaml
└── 4.51.yaml
```

The files are embedded in the operator binary or image. They are not runtime
configuration and are not delivered through a mutable ConfigMap.

A release manifest contains the information required to resolve a NetEye
release, including:

- the exact, digest-pinned image for every component;
- the migrations required by each supported forward transition;
- any compatibility data required by the reconcilers.

A component-only patch is released as an operator patch even when the Go code
does not change. The release manifest is updated, then a new operator image and
matching OLM bundle are built and published in the same versioned channel.
Rebuilding the operator image for this purpose is acceptable.

CI validates at least that:

- release manifests conform to their schema;
- all component images are pinned by digest;
- every supported NetEye release has a manifest;
- upgrade edges move forward and reference existing releases;
- every referenced migration exists.

The operator must not become ready with invalid embedded release data.

Testing an unreleased component set uses an experimental or purpose-built
operator bundle. Production custom resources do not expose component image
overrides.

### Reported versions

`status.currentVersion` reports the NetEye product release that the operator
has successfully applied. During an upgrade it remains at the source release
until the upgrade completes. The desired target remains in
`NetEyeUpgrade.spec.targetVersion`.

The standard conditions report whether an upgrade is progressing, complete,
or degraded. `status.currentVersion` changes only after the requirements for
the new release and its migrations have completed successfully.

As defined in ADR-0002, each component status also reports its complete
`resolvedImages` set. Every entry has a stable logical name and an exact,
digest-pinned OCI image reference. This makes the component artifacts selected
for a running installation visible without inspecting its workloads or release
manifests.

The operator image digest, its embedded release manifests, the NetEye status,
and the component `resolvedImages` together provide an auditable description
of the deployed software set.

Status is observed state, not the operator's only record of release identity.
If status is missing or stale, the operator must reconstruct it from the
managed cluster state and durable operator-owned release identity.

## Alternatives considered

### Let an operator update authorize a NetEye upgrade

This was not chosen because automatic OLM updates would then be able to start
product migrations. Operator delivery and product-upgrade approval need
separate controls.

### Put the desired version in `NetEye.spec`

This is common when changing the primary resource directly authorizes an
application upgrade. It was not chosen because `NetEyeUpgrade` already contains
the desired target and provides transaction progress, external gates, and
recovery. Keeping the target in both resources would create two sources of
desired state and would require unusual controller ownership of a user-facing
specification.

### Represent every service release as a product-upgrade target

Values such as `4.51-SR1` would require a desired-state change for compatible
patches. This was not chosen because patches are delivered through the selected
OLM channel within a NetEye release line.

### Use generic `stable` and `experimental` channels

A generic channel would eventually move to a different NetEye release and
could install an operator that no longer manages the current installation.
Versioned channels keep automatic updates inside an explicit compatibility
boundary.

### Use one operator update stream for several NetEye releases

This would expose stable installations to development for the next release and
would make the runtime operator retain growing historical behavior. Separate
minor lines keep development isolated and the supported version window
bounded.

### Give the operator the same version as NetEye

This would mix the product lifecycle with the operator implementation
lifecycle. Independent versions allow operator-only fixes while the explicit
mapping preserves compatibility.

### Load release data from a ConfigMap or separate runtime artifact

This could release component patches without rebuilding the operator image.
It was not chosen because it adds another mutable input and another
compatibility boundary. Embedding the manifests makes one operator image digest
identify both the reconciler and its certified component set.

### Allow component image overrides in the NetEye resource

This would make development testing convenient, but it would also create
untested combinations that the operator could not support reliably. Dedicated
bundles and experimental channels provide the required testing path.

## Consequences

A NetEye release upgrade is always visible as an intentional
`NetEyeUpgrade` resource. Automatic OLM updates can safely deliver compatible
fixes without granting permission for a cross-release upgrade.

Installation automation must stage a compatible operator before creating the
upgrade resource. It must wait for each step instead of assuming that operator
delivery and product upgrade are atomic.

The complete `NetEye.spec` remains user-owned. Product-upgrade intent exists
only in `NetEyeUpgrade.spec.targetVersion`, so clients do not need to reconcile
two desired version fields.

The catalog must publish separate, versioned stable and experimental channels
and preserve their release boundaries.

The project must maintain one stable branch and one development line, including
the required forward-porting of fixes. In return, production patches are
isolated from development of the next NetEye release.

Each component change requires a new operator image and OLM bundle. This adds a
build, but makes the resulting software set immutable and reproducible.

The operator needs a validating webhook, matching controller-side validation,
an explicit forward-upgrade graph, and explicit migrations. Downgrade logic is
not implemented.

Each new operator line carries only its target release and the immediately
previous upgrade source. Support for older releases is removed as the release
window advances, which prevents migration complexity from growing without a
bound.

Support teams and automation can identify both the achieved NetEye release and
the exact component images through the status API.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0005: Component Lifecycle and Dependency Orchestration](0005-component-lifecycle-and-dependency-orchestration.md)
- [ADR-0006: NetEye Upgrade Coordination](0006-neteye-upgrade-coordination.md)
- [OLM v1: Version ranges](https://operator-framework.github.io/operator-controller/concepts/version-ranges/)
- [OLM v1: Upgrade support](https://operator-framework.github.io/operator-controller/concepts/upgrade-support/)
