# Deploying the NetEye Operator on AWS EKS

This directory contains example manifests to install the NetEye Operator on an
Amazon EKS cluster and expose Keycloak through an AWS Application Load Balancer
(ALB). The manifests assume EKS Auto Mode (the Gateway API controller is
`eks.amazonaws.com/gateway-alb`). Placeholders in `<...>` must be replaced
before applying.

- `olmv1_values.yaml`: Helm values for the OLM v1 chart.
- `cluster_catalogs.yaml`: `ClusterCatalog`s for OperatorHub and the NetEye
  catalog.
- `neteye_operator.yaml`: namespaces and the `ClusterExtension` that installs
  the operator.
- `neteye.yaml`: shared namespace, issuer, database secret, `NetEye` resource
  and the Keycloak `HTTPRoute`.
- `gateway.yaml`: `GatewayClass`, ALB configuration, `Gateway` and
  HTTP-to-HTTPS redirect.
- `pg_test.yaml`: throwaway PostgreSQL for testing only.

## Prerequisites

- An EKS cluster with `kubectl` and `helm` access.
- The Gateway API CRDs and the AWS Gateway CRDs (`gateway.k8s.aws`) available
  in the cluster.
- An ACM certificate in the cluster's region covering your Keycloak hostname.
- A PostgreSQL database reachable from the cluster (for example Amazon RDS).

## 1. Install cert-manager

```sh
helm install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --version v1.21.3 \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true
```

## 2. Install OLM v1

Review `olmv1_values.yaml` first. It enables the bundled cert-manager
integration and pins the OLM pods to the EKS Auto Mode `standard` node class.

<!-- markdownlint-disable MD013 -->
```sh
helm install olmv1 \
  oci://ghcr.io/neteye-platform/charts/olmv1:1.12.0@sha256:7e955435dceeed51a50beaf85c7a8fe769d35dc5de0611714ed930525d96f47b \
  --values examples/eks/olmv1_values.yaml
```
<!-- markdownlint-enable MD013 -->

## 3. Install the operator

Create the catalogs, then the namespaces and the `ClusterExtension`:

```sh
kubectl apply -f examples/eks/cluster_catalogs.yaml
kubectl apply -f examples/eks/neteye_operator.yaml
```

The `ClusterExtension` pins the operator version and channel. Adjust
`spec.source.catalog.version` and `channels` as needed. Check the installation
with:

```sh
kubectl get clusterextension neteye-operator
kubectl get pods -n neteye-system
```

## 4. Prepare the database

Keycloak needs a PostgreSQL database. For production, use your own instance
(for example Amazon RDS) and set its address in `neteye.yaml`.

For a quick test only, `pg_test.yaml` runs a non-persistent PostgreSQL pod with
the credentials `test`/`test` and a `keycloak` database:

```sh
kubectl apply -f examples/eks/pg_test.yaml
```

In that case use `pg-test.pg-test.svc.cluster.local` as the database host.

## 5. Create the NetEye resource

Edit `neteye.yaml` and replace:

- `<MY_DB_USERNAME>` / `<MY_DB_PASSWORD>`: Keycloak database credentials (stored
  in the `keycloak-db-credentials` secret, the name the operator reads by
  default).
- `<MY_DB_HOST>`: PostgreSQL host name.
- `<MY_HOSTNAME>`: public host name for Keycloak.

Then apply it. This creates the `neteye-tenant-shared` namespace, so it must be
applied before `gateway.yaml`:

```sh
kubectl apply -f examples/eks/neteye.yaml
```

The example sets `gateway.managed: false`: the operator does not create a
Gateway, and the `keycloak-public` `HTTPRoute` in `neteye.yaml` routes traffic
to the `neteye-kc-service` Service instead.

## 6. Expose Keycloak through an ALB

Edit `gateway.yaml` and replace:

- `<MY_REGION>`, `<MY_ACCOUNT_ID>`, `<MY_CERTIFICATE_ID>`: the ARN of the ACM
  certificate used for HTTPS.
- `<MY_HOSTNAME>`: the same host name used in `neteye.yaml`.

```sh
kubectl apply -f examples/eks/gateway.yaml
```

Once the load balancer is provisioned, find its address and point a DNS record
for your hostname at it:

```sh
kubectl get gateway neteye-public -n neteye-tenant-shared
```

## Verify

```sh
kubectl get neteye -n neteye-tenant-shared
kubectl get pods -n neteye-tenant-shared
kubectl get httproute -n neteye-tenant-shared
```
