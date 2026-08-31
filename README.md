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

## Documentation and support

This repository contains the operator only. NetEye itself is a commercial
product and requires a valid license to run. For the product, its
documentation, licensing, and support, see Würth IT Italy:

- Documentation: <https://neteye.guide/current/>
- Product page: <https://www.wuerth-it.it/en/it-system-management/>
- Email: <info.italy@wuerth-it.com>

## Install

The operator is packaged for OLM. The Helm chart under `charts/` registers
the operator's `ClusterCatalog` and `ClusterExtension` (and any dependent
operator catalogs):

```sh
helm install neteye-operator ./charts \
  --namespace neteye-system --create-namespace
```

Set the catalog image and channel in `charts/values.yaml` before
installing. Once the operator is running, create a `NetEye` resource to
describe your deployment — see the API types in `src/api/v1alpha1/` and the
generated CRD under `src/bundle/manifests/`.

The chart creates `keycloak-system` for the Keycloak Operator and
`neteye-tenant-shared` for the shared Keycloak workload; pre-existing
namespaces are left untouched. The Keycloak instance, its TLS Certificate,
HTTPRoute, and NetworkPolicy always run in `neteye-tenant-shared`, regardless
of the `NetEye` resource namespace. Its database credential Secrets and
cert-manager Issuer must therefore exist in `neteye-tenant-shared`.

## Development

The Go module lives in `src/` (Go 1.26,
[controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)).
Run make targets from that directory, e.g. `make -C src build`:

- `build` — build the manager binary
- `generate` / `manifests` — regenerate deepcopy code and CRDs
  (controller-gen)
- `fmt` / `vet` / `lint` — format and static analysis (golangci-lint)
- `test` — run unit tests
- `docker-build` — build the container image

### Local OLM v1 workflow

For local development, publish unique prerelease images to a registry reachable
by both the VM and Kubernetes. The default is `172.19.69.254:5000`:

```sh
make -C src dev-mirror-keycloak  # optional; run when the registry lacks Keycloak
make -C src dev-publish
KUBECONFIG=/path/to/dev-vm.kubeconfig make -C src dev-install
```

`dev-publish` creates a temporary, self-contained `src/bin/dev/context` and
does not modify the committed bundle or catalog. It records the rendered
version and catalog image there, and the later `dev-install` reads that state
so it installs the exact artifact just published. It uses a timestamp and short
Git SHA prerelease by default. Supply another unique plain SemVer prerelease
with `DEV_VERSION=0.1.1-dev.20260819.abc1234`. Override the registry with
`REGISTRY=172.19.69.254:5000`, and set `DEV_IMAGE_PULL_SECRET=name` when the
rendered CSV needs an image pull secret. Keycloak is not mirrored automatically:
run `dev-mirror-keycloak` separately or provide `KEYCLOAK_IMG`.

An HTTP registry is suitable only for an isolated, trusted lab network with no
registry credentials; configure it as insecure for Docker, Kubernetes node
image pulls, and the catalogd/operator-controller runtime. Use TLS for every
other environment. The registry must be reachable from the VM and Kubernetes
nodes. This workflow requires OLM v1 and
an existing `operatorhubio` `ClusterCatalog` (checked by `dev-install`). It does
not use classic OLM `CatalogSource`/`Subscription` commands.

`dev-install` passes the rendered version as an exact OLM constraint
(`=<version>`), rather than selecting a newly generated timestamp version. It
requires `KUBECONFIG` and passes it explicitly to `kubectl` and Helm. Because
the temporary development catalog has only the current bundle, `dev-install`
uses OLM's `SelfCertified` upgrade policy. Normal chart installs retain
`CatalogProvided`; CRD upgrade-safety preflight remains `Strict` in both cases.

Pre-commit hooks are configured in `.pre-commit-config.yaml` (run via
[`prek`](https://github.com/j178/prek) or `pre-commit`). Set `LOG_LEVEL`
(`debug`, `info`, `warn`, `error`, or `v<n>`) to control log verbosity.

## Repository layout

```text
src/
  api/v1alpha1/      # NetEye CRD types + validating webhook
  controllers/       # NetEye reconciler
  internal/
    keycloak/        # shared Keycloak component (ClusterExtension + instance)
    resources/       # generic apply / ownership / readiness helpers
  bundle/ catalog/   # OLM bundle and file-based catalog
charts/              # Helm chart (installs the operator via OLM)
```

## License

Dual-licensed under either the [Apache License 2.0](LICENSE-APACHE) or the
[MIT license](LICENSE-MIT), at your option.
Copyright © Würth IT Italy S.r.l.
