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
