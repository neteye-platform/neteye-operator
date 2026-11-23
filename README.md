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

### Gateway exposure

By default the operator creates a Gateway API `Gateway` from `spec.gateway`,
an HTTP-to-HTTPS redirect route, and the routes and cert-manager TLS
certificates that expose Keycloak and, when enabled, the OpenTelemetry
Collector. Where exposure is handled outside the operator, for example on EKS,
set `spec.gateway.managed: false`:

```yaml
spec:
  gateway:
    managed: false
```

The operator then creates none of these and deletes the ones it created
earlier; Gateways and routes it does not control are left untouched.
`gateway.name`, `gateway.className`, and `internalCertificateIssuerRef` are
only required while the Gateway is managed. Route traffic yourself to the
`neteye-kc-service` Service (port 8080) and the `otel-collector-service` Service
(4317 for gRPC, 4318 for HTTP) in the `neteye-tenant-shared` namespace.

### Alyvix RDP access

The Alyvix feature module is enabled per tenant. A `NetEyeTenant` with
`spec.alyvix.enabled: true` gets an Apache Guacamole web application and
proxy daemon (guacd) in its namespace. The Alyvix module opens RDP sessions
to the Alyvix nodes through them:

```yaml
spec:
  displayName: Default
  alyvix:
    enabled: true
    rdp:
      # TCP ports of RDP on the Alyvix nodes; the default is 3389.
      ports: [3389]
```

The images come from the release data of the `NetEye` resource. The services
of a tenant have the hostname `<service>.tenant-<tenant>.neteyelocal`, where
`<tenant>` is the name of the tenant namespace without the prefix
`neteye-tenant-`:
the RDP access is on `guacamole.tenant-<tenant>.neteyelocal`.

- The Guacamole JSON authentication key of a tenant is generated once, in the
  Secret `alyvix-rdp-json-auth` (key `json-secret-key`, 32 hexadecimal
  characters) of the tenant namespace. The Alyvix module needs the same key.
  The key stays when the access is disabled; delete the Secret to get a new
  one.
- Each tenant has its own Gateway `neteye`, in its namespace, with the
  GatewayClass of the `NetEye` resource, so the listeners and their limits
  are not shared between the tenants. Its HTTPS listener `guacamole` serves
  only the hostname of the tenant and accepts only the routes of the tenant
  namespace, and it has no HTTP listener. The Gateway gets its address from
  the load balancer pool: the annotations of `spec.gateway` are for the shared
  Gateway only. It is created only after the shared Gateway has its address,
  so the pool cannot give that address to a tenant.
- The route matches only the paths that the module and the browser use:
  `POST /guacamole/api/tokens`, `DELETE /guacamole/api/session`,
  `GET /guacamole/websocket-tunnel` and
  `GET /guacamole/guacamole-common-js/all.min.js`.
- The certificate of the listener (`alyvix-rdp-tls`) is signed by the Issuer
  `alyvix-rdp-ca` of the tenant namespace. That Issuer signs with an
  intermediate CA of the tenant: the Certificate `alyvix-rdp-ca-<namespace>`
  of the shared namespace, signed by `spec.internalCertificateIssuerRef`. The
  operator copies its certificate, its key and the certificate of the root CA
  into the Secret `alyvix-rdp-ca-secret` of the tenant namespace, also after a
  renewal. The tenant namespace never holds the key of the root CA. Clients
  that trust the root CA trust the certificate of the listener, through the
  chain of the intermediate CA, which the Gateway sends: a client that limits
  the depth of the chain must allow 2, as the web server of NetEye does.
  The intermediate CA is removed when the RDP access is
  disabled or the tenant is deleted.
- guacd trusts the CAs of the optional ConfigMap that `spec.caBundle` of the
  tenant selects (default `neteye-ca-bundle`, key `ca-bundle.pem`, in the
  tenant namespace) for the RDP certificates of the Alyvix nodes, in addition
  to the system CAs. A changed bundle applies to the next RDP sessions once
  the kubelet refreshes the volume; guacd is not restarted, so the open
  sessions stay. The certificates are verified only for connections that do
  not ignore them (the Guacamole parameter `ignore-cert`).
- The egress policies are not a destination allowlist: both workloads may
  send DNS queries (TCP and UDP ports 53 and 5353) to any address, and guacd
  may connect to the RDP ports on any address. Apart from DNS, the web
  application reaches only guacd on TCP port 4822.

With `spec.gateway.managed: false` the operator creates neither the Gateway of
the tenant nor its certificate nor the route: route traffic to the
`alyvix-guacamole` Service (port 8080) of the tenant namespace yourself.

### Procedure gates

Parts of NetEye still run on the legacy infrastructure and are installed and
upgraded by the NetEye Ansible procedure. The two sides synchronize through
`NetEyeProcedureGate` resources (short name `nepg`) in the namespace of the
`NetEye` resource. A gate records that a step for a NetEye `version` is done;
its existence is the signal:

```yaml
apiVersion: neteye.cloud/v1alpha1
kind: NetEyeProcedureGate
metadata:
  name: 4.52-keycloak-db-configured
  namespace: neteye-tenant-shared
spec:
  version: "4.52"
  gate: keycloak-db-configured
  producer: Ansible
```

While installing (`status.currentVersion` empty, phase `Installing`) or
upgrading (`status.currentVersion` is the previous release, phase
`Upgrading`), each component waits for the gates its release plan requires and
the operator creates the gates it produces once that component is ready. The
other components keep reconciling. A disabled component, such as the Elastic
Stack when `elasticStack.enabled` is false, neither waits for nor creates
gates, since its feature module is disabled on the legacy side too.
`status.currentVersion` is set once every component is ready, after which
gates are no longer checked. An administrator can create a missing gate by
hand to unblock a procedure. Set
`spec.useProcedureGates: false` where no Ansible procedure runs; the operator
then neither waits for nor creates gates.

## Documentation and support

This repository contains the operator only. NetEye itself is a commercial
product and requires a valid license to run. For the product, its
documentation, licensing, and support, see Würth IT Italy:

- Documentation: <https://neteye.guide/current/>
- Product page: <https://www.wuerth-it.it/en/it-system-management/>
- Email: <info.italy@wuerth-it.com>

## Install

The operator is packaged for OLM. To install it, first create a `ClusterCatalog`
that references the `neteye-catalog:latest` image from the separate
catalog repository (`neteye-platform/neteye-catalog`), the
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
separate `neteye-catalog` repository. OLM requires bundle-format
channel metadata, so every bundle declares the `preview` channel metadata.
This is not a release input and does not assign catalog membership.

OLM refuses to upgrade a bundle whose CRDs change in ways it can't prove safe,
such as an added CEL validation rule, a removed field, or a newly required
field. `make crd-upgrade-check` runs [crdify](https://github.com/kubernetes-sigs/crdify)
with OLM's rules (`src/hack/crdify.yaml`) against the CRDs at `CRD_BASE_REF`
(default `origin/main`); CI runs it on every pull request against its base.
Put cross-field rules in the validating webhook instead of CEL.

### OLM catalog

The file-based OLM catalog and its catalog image build are maintained in the
separate `neteye-catalog` repository. This repository builds the
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
versions. In every channel the new release replaces the previous head and
skips all earlier entries, so installations can upgrade straight to it from any
older version. The release workflow rejects tags that do not exactly match the
checked-in version or do not come from an allowed release source.

After validation, the workflow builds and publishes the operator and bundle
images with SBOM and provenance attestations. It passes the release version to
both image builds and records the resulting bundle digest. It then opens a
version-specific pull request in `neteye-platform/neteye-catalog` that
references the immutable bundle digest. Rerunning the workflow updates or
reports the same pull request instead of opening a duplicate.

Cross-repository pull requests use a GitHub App installed on
`neteye-platform/neteye-catalog` with repository `Contents: Read and
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
repository publishes `neteye-catalog:latest`. Never move a release tag
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
