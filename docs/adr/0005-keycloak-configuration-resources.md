# ADR-0005: Keycloak Configuration Resources

- **Status:** Accepted
- **Date:** 2026-11-03

## Context

NetEye needs a configured Keycloak. This includes realms, clients, users, and
authentication flows. Before, Ansible roles did this configuration. The
Keycloak Operator runs the Keycloak instance (ADR-0001), but it does not
manage this configuration continuously.

Users must be able to declare this configuration as Kubernetes resources.
Some Keycloak objects can exist before the operator starts to manage them.

## Decision

### Resources and synchronization

- The operator has four CRDs in the group `neteye.cloud`: `KeycloakRealm`,
  `KeycloakClient`, `KeycloakUser`, and `KeycloakAuthFlow`.
- The controllers synchronize each resource with the Keycloak Admin REST API.
  They do not create Kubernetes resources for it.
- The controllers connect to the Keycloak Service in the cluster. They use the
  internal admin account of the operator if it operates. Else they use the
  bootstrap admin from the Secret `neteye-kc-initial-admin`.
- The controllers read the admin credentials from the namespace of the
  resource.
- If the credentials are missing, the status is `Failed`. If Keycloak is not
  available, the status is `NotReady`.
- The name of a realm in a `KeycloakRealm` resource is immutable.

### Existing Keycloak objects

- If a user or a client with the same name exists in Keycloak, the operator
  adopts it. It does not fail.
- For an adopted user, `status.adopted` is `true`. The operator does not
  change the credential of an adopted user.
- The operator adds the roles and groups in the spec. It does not remove roles
  or groups that the spec does not contain.
- For a realm, the operator merges the spec into the live realm. It keeps the
  fields that the spec does not contain.

### Deletion

- Each resource type has a finalizer.
- `spec.deletionPolicy` is `Delete` or `Orphan`. If it is empty, the operator
  uses `Delete`.
- `Delete` removes the object from Keycloak. `Orphan` keeps it.
- The CRD has no schema default for `deletionPolicy`. Thus the meaning of an
  empty value can change without an OLM upgrade failure.
- The operator does not delete the `master` realm.
- If the admin credentials are missing during a deletion, the operator
  removes the finalizer. The object stays in Keycloak.

### Platform resources

The operator creates these resources for NetEye: the `master` realm, the
internal admin user, the root user, the NetEye client, and two authentication
flows.

- The operator creates them only if they are missing. It does not update them.
  Administrators can change them, and the changes stay.
- If an administrator deletes one, the operator creates it again.
- They have no owner reference. When you delete the `NetEye` resource, they
  stay.
- They have `deletionPolicy: Orphan`.
- When the internal admin account operates, the operator disables the
  bootstrap admin account.

### Status

Each resource has `status`, `message`, and `observedGeneration`. The `status`
values are the same as in ADR-0002. `KeycloakClient` also has `clientUUID`.
`KeycloakUser` also has `userID`, `adopted`, and `credentialRotation`. The
status has no conditions.

## Alternatives considered

### Keep the configuration in Ansible

We did not select this option. Ansible runs only at installation and update
time. It does not correct drift.

### Reject existing Keycloak objects

We did not select this option. Installations that Ansible configured already
contain these objects. The operator must take them without data loss.

### Apply the platform resources on each pass

We did not select this option. An unconditional apply removes the changes
that administrators make.

## Consequences

- Users can declare Keycloak configuration in the same way as other
  Kubernetes resources.
- With the default policy `Delete`, deletion of a resource removes an adopted
  object. The operator did not create that object.
- The operator does not remove roles or groups from adopted accounts. An
  administrator must remove access that is not necessary.
- The platform resources do not follow later changes in the operator
  defaults. A new default does not apply to an existing installation.
- The platform resources stay after you delete the `NetEye` resource.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
