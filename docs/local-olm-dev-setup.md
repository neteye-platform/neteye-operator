# Local OLM dev environment setup (single-node RKE2)

How to stand up a working OLMv1 + neteye-operator dev/test environment on a
single-node RKE2 cluster (e.g. `neteye4-dev`), using a local, self-hosted
container registry instead of the real production registry or a third-party
one. This is the clean, repeatable version of a path found by trial and
error.

> Two things this guide assumes are not yet in this repo:
> - **`charts/`** (step 6) — a Helm chart wiring `ClusterCatalog` +
>   `ClusterExtension` + pull secret. Until it exists, apply those objects by
>   hand from `config/` instead of `helm install`.
> - **A file-based (FBC) catalog.** The catalog image built in step 3 here is
>   FBC (`index.yaml`, declarative). This repo's `make catalog-build` still
>   scaffolds the legacy `opm index add` sqlite-index format instead —
>   confirm OLMv1 accepts that before following step 3 as written, or build
>   the catalog by hand as an FBC image in the meantime.

## Prerequisites

- Root SSH access to the node.
- `podman` (or `docker`) and `helm` available locally, for building images and
  templating the chart.
- The node needs outbound internet access (to install OLMv1/cert-manager and,
  in this repo's case, to pull `keycloak-operator` from `operatorhubio`).

## 1. Confirm the node

```bash
ssh root@<node> "kubectl get nodes -o wide"
```

Note the node's **internal IP** — you'll need it for the local registry
address (e.g. `172.19.69.105`). If the node's role is missing `control-plane`
(single combined node should show `control-plane,worker`), fix it:

```bash
ssh root@<node> "kubectl label node <node-name> node-role.kubernetes.io/control-plane=\"\" --overwrite"
```

## 2. Install OLMv1

```bash
scp hack/install-olm.sh root@<node>:/tmp/
ssh root@<node> "chmod +x /tmp/install-olm.sh && /tmp/install-olm.sh"
```

This installs `cert-manager`, OLMv1 (`olmv1-system` namespace), and the
`operatorhubio` `ClusterCatalog`. Verify:

```bash
ssh root@<node> "kubectl get pods -n olmv1-system"
ssh root@<node> "kubectl get clustercatalog"
```

Both `catalogd-controller-manager` and `operator-controller-controller-manager`
should be `1/1 Running`; `operatorhubio` catalog should show `SERVING: True`.

Note: this also creates a `ClusterIssuer` named `olmv1-ca` (a self-signed CA)
used internally by OLM's own webhook/catalog-serving certs. **Reuse this same
issuer for your local registry's TLS cert** (see step 4) — that way
`catalogd`/`operator-controller` already trust it, with zero extra
configuration on their side.

## 3. Build your images

Build the operator image, a bundle image (containing exactly one CSV — see
"Gotcha: bundle/manifests has multiple CSVs" below), and a catalog image
(FBC format), all tagged for your target registry, e.g.:

```bash
podman build -t <registry>/neteye-operator:<version> .
podman build -t <registry>/neteye-operator-bundle:<version> <bundle-build-context>/
podman build -t <registry>/neteye-operator-catalog:<version> <catalog-build-context>/
```

Where `<registry>` will be `<node-ip>:5000` per this guide.

**Important — don't bother importing images into the node's containerd via
`ctr`.** That only helps the operator's own `Deployment` pod (kubelet uses
containerd directly). `catalogd` and `operator-controller` pull images via
their own OCI client, completely bypassing the node's local image cache. You
need a real, network-reachable registry for the catalog and bundle images no
matter what.

## 4. Stand up a local registry with a trusted cert

Run a plain `registry:2` container on the node itself (via `podman`, not a
Kubernetes Deployment — simplest for a single-node dev box):

```bash
ssh root@<node> "podman run -d --name test-registry -p 5000:5000 --restart=always registry:2"
```

**Plain HTTP will not work** — `catalogd` refuses HTTP registries outright
(`http: server gave HTTP response to HTTPS client`, no insecure-registry
opt-out exists). Issue it a cert from the `olmv1-ca` `ClusterIssuer` (already
installed in step 2):

```bash
cat <<EOF | ssh root@<node> "kubectl apply -f -"
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: local-registry-tls
  namespace: default
spec:
  secretName: local-registry-tls
  ipAddresses:
    - "<node-ip>"
  dnsNames:
    - "<node-hostname>"
  issuerRef:
    name: olmv1-ca
    kind: ClusterIssuer
EOF
```

Extract the cert/key and restart the registry with TLS:

```bash
ssh root@<node> '
mkdir -p /tmp/registry-certs
kubectl get secret local-registry-tls -o jsonpath="{.data.tls\.crt}" | base64 -d > /tmp/registry-certs/tls.crt
kubectl get secret local-registry-tls -o jsonpath="{.data.tls\.key}" | base64 -d > /tmp/registry-certs/tls.key
podman rm -f test-registry
podman run -d --name test-registry -p 5000:5000 --restart=always \
  -v /tmp/registry-certs:/certs:Z \
  -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt \
  -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key \
  registry:2
'
```

Also extract the CA cert alone (needed in step 5, for containerd/kubelet):

```bash
ssh root@<node> \
  'kubectl get secret local-registry-tls -o jsonpath="{.data.ca\.crt}" | base64 -d > /etc/rancher/rke2/registry-ca.crt'
```

Verify:
```bash
ssh root@<node> "curl -sS --cacert /etc/rancher/rke2/registry-ca.crt https://<node-ip>:5000/v2/_catalog"
```

## 5. Push images and configure containerd to trust the CA

```bash
ssh root@<node> "
podman tag <local-tags...> <node-ip>:5000/...
podman push --tls-verify=false <node-ip>:5000/neteye-operator:<version>
podman push --tls-verify=false <node-ip>:5000/neteye-operator-bundle:<version>
podman push --tls-verify=false <node-ip>:5000/neteye-operator-catalog:<version>
"
```

`catalogd`/`operator-controller` will now be able to reach the registry (they
already trust `olmv1-ca`). But **kubelet/containerd will not** — they need
their own trust config. This is where it gets RKE2-installation-specific:

> **Gotcha: find the real config file first.** Don't assume
> `/etc/rancher/rke2/registries.yaml` is read. Check the running process's
> environment for an override:
> ```bash
> ssh root@<node> "cat /proc/\$(pgrep -f 'rke2 server')/environ | tr '\0' '\n' | grep -i rke2"
> ```
> On `neteye4-dev` this cluster uses `RKE2_CONFIG_FILE=/neteye/local/rke2/conf/config.yaml`,
> and that file's `private-registry:` key points to
> `/neteye/local/rke2/conf/registry.yaml` — a **non-default path and
> filename** (`registry.yaml`, not `registries.yaml`). Always check
> `private-registry:` in the actual active config file rather than assuming
> the RKE2 default.

Write the registry config to whatever path `private-registry:` resolves to:

```yaml
mirrors:
  "<node-ip>:5000":
    endpoint:
      - "https://<node-ip>:5000"
configs:
  "<node-ip>:5000":
    tls:
      ca_file: /etc/rancher/rke2/registry-ca.crt
```

Then restart RKE2 to regenerate containerd's `certs.d`:

```bash
ssh root@<node> "systemctl restart rke2-server"
# wait ~20-25s, then confirm:
ssh root@<node> "kubectl get nodes"
ssh root@<node> "find /neteye/local/rke2/data/agent/etc/containerd/certs.d -type f"
# should show: certs.d/<node-ip>:5000/hosts.toml
```

## 6. Install via Helm

Use the chart in `charts/` (already wires up the `ClusterCatalog` +
`ClusterExtension` + pull secret — no need to hand-write these):

```yaml
# values-override.yaml
operator:
  packageName: neteye-operator
  catalogName: neteye-catalog
  catalogImage: <node-ip>:5000/neteye-operator-catalog:<version>
  versionRange: ">=<version>"
  channel: stable
  pollIntervalMinutes: 10
```

```bash
scp -r charts/* root@<node>:/tmp/neteye-operator-chart/
scp values-override.yaml root@<node>:/tmp/
ssh root@<node> "helm install neteye-operator /tmp/neteye-operator-chart -f /tmp/values-override.yaml"
```

## 7. Gotcha: pre-existing CRDs block OLM adoption

If the `neteyes.neteye.com` CRD (or any CR under it) already
exists on the cluster from a prior manual/non-OLM install, `operator-controller`
will refuse to manage it:

```
CustomResourceDefinition 'neteyes.neteye.com' already exists
in namespace '' and cannot be managed by operator-controller
```

Delete the CRD (this cascades to delete any CRs) before installing via OLM:
```bash
ssh root@<node> "kubectl delete crd neteyes.neteye.com"
```

## 8. Gotcha: bundle/manifests has multiple CSVs

At the time of writing this repo's `bundle/manifests/` holds exactly one CSV,
so this does not bite yet. It will the moment a second release is cut without
clearing the directory first: `bundle.Dockerfile` copies the whole
`bundle/manifests/` directory as-is, and a real OLM bundle image must contain
**exactly one** CSV. For a test bundle image built from more than one, build
from a **scoped temp directory** containing only the CRD + the one CSV you
want:

```bash
mkdir -p /tmp/bundle-build/manifests /tmp/bundle-build/metadata
cp bundle/manifests/neteyes.neteye.com.crd.yaml /tmp/bundle-build/manifests/
cp bundle/manifests/neteye-operator.v<X>.clusterserviceversion.yaml /tmp/bundle-build/manifests/
cp bundle/metadata/annotations.yaml /tmp/bundle-build/metadata/
# then build with a Dockerfile that COPYs manifests/ and metadata/ from that dir
```

This is a pre-existing structural issue in the repo, not something introduced
by this test — worth fixing separately if real bundle releases are ever cut
from this layout as-is.

## 9. Nudging reconciliation

`ClusterCatalog`/`ClusterExtension` don't always re-reconcile instantly after
you fix something out-of-band (e.g. after restarting the registry or fixing
containerd's CA trust). Force a reconcile with a no-op annotation bump:

```bash
ssh root@<node> "kubectl annotate clusterextension <name> force-reconcile=\"$(date +%s)\" --overwrite"
```

Or delete/recreate the `ClusterCatalog` (`kubectl delete clustercatalog <name>`,
then `helm upgrade` to recreate it via the chart).

## 10. Gotcha: `systemctl restart rke2-server` resets the control-plane label

If you restart `rke2-server` (e.g. after changing `private-registry` config,
step 5), RKE2 re-asserts `node-role.kubernetes.io/control-plane` back to its
own default value (`"true"` on this cluster's RKE2 build) — **overwriting**
the empty-string override from step 1. OLM's `catalogd` and
`operator-controller` Deployments use an **exact-match** `nodeSelector` on
that label with value `""`. A value mismatch (`"true"` vs `""`) means both
pods go `Pending`/`Error`, silently breaking `ClusterCatalog`/`ClusterExtension`
reconciliation with no obvious top-level error — you have to check
`kubectl get pods -n olmv1-system` and `kubectl describe pod ...` to see
`FailedScheduling: node(s) didn't match Pod's node affinity/selector`.

Fix: re-apply the override after every `rke2-server` restart:
```bash
ssh root@<node> "kubectl label node <node-name> node-role.kubernetes.io/control-plane=\"\" --overwrite"
```

## 11. Gotcha: ephemeral node state (`/tmp`, `/etc/rancher/rke2/*`, podman images)

On this environment, `/tmp` contents (the test registry's TLS cert files, the
scratch catalog/bundle build directories) and even files under
`/etc/rancher/rke2/` (e.g. `registry-ca.crt`) can disappear between sessions
(node reboot, `/tmp` cleanup, or similar). Don't assume anything set up in a
previous session is still there — re-check before continuing a multi-step
build:

```bash
ssh root@<node> "podman ps -a | grep test-registry"          # is the registry container still running?
ssh root@<node> "ls /tmp/registry-certs /etc/rancher/rke2/registry-ca.crt"
ssh root@<node> "kubectl get secret local-registry-tls"       # cert-manager Certificate/Secret
ssh root@<node> "cat /neteye/local/rke2/conf/registry.yaml"    # containerd trust config
ssh root@<node> "helm list -A"                                 # is the release still installed?
```

If any of these are gone, redo the corresponding step (3-5) before trying to
push/pull images again — the symptoms are confusing otherwise (e.g. `helm
upgrade` "succeeding" against a `ClusterCatalog` that can no longer resolve
its image because the registry container is gone).

## 12. Gotcha: disk pressure crashes `catalogd` in a hard-to-diagnose way

A `go build`/`podman build` that runs out of disk space
(`no space left on device`) can trigger a **`disk-pressure` node taint**. Any
pod that doesn't tolerate it (including `catalogd-controller-manager`) gets
evicted — and Kubernetes' Deployment controller will keep recreating and
re-evicting new replicas in a loop for as long as the taint is present,
producing dozens of `Evicted` pods. Once you free disk space (see below), the
taint clears and a fresh pod schedules, but you still need to clean up:

```bash
ssh root@<node> "podman image prune -f && podman container prune -f && podman builder prune -f"
ssh root@<node> "kubectl delete pods -n olmv1-system --field-selector=status.phase=Failed"
```

Then re-nudge the `ClusterCatalog`/`ClusterExtension` (step 9) since they were
stuck without a working `catalogd` in the meantime.

Also check `df -h` across **all** mount points, not just `/` — on this
cluster, podman's storage lives on `/var` (a separate LVM volume from `/`),
which can fill up independently of the root filesystem looking fine.

## 13. Gotcha: `keycloak-operator` needs `spec.startOptimized: false` for community images

If the `Keycloak` CR's pod crashes immediately with:
```
The '--optimized' flag was used for first ever server start.
Please don't use this flag for the first startup or use 'kc.sh build' to build the server first.
```
it's because `keycloak-operator` assumes any custom `spec.image` has already
been pre-built (`kc.sh build`) unless told otherwise. Set:
```yaml
spec:
  startOptimized: false
```
This is required whenever `spec.image` points at the vanilla
`quay.io/keycloak/keycloak` community image (as opposed to a custom image
built via multi-stage `kc.sh build`). After changing this, the running pod
needs to be deleted manually once (`kubectl delete pod <keycloak-pod>`) so it
picks up the updated `StatefulSet` template — the `StatefulSet` itself updates
immediately, but existing pods aren't recreated automatically.

## 14. Gotcha: the operator never provisions the database itself

Per the [upstream Keycloak Operator docs](https://www.keycloak.org/docs/latest/server_installation/#_operator),
*"The Keycloak Operator does not manage the database and you need to
provision it yourself."* This project's `EnsureDBSecret`/`EnsureKeycloakCR`
only ever *reference* a DB hostname
(`postgres-db.<namespace>.svc.rke2.neteyelocal`) — nothing in the operator
creates the actual database. If nothing is listening there, Keycloak crashes
with `java.net.UnknownHostException: postgres-db...`.

To provision a real Postgres for local testing, install CloudNativePG and a
`Cluster`, then alias its `-rw` service to the plain hostname this operator
expects:

```bash
# 1. Install CloudNativePG operator
ssh root@<node> "kubectl apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.0.yaml"
ssh root@<node> "kubectl rollout status deployment -n cnpg-system cnpg-controller-manager"

# 2. Install a storage provisioner if none exists (check `kubectl get storageclass` first)
ssh root@<node> "kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.30/deploy/local-path-storage.yaml"
ssh root@<node> "kubectl patch storageclass local-path -p '{\"metadata\": {\"annotations\":{\"storageclass.kubernetes.io/is-default-class\":\"true\"}}}'"

# 3. Create a Cluster using the existing db secret this operator already created
cat <<EOF | ssh root@<node> "kubectl apply -f -"
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: postgres-db
  namespace: neteye-system
spec:
  instances: 1
  storage:
    size: 1Gi
  bootstrap:
    initdb:
      database: keycloak
      owner: testuser
      secret:
        name: keycloak-db-secret
EOF

# 4. CNPG creates postgres-db-rw/-ro/-r services, not a plain "postgres-db" one.
#    Alias it, since that's the hostname EnsureKeycloakCR/dbHostForNamespace expects:
cat <<EOF | ssh root@<node> "kubectl apply -f -"
apiVersion: v1
kind: Service
metadata:
  name: postgres-db
  namespace: neteye-system
spec:
  selector:
    cnpg.io/cluster: postgres-db
    cnpg.io/instanceRole: primary
  ports:
    - port: 5432
      targetPort: 5432
EOF

# 5. Restart the Keycloak pod so it picks up the now-resolvable hostname
ssh root@<node> "kubectl delete pod neteye-kc-0 -n neteye-system"
```

Verify: `kubectl get cluster -n neteye-system` should show
`Cluster in healthy state`, and
`kubectl get keycloak neteye-kc -n neteye-system -o go-template='{{range .status.conditions}}{{.type}}: {{.status}}{{"\n"}}{{end}}'`
should show `Ready: True`.

## Testing an upgrade (two-version flow)

To simulate a real OLM-driven upgrade end-to-end:

1. Build and push a second version's operator/bundle images (same registry,
   new tag, e.g. `v0.2.15`), with a CSV bumping `metadata.name`, `spec.version`,
   and the container image ref.
2. Add a second `olm.bundle` entry to the FBC `index.yaml`, and extend the
   `olm.channel` entry list with `replaces: <previous-version-CSV-name>`.
3. Rebuild and push the catalog image under a **new tag** containing both
   bundle entries (don't reuse the old tag — image references are meant to be
   immutable; reusing a tag works but is confusing to reason about).
4. Update the Helm values' `operator.catalogImage` to the new catalog tag
   (leave `versionRange` as `>=<first-version>` — no need to bump it, any
   version satisfying the range that's newer in the channel will be picked).
5. `helm upgrade` and nudge reconciliation (step 9) if needed.
6. Verify: `kubectl get clusterextension <name> -o wide` should show the new
   `INSTALLED BUNDLE`/`VERSION`, and the operator pod's logs should show the
   new binary running (e.g. a bumped startup log version string).

## Quick verification checklist

```bash
ssh root@<node> "kubectl get clustercatalog <name> -o wide"        # SERVING: True
ssh root@<node> "kubectl get clusterextension <name> -o wide"      # INSTALLED: True
ssh root@<node> "kubectl -n <ns> get pods"                          # operator pod Running
ssh root@<node> "kubectl -n <ns> logs deploy/<name> --tail=15"       # confirm version string
ssh root@<node> "kubectl get cluster -n <ns>"                        # Postgres: Cluster in healthy state
ssh root@<node> "kubectl get keycloak <name> -n <ns> -o go-template='{{range .status.conditions}}{{.type}}: {{.status}}{{\"\n\"}}{{end}}'"  # Ready: True
```
