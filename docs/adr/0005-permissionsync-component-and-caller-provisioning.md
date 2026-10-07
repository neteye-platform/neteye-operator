# ADR-0005: PermissionSync Component and Caller Provisioning

- **Status:** Accepted
- **Date:** 2026-10-05

## Context

PermissionSync is a separate service that reconciles a user's desired
permissions with one selected target when that user logs in. Its own
repository fixes the service contract and deliberately leaves deployment
outside it: the service loads exactly one YAML document whose path comes only
from `PERMISSIONSYNC_CONFIG_FILE`, depends on no Kubernetes API, object, or
discovery semantics, and treats manifests and Helm charts as
deployment-owned optional artifacts. Kubernetes is its only explicitly
supported platform, and the properties that platform has to provide are
recorded as a reference example rather than as a product interface.

The caller is not an administrator but the Keycloak `login-sync`
authenticator NetEye's Keycloak image ships. On a browser login it posts one
synchronization request authenticated with a Client Credentials token. That
token must carry the PermissionSync audience and at most one
`permissionsync:<target>` scope, and the authenticator decides whether a login
client has a target at all by looking for a client scope named exactly
`permissionsync:<login client id>` in the login realm. Both the authenticator
repository and the Keycloak image repository state that provisioning that
client scope, its audience, and the service-account client is an operator
task outside them. Nothing in the platform performs it today, so the
integration cannot work without a manual Keycloak setup that no declarative
resource records.

Three further constraints shape the decision. PermissionSync's single
document carries the GLPI credentials and private trust material inline, so
it cannot be delivered as a ConfigMap. PermissionSync has no runtime reload,
so a configuration change has to reach the processes as a new revision. And
PermissionSync accepts a defective `provider` or `glpi` section by leaving
that component unavailable rather than failing to start, which would turn a
provisioning mistake into logins that are silently never reconciled.

## Decision

### PermissionSync is a NetEye component

PermissionSync is reconciled from the singleton `NetEye` resource as one
delegated component, like the identity and Elastic Stack components, in the
shared NetEye namespace. It reports its own entry in
`status.servicesStatus` and is reconciled after the identity component is
ready, because its caller provisioning needs a reachable Keycloak Admin API
and the service verifies tokens that identity service issues.

The component is part of every NetEye deployment and has no switch. Unlike the
Elastic Stack feature module, it is not opt-in: `spec.permissionSync` is a
required section, there is no `enabled` field, and the operator reconciles the
service unconditionally. Permission synchronization is part of what a NetEye
login does, not an add-on, so an installation that silently did not run it
would be a NetEye behaving differently from every other one.

Its image is resolved from release data as a digest-pinned component image and
is not selectable in the custom resource, as required by ADR-0003.

### The operator owns the configuration document

The operator renders the complete document and delivers it as a Secret it
owns, mounted read-only, with `PERMISSIONSYNC_CONFIG_FILE` as the container's
only environment value. The workload carries the document's resource version
as a pod-template annotation, so a changed document rolls the workload instead
of waiting for a reload the service does not have.

Every value PermissionSync accepts as a deployment bound is exposed in the
custom resource, and each one defaults to the value the operator would
otherwise have fixed: the replica count and log level, the trusted CA Secret,
the Permission Provider and GLPI endpoints with the GLPI credentials Secret
and optional authentication source, the logical target routes, the overall
request deadline, the inbound admission limit, the synchronization capacity,
the shutdown grace, the verification cache policy, the clock skew allowance,
and the Provider and GLPI operation timeout. A resource therefore describes
the whole runtime rather than hiding part of it, and an omitted section still
yields a completely configured service.

Endpoints that the NetEye deployment itself provides are defaulted to it
rather than left to each installation to repeat. The Permission Provider is
always the NetEye deployment's own permission API, reached through the
platform's internal `httpd.neteyelocal` entry point, so its section is always
present and the operator always renders it; GLPI defaults to the GLPI the
platform runs. Only an installation whose backend lives elsewhere overrides
one.

What stays derived rather than configured is the integration contract itself:
the issuer, from the identity hostname and the fixed master realm; the
audience and the target scope grammar; the signing-algorithm allowlist; and
the listener address and port. Those are not deployment choices — a different
value would simply stop the caller and the receiver agreeing.

The pod's termination grace period is derived from the configured shutdown
grace plus a fixed margin for PermissionSync's post-grace phases, so a longer
grace is actually honored instead of being cut short by a fixed horizon.

Trace export stays disabled. PermissionSync exports spans only to an absolute
HTTPS OTLP endpoint, which the in-cluster EDOT Gateway is not; structured logs
and Prometheus metrics are available either way.

### A refused configuration is never rendered

The operator validates the configuration before rendering it, both at
admission and again during reconciliation, because a webhook can be bypassed
or unavailable. A declared target whose Provider or adapter backend is absent
is refused, and a missing or empty user-managed Secret leaves the component
degraded with that reason reported. Nothing is rendered or deployed from a
refused configuration, so a provisioning mistake surfaces as a degraded
component instead of as an unreconciled login.

### The operator provisions the Keycloak side

For every declared logical target the operator provisions, in the master
realm, a client scope named `permissionsync:<target>` carrying the
PermissionSync audience, and it declares one confidential
`login-sync` client whose service account the authenticator authenticates as.
Target scopes are assigned to that client as Optional and never as Default: a
Default scope travels on every token, so a caller serving two targets would
emit two `permissionsync:` scopes in one token, which the receiver refuses.

That provisioning is additive. Scopes are created when missing and the
audience mapper is reconciled, but no scope assignment is revoked and a target
removed from the `NetEye` resource does not invalidate a caller still using
it. There is no teardown path: the component cannot be switched off, and its
objects are owned by the `NetEye` resource, so deleting that resource is what
removes them.

The client secret is not managed. No secret reference is declared, so the
secret Keycloak generates is never replaced and no Pod can mount it from the
cluster.

### What stays outside this decision

Three things are deliberately not resolved here. [ADR-0006](0006-login-sync-wiring-and-permissionsync-exposure.md)
resolves the first two and replaces the unmanaged client secret above.

- Configuring the `login-sync` authenticator itself — its endpoint, service
  account credentials, and token endpoint — and placing its execution in the
  browser flow. That is the Keycloak side of the authenticator, it requires
  the client secret above, and it belongs to a decision about that component.
- Exposing PermissionSync outside the cluster, and terminating TLS in front of
  its listener. It is reachable only as a ClusterIP Service, admitting the
  identity service and the node running its probes; no Gateway listener,
  certificate, or route is provisioned. PermissionSync terminates no TLS
  itself, so the in-cluster hop from the identity service is plaintext today,
  and the login-sync authenticator refuses a plaintext `service-endpoint`
  unless it is configured with `allow-insecure-http`. Closing that gap means
  choosing a TLS terminator for in-cluster traffic, which is the same
  unresolved decision as the operator's own plaintext hop to the Keycloak
  Admin API, and it is deliberately not resolved here.
- Creating the GLPI credentials and trusted-CA Secrets. They stay
  user-managed, and the component reports their absence rather than inventing
  them.

## Alternatives considered

### Deliver the document as a ConfigMap, with credentials as environment values

PermissionSync accepts no per-value environment overrides and no environment
substitution inside the document, so the credentials have to be inside the one
file. A ConfigMap would therefore mean storing the GLPI tokens in plaintext in
an object that is not treated as a secret.

### Let an administrator supply the document directly

PermissionSync accepts any volume source, so an operator-managed Secret is not
required by the service. It was still chosen, because deriving the issuer, the
audience, and the target routes from the `NetEye` resource is what keeps the
document consistent with the Keycloak objects the same resource provisions.
Two hand-maintained sides of one contract is exactly the manual setup this
decision removes.

### Make the component opt-in, like the Elastic Stack feature module

An `enabled` flag would let an installation defer PermissionSync and would
keep existing resources valid. It was not chosen because the product decision
is that every NetEye synchronizes permissions on login: a flag would make the
difference between installations invisible in the resource that is supposed to
describe them, and it would add a teardown path whose only purpose is to undo
something no one should be undoing.

### Keep the request and shutdown bounds operator-owned

Hiding the deadlines, the capacity, and the cache policy would make a valid
document the only possible output, since those values have validated
relations: the shutdown grace has to cover one complete request, and every
child timeout has to fit the overall deadline. It was not chosen because a
deployment that has to tune them would then have no way to, and because a
resource that silently omits part of the runtime it describes is harder to
audit than one that states it. The relations are enforced instead — at
admission, with the field that is wrong, and again during reconciliation,
because a webhook can be bypassed.

### Leave the Keycloak side to an administrator or to Ansible

This is the current state, and it is why the integration does not work out of
the box. A missing client scope is also the one misconfiguration that fails
silently: the authenticator then requests no scope, PermissionSync answers the
request as a targetless no-op, and the login succeeds without the target ever
being reconciled. Declaring the scopes and the caller makes that state
impossible to reach by omission.

### Create the client scopes through a new custom resource

A `KeycloakClientScope` resource with its own controller would match how
realms, clients, users, and authentication flows are declared. It was not
chosen for this decision because the scopes exist only as part of the
PermissionSync contract and have no independent lifecycle; the existing
component-level Admin API provisioning is enough. A resource becomes
worthwhile if client scopes gain consumers outside this integration.

## Consequences

Enabling PermissionSync becomes one declarative change to the `NetEye`
resource, and the Keycloak objects the integration needs come with it. The
component's status reports its resolved image and why it is not ready, and a
configuration that cannot work is refused at admission with the field that is
wrong.

The operator now renders a Secret containing credentials, which widens what a
compromise of the operator can read, and it needs `create`, `update`, and —
because disabling the component removes the rendered document — `delete` on
Secrets for it. The document is readable by anyone who can read Secrets in the
shared namespace, which is also true of the credentials it is assembled from.

An administrator still has to finish the integration by configuring the
`login-sync` authenticator with the client secret Keycloak generated and
placing its execution in the browser flow. Until that happens the component
reconciles and reports Ready while no login is synchronized, and that gap is
not visible in the `NetEye` status.

Because the in-cluster hop carries no TLS, completing that configuration
currently also means allowing the authenticator to post to a plaintext
endpoint, which exposes the service-account token and the synchronization
payload to anything that can observe pod-network traffic. The network policies
bound who may reach the listener, which is not the same protection. This is a
known gap, not an accepted end state.

Because provisioning is additive, removing a target from the resource leaves
its client scope and assignment in Keycloak. Cleaning those up is a manual
action, deliberately, so that it is never a side effect of editing the desired
state.

Making the section required is a breaking API change for an existing `NetEye`
resource that does not carry it: the next apply is rejected until the section
is added. The API group is `v1alpha1` and explicitly experimental, and the
resource is a singleton, so the migration is one edit per installation. A Go
client is unaffected, because the field always serializes.

An empty `spec.permissionSync` still validates and yields a service with the
default Provider but no routes, which answers every request as a targetless
no-op until a target is declared. Requiring at least one route would close
that gap; it is left open deliberately, so that an installation can deploy the
component before deciding which login clients it serves.

Exposing the bounds means an installation can now write a combination
PermissionSync refuses to start on. That is why the relations are validated
twice, and why the defaults are pinned by a test: the defaults are a valid
combination, and the pod always outlives the grace it configures.

Follow-up work: each PermissionSync release the operator line ships is a
release manifest change, so a component-only update is still an operator patch
release with a new bundle, as ADR-0003 requires.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- PermissionSync ADR-0001: Inbound Synchronization Contract
- PermissionSync ADR-0002: Receiver-Side JWT Verification
- PermissionSync ADR-0006: Runtime Configuration, OCI and Observability
- PermissionSync ADR-0009: GLPI Target Adapter
