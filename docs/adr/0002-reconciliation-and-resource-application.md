# ADR-0002: Reconciliation and Resource Application

- **Status:** Accepted
- **Date:** 2026-09-03

## Context

The operator changes the desired state in the `NetEye` resource into
Kubernetes resources. ADR-0001 makes the `NetEye` spec the only configuration
interface. Thus the operator must correct drift in the resources that it owns.

Other actors also write to these resources, for example API defaults and
other controllers. The operator must not take a resource that it does not own.

Users and scripts must have a stable value that shows if the installation is
ready.

## Decision

### Create and update

- The operator reads the live object. If the object is missing, the operator
  creates it. If the object exists, the operator updates it.
- The operator does not use Server-Side Apply.
- The desired spec replaces the full live `spec`. For a ConfigMap, the desired
  `data` replaces the live `data`.
- The operator compares the full desired spec with the full live spec. If they
  are different, the operator sends an update.
- The operator keeps the metadata of other actors, for example labels,
  annotations, and other owner references.
- For a Service, the operator keeps the IP fields that the cluster sets.
- If an update has a version conflict, the operator tries again.
- The operator sets the label `app.kubernetes.io/managed-by=neteye-operator`
  on new objects. The label is only for identification. The operator does not
  use it to find the owner.

### Ownership check

- Before an update, the existing object must have a controller owner
  reference with the UID of the `NetEye` resource.
- If the object has a different controller owner, or no controller owner, the
  operator does not change it. The operator does not delete it. The component
  fails and the status shows the error.
- Cluster-scoped objects, for example the Keycloak `ClusterExtension`, have no
  owner. The operator updates them without an ownership check.

### Deletion of resources

- There is no general removal of objects that are not in the desired state.
- A component can delete its own resources from a fixed list of names. It
  deletes an object only if the `NetEye` resource is its controller owner.

### Reconciliation behavior

- The reconciliation is level-based and idempotent. Each pass calculates the
  full desired state again.
- The controller watches only the `NetEye` resource. It starts a pass when
  the generation changes or when deletion starts.
- The controller does not watch owned resources. It corrects drift on the
  next periodic pass.
- The default intervals are 30 seconds for a component that progresses,
  2 minutes after a failure, and 10 minutes for a periodic pass.
- First, the operator does the shared base resources: the `Issuer` check, the
  Gateway, the HTTP-to-HTTPS redirect route, and the default-deny network
  policy. If one of these fails, the pass stops.
- Then the operator does the components in a dependency graph. The components
  are `identity`, `otel-collector`, and `edot-gateway`.

### Readiness

A successful create or update does not make a component ready. The operator
examines the readiness of each resource:

- A Deployment is ready when its observed generation is current and all
  replicas are updated and ready.
- A cert-manager Certificate is ready when its `Ready` condition is `True`.
- The Keycloak custom resource is ready when its observed generation is
  current and its `Ready` condition is `True`.
- A route is ready when the expected parent Gateway accepts it.

### Delegated resources

- The operator applies the delegated custom resource with the same rules as
  owned resources.
- The operator does not create, change, or delete the resources that the
  delegated operator makes.
- The status shows the readiness that the delegated operator reports. The
  operator does not show a component as ready only because the API server
  accepted the custom resource.

### Immutable fields

The operator does not delete and create an object again. If the API server
rejects an update because a field is immutable, the component fails.

### Status contract

The `NetEye` status has this schema:

```yaml
status:
  phase: NotReady
  message: Check services status for details
  observedGeneration: 4
  servicesStatus:
    identity:
      status: Ready
      message: Keycloak is ready
      resolvedImage: ghcr.io/neteye-platform/neteye-keycloak:1.0.7@sha256:<digest>
    elasticStack:
      status: NotReady
      message: <text>
      otelCollector:
        status: NotReady
        message: <text>
        resolvedImage: <image>
      edotGateway:
        status: Ready
        message: <text>
        resolvedImage: <image>
```

These rules apply:

- `phase` is the stable value for scripts. The values are `Ready`,
  `NotReady`, `Failed`, and `PendingUpgrades`.
- Use `phase` only when `observedGeneration` is equal to
  `metadata.generation`.
- If a component fails, `phase` is `Failed`. Else, if a component is not
  ready, `phase` is `NotReady`. Else, `phase` is `Ready`.
- Each service has `status`, `message`, and `resolvedImage`. The `status`
  values are `Unknown`, `NotReady`, `Ready`, `Failed`, and `Disabled`.
- A disabled component does not prevent `phase: Ready`.
- `resolvedImage` is the main image that the operator selected for the
  service. It does not show that all Pods use this image.
- Messages are for persons. They are not an API.
- The status has no conditions. The operator does not write Kubernetes
  events.
- The controller writes the full status through the status subresource at the
  end of each pass. Each pass starts from `Unknown`.
- The status shows the observed state. It is not a configuration interface.

## Alternatives considered

### Adopt existing objects

We did not select this option. The operator cannot know if a different actor
still uses the object. A name conflict must fail and be visible.

### Delete and create an object again after an immutable field change

We did not select this option. The deletion can cause downtime or data loss.
Each component must have its own procedure.

### Report only one global value

We did not select this option. It is difficult to find the failed part of the
installation. The `servicesStatus` map gives this information.

## Consequences

- The `NetEye` spec is the authority for the spec of owned resources. The
  operator corrects drift.
- A resource builder must give all spec fields, also fields with API
  defaults. If a field is missing, the operator removes it or updates the
  object on each pass.
- Changes that other actors make in the spec of owned resources do not stay.
- Drift stays until the next periodic pass because the controller does not
  watch owned resources.
- A name conflict with a resource that the operator does not own fails and is
  visible.
- A new component needs a new field in `servicesStatus`.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
