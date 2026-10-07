# ADR-0006: Login-Sync Wiring and PermissionSync Exposure

- **Status:** Proposed
- **Date:** 2026-10-07

## Context

ADR-0005 makes PermissionSync a NetEye component and provisions the Keycloak
objects its technical caller authenticates with, but deliberately leaves two
things open: configuring the `login-sync` authenticator and placing it in a
browser flow, and terminating TLS in front of the PermissionSync listener.
Until both are resolved, no login is synchronized, the component still reports
Ready, and the only way to finish the integration by hand is to let the
authenticator post a service-account token to a plaintext endpoint.

The authenticator constrains the decision. It is configured only through
Keycloak SPI options, so its endpoint, client credentials, token endpoint, and
timeout are instance options of the Keycloak deployment. It refuses plaintext
credential-bearing endpoints unless `allow-insecure-http` is set. It runs
fail-closed: when PermissionSync cannot be reached, the login it runs in is
refused. And it needs a client secret, which ADR-0005 left to Keycloak to
generate, so nothing in the cluster can hand it to the authenticator.

Keycloak adds two constraints of its own. In a flow, a REQUIRED execution makes
Keycloak ignore the ALTERNATIVE executions at the same level, so the
authenticator cannot simply be appended to the built-in browser flow. And the
realm browser flow serves every client, including the admin console, so
binding a fail-closed step there would let a PermissionSync outage lock every
user out of every client.

Finally, the Permission Provider validates the forwarded caller token as an
independent resource server with its own audience (PermissionSync ADR-0008),
so a token carrying only the PermissionSync audience is refused one hop later.

## Decision

### The operator configures the authenticator

The operator sets the authenticator's SPI options on the Keycloak instance it
manages: the PermissionSync synchronization endpoint, the `login-sync` client
id and secret, the master realm token endpoint, and an HTTP timeout derived
from PermissionSync's overall request deadline plus a fixed margin, so the
caller never abandons a request PermissionSync would still have answered.
These options are managed: `additionalOptions` cannot override them, and
`allow-insecure-http` is managed and never set.

### The client secret is operator-generated and referenced, never inlined

This replaces ADR-0005's rule that the client secret is not managed. The
operator generates the `login-sync` client secret once, as a random value in a
Secret it owns in the shared namespace. The Keycloak client declares it as its
secret reference, and the authenticator option references the same Secret, so
the value never appears in a Keycloak, client, or `NetEye` resource. An
existing value is never replaced: rotation is an explicit act, not a side
effect of reconciliation.

### Caller tokens carry both audiences

The `login-sync` client carries one audience mapper for PermissionSync and one
for the Permission Provider, so the token PermissionSync forwards is accepted
by both. The target scopes and their grammar are unchanged from ADR-0005.

### A dedicated browser flow, bound only to target clients

The operator declares a dedicated browser flow in the master realm: Keycloak's
built-in browser flow, unchanged, wrapped in a REQUIRED subflow, followed by
the `login-sync` authenticator as a top-level REQUIRED execution. At the top
level it runs on every login, including one an existing SSO cookie completes.

The flow is not the realm browser flow. The operator binds it as the browser
flow override of each Keycloak client that is a declared PermissionSync
target, and removes that override from a client that stops being one. A
PermissionSync outage therefore refuses logins only on the clients it would
have synchronized, never on the admin console or any client without a target.
The flow resource is orphaned on deletion and redeclared when missing, so its
own lifecycle can never remove a flow a client is bound to.

### PermissionSync is published through the shared Gateway over HTTPS

PermissionSync gets its own HTTPS listener on the shared Gateway, under a
fixed internal hostname, with a certificate the internal issuer signs, and an
HTTPS route to its Service. The authenticator reaches both PermissionSync and
the token endpoint through the Gateway, so neither the service-account token
nor the synchronization payload crosses the pod network in plaintext between
Keycloak and the Gateway.

The component is not Ready until its certificate is issued and its route is
accepted by the Gateway listener, so a component that Keycloak cannot reach is
reported as such.

Resolving the internal hostname to the Gateway is a deployment
responsibility, like the identity hostname: the operator publishes the name
but does not manage name resolution.

### Keycloak trusts the platform CA, and only its public certificate

Keycloak trusts the CA that signs the Gateway certificates through a
truststore. The operator copies only the public certificate of the trusted CA
Secret into a ConfigMap and mounts that ConfigMap. The CA Secret also carries
the CA private key, which is never mounted into Keycloak. The identity
component waits for the trusted CA Secret before declaring the Keycloak
instance, because the authenticator cannot work without it.

### Network policy follows the Gateway path

The Keycloak pods are allowed egress to the Gateway on its HTTPS port and to
the backends the Gateway routes their requests to, PermissionSync and
Keycloak itself, because Cilium checks traffic through the Gateway against
both. PermissionSync admits ingress from the Gateway and from the node running
its probes; the direct path from Keycloak is removed.

## Alternatives considered

### Bind the flow as the realm browser flow

One binding would cover every client and would need no per-client
reconciliation. It was not chosen because the authenticator is fail-closed: an
outage would refuse every login in the realm, including the administrator's,
which turns a degraded permission backend into a lost identity service.

### Keep the plaintext in-cluster hop with `allow-insecure-http`

It needs no listener, certificate, or truststore. It was not chosen because it
exposes a credential-bearing request to anything that can observe pod-network
traffic, and because enabling an option whose purpose is to weaken the
authenticator is not something a default deployment should do.

### Terminate TLS in PermissionSync or in a sidecar

PermissionSync does not terminate TLS, and adding a sidecar means a second
certificate lifecycle and a TLS component the platform does not otherwise
run. The shared Gateway already terminates TLS for the identity service with
certificates from the same issuer.

### Mount the CA Secret directly as the truststore

It would avoid a copy, but the Secret holds the CA private key, which would
then be readable inside every Keycloak pod.

### Keep the client secret Keycloak-generated and copy it out

Reading the generated secret through the Admin API and writing it into a
Secret makes the cluster copy follow Keycloak's value, so a client recreated
by Keycloak silently changes the credential. A Secret the operator owns and
both sides reference keeps one source of truth.

## Consequences

A NetEye login on a target client is synchronized without any manual Keycloak
step, and the known plaintext gap ADR-0005 records between Keycloak and
PermissionSync is closed. The hop from the Gateway to the PermissionSync pod
remains plaintext inside the cluster, as for every Gateway backend.

A PermissionSync outage now refuses logins on target clients. That is the
authenticator's intended fail-closed behavior, and binding per client is what
keeps it from reaching the rest of the realm.

The identity component now depends on the trusted CA Secret and on the
Gateway publishing PermissionSync; until both exist, logins on target clients
fail and the respective components report what they wait for.

The operator now owns one more credential, the `login-sync` client secret.
Anyone who can read Secrets in the shared namespace can read it, as for the
other credentials there. Rotating it means deleting the Secret and letting
Keycloak pick up the new value, which is a disruptive, manual action.

A client that an administrator binds to another browser flow loses that
binding when it becomes a target, because the operator owns the override of
target clients. The binding of non-target clients is left untouched unless it
points at the PermissionSync flow.

Follow-up work: the deployment has to resolve the PermissionSync internal
hostname to the Gateway, as it does for the identity hostname.

## References

- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0005: PermissionSync Component and Caller Provisioning](0005-permissionsync-component-and-caller-provisioning.md)
- PermissionSync ADR-0001: Inbound Synchronization Contract and Caller-Owned
  Workflow Policy
- PermissionSync ADR-0002: Caller Authentication and Authorization
- PermissionSync ADR-0008: Generic REST Permission Provider Wire and Transport
  Contract
