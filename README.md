# NetEye Operator

A Kubernetes operator that deploys and manages **NetEye** — an IT systems
and network monitoring platform — on Kubernetes.

> **Status: early development.** The API group `neteye.cloud/v1alpha1` is
> experimental.

## Overview

The operator reconciles a namespaced `NetEye` custom resource (short name
`ne`) that describes a NetEye deployment. From that single resource it
provisions and continuously reconciles the shared platform services the
deployment depends on.

Reconciliation is version-aware — a validating webhook guards the declared
product `version` and its allowed upgrade transitions — and reports progress
through the resource `status`.

### PermissionSync

The PermissionSync component synchronizes a user's permissions with a selected
target when that user logs in. It is part of every NetEye deployment and
cannot be switched off, so `spec.permissionSync` is required. The operator
renders PermissionSync's single configuration document into a Secret it owns,
publishes the service over HTTPS on the shared Gateway as
`permissionsync.neteyelocal`, and provisions the Keycloak side of the
contract: one `permissionsync:<target>` client scope per declared target, the
confidential `login-sync` client with an operator-generated secret, the
login-sync authenticator configuration, and a browser flow running it, bound
only to the Keycloak clients that are targets.

A logical target must be named after the Keycloak login client it serves,
because the authenticator derives the target from that client's `clientId`.
The GLPI credentials and the trusted-CA Secret stay user-managed and must
exist in the shared namespace before the component can become ready.

Every deployment bound — request deadline, inbound admission limit,
synchronization capacity, shutdown grace, verification cache policy, clock
skew, and operation timeout — is a field with the operator's own value as its
default, and endpoints the platform itself provides default to it: the
Permission Provider to
`https://httpd.neteyelocal/neteye/api/v2/permission-provider/desired-state`
and GLPI to `https://glpi.neteyelocal/apirest.php`. The
relations between the bounds are validated at admission, because
PermissionSync aborts startup on an impossible combination rather than
degrading one component.

The deployment has to resolve `permissionsync.neteyelocal` to the Gateway, as
it does for the identity hostname. Because the authenticator is fail-closed, a
PermissionSync outage refuses logins on target clients only. See
[ADR-0005](docs/adr/0005-permissionsync-component-and-caller-provisioning.md)
and
[ADR-0006](docs/adr/0006-login-sync-wiring-and-permissionsync-exposure.md)
for the full boundary.

## Documentation and support

This repository contains the operator only. NetEye itself is a commercial
product and requires a valid license to run. For the product, its
documentation, licensing, and support, see Würth IT Italy:

- Documentation: <https://neteye.guide/current/>
- Product page: <https://www.wuerth-it.it/en/it-system-management/>
- Email: <info.italy@wuerth-it.com>

## Install

The operator is packaged for OLM. To install it, first create a `ClusterCatalog`
that references the `neteye-operator-catalog:latest` image from the separate
catalog repository (`neteye-platform/neteye-operator-catalog`), the
`neteye-operator` `ClusterExtension`, and required namespaces. An example
`install.yaml` is provided in `examples/` for convenience. Apply it with:

```sh
kubectl apply -f ./examples/install.yaml
```

Once the operator is running, create a `NetEye` resource to
describe your deployment — see the API types in `src/api/v1alpha1/` and the
generated CRD under `src/bundle/manifests/`.

## Development

The Go module lives in `src/` ([controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)).
Run make targets from that directory, e.g. `make -C src build`:

- `build` — build the manager binary
- `generate` / `manifests` — regenerate deepcopy code and CRDs
  (controller-gen)
- `fmt` / `vet` / `lint` — format and static analysis (golangci-lint)
- `test` — run unit tests
- `docker-build` — build the container image
- `kustomize-manifests` — regenerate the Operator SDK CSV base and render the
  release manifest source
- `bundle` — generate `src/bundle/` from `config/manifests/` and the API CRD
- `bundle-validate` — validate the generated OLM bundle
- `bundle-build` — build the bundle image with the maintained
  `src/Dockerfile.bundle`

### OLM bundle generation

`src/bundle/` is generated release output; do not edit its CSV, CRD, or
metadata by hand. The maintainable inputs are the API types and RBAC markers,
the intentional legacy RBAC additions, and the CSV patches and sample in
`src/config/manifests/` and `src/config/samples/`.

Generate it from the repository root:

```sh
make bundle
make bundle-validate

# Example release override
make bundle VERSION=0.1.1-alpha.1 \
  IMG=ghcr.io/neteye-platform/neteye-operator:0.1.1-alpha.1 \
  PACKAGE_NAME=neteye-operator
make bundle-build BUNDLE_IMG=ghcr.io/neteye-platform/neteye-operator-bundle:0.1.1-alpha.1
```

The Makefile pins and downloads Operator SDK `v1.42.3` and kustomize `v5.7.1`
to `src/bin/`. `operator-sdk generate kustomize manifests` refreshes the source
CSV scaffold only. `bundle` creates an untracked temporary release overlay,
renders it directly with kustomize into `bundle/manifests/`, and renders bundle
metadata from `config/bundle/annotations.yaml.tmpl`. The release overlay does
not mutate tracked `config/manifests/`. `verify-generated` regenerates and
validates the code and bundle output. The bundle remains AllNamespaces-only
and preserves the validating webhook at `/validate-neteye-cloud-v1alpha1-neteye`.
Bundle channel membership is defined only by the file-based catalog in the
separate `neteye-operator-catalog` repository. OLM requires bundle-format
channel metadata, so every bundle declares the `preview` channel metadata.
This is not a release input and does not assign catalog membership.

### OLM catalog

The file-based OLM catalog and its catalog image build are maintained in the
separate `neteye-operator-catalog` repository. This repository builds the
operator and its OLM bundle image; release automation publishes that bundle
before the catalog repository references it. The catalog repository publishes
its `:latest` image from `main`.

### Releases

Releases are prepared in a pull request and published from an annotated SemVer
tag. The tagged commit must be reachable from `main` or from the matching
`release/<major>.<minor>` branch derived from the tag. For example, `v1.2.4`
may be published from `main` or `release/1.2`, but not from another release
train. Update `VERSION` in `src/Makefile` then regenerate the bundle:

```sh
make bundle bundle-validate
./hack/check-version-consistency.sh
```

Use the `alpha` catalog channel for prerelease versions and `stable` for GA
versions. The release workflow rejects tags that do not exactly match the
checked-in version or do not come from an allowed release source.

After validation, the workflow builds and publishes the operator and bundle
images with SBOM and provenance attestations. It passes the release version to
both image builds and records the resulting bundle digest. It then opens a
version-specific pull request in `neteye-platform/neteye-operator-catalog` that
references the immutable bundle digest. Rerunning the workflow updates or
reports the same pull request instead of opening a duplicate.

Cross-repository pull requests use a GitHub App installed on
`neteye-platform/neteye-operator-catalog` with repository `Contents: Read and
write` and `Pull requests: Read and write`. Configure its App ID as the
organization variable `NETEYE_APP_ID` and its private key as the organization
secret `NETEYE_APP_PRIVATE_KEY`, granting this repository access to both. The
source repository's standard `GITHUB_TOKEN` cannot write to the catalog
repository.

Protect the `v*` tag namespace so only release maintainers can create tags and
tags cannot be updated or deleted. Protect `main` and `release/*` in this
repository as release sources. In the catalog repository, protect `main` from
direct App pushes and reserve `release/neteye-operator-v*` branches for the
App. The workflow itself requires an annotated tag and creates catalog changes
from a fresh trusted `main`; repository rules establish who may trigger that
workflow and prevent branch-name preemption.

Once the catalog pull request passes validation and is merged, the catalog
repository publishes `neteye-operator-catalog:latest`. Never move a release tag
or overwrite an existing catalog release with another digest; publish a new
SemVer version for corrections.

Pre-commit hooks are configured in `.pre-commit-config.yaml` (run via
[`prek`](https://github.com/j178/prek) or `pre-commit`). Set `LOG_LEVEL`
(`debug`, `info`, `warn`, `error`, or `v<n>`) to control log verbosity.

## Repository layout

```text
src/
  api/*/             # NetEye CRD types + validating webhook
  controllers/       # NetEye reconcilers
  internal/
    resources/       # generic apply / ownership / readiness helpers
    ...              # other internal packages / operator components
  bundle/            # OLM bundle built from this operator's API
examples/            # example of operator installation
```

## License

Dual-licensed under either the [Apache License 2.0](LICENSE-APACHE) or the
[MIT license](LICENSE-MIT), at your option.
Copyright © Würth IT Italy S.r.l.
