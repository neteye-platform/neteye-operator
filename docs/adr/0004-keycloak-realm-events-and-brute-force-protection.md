# ADR-0004: KeycloakRealm Events and Brute Force Protection Are Always Enforced

- **Status:** Accepted
- **Date:** 2026-09-09

## Context

The legacy ansible-based NetEye installer configures every Keycloak realm
unconditionally with a fixed set of event-logging and brute-force-lockout
settings (`src/ansible/roles/keycloak-setup/tasks/setup_realm.yml` in the
`neteye/keycloak` repository). `KeycloakRealmSpec` must reach parity with that
task so the operator-managed realm is not weaker than the realm the installer
used to produce.

Other optional sub-resources in the Keycloak API group (for example
`KeycloakClientSpec.ServiceAccount`) are modeled as optional pointer structs:
when omitted, the operator leaves that aspect of the remote object untouched.
Applying that same pattern to the new `Events` and `BruteForceProtection`
settings would make them opt-in per realm, and an omitted block would leave
whatever values already exist in Keycloak (including Keycloak's own defaults,
which are weaker than the ansible-configured values).

## Decision

`KeycloakRealmSpec.Events` and `KeycloakRealmSpec.BruteForceProtection` are
plain (non-pointer) structs, always present on every `KeycloakRealm`. Their
fields carry `+kubebuilder:default` values equal to the ansible task's fixed
values (`eventsExpiration: 15552000`, `failureFactor: 30`,
`waitIncrementSeconds: 60`, etc.). The operator always reconciles these
settings on every realm; there is no way to opt a realm out of them or leave
them unmanaged.

`permanentLockout` is not exposed as a spec field. The operator always sends
`permanentLockout: false`, matching the ansible task, which never varies this
value.

This is a deliberate deviation from the optional-pointer pattern used
elsewhere in the API group.

## Alternatives considered

### Optional pointer structs, opt-in per realm

Consistent with `KeycloakClientSpec.ServiceAccount`. Rejected because an
omitted block would silently leave event logging and brute-force protection
unmanaged, reproducing the exact drift (realms without event logging or
lockout protection) that the CRD is meant to eliminate.

### No kubebuilder defaults, rely on Keycloak server defaults

Rejected because Keycloak's built-in defaults are weaker than the values the
ansible task enforces (for example, brute-force protection is off by
default). Omitting defaults would silently regress every realm created
through this CRD relative to the legacy installer.

### Expose `permanentLockout` as a configurable field

Rejected: no realm has ever needed a different value, and exposing it adds
spec surface for a setting that would be surprising to enable (locking users
out permanently rather than temporarily).

## Consequences

Every `KeycloakRealm` gets ansible-parity event logging and brute-force
protection without extra configuration, and Keycloak-side drift on these
settings is corrected on every reconcile.

A realm cannot opt out of these settings through this CRD. If a future realm
genuinely needs different behavior, the field-level defaults must be
overridden explicitly in that realm's spec; there is no "leave unmanaged"
option.

This diverges from the optional-pointer convention used for
`KeycloakClientSpec.ServiceAccount`; future readers of the Keycloak API group
should not assume all optional-looking configuration blocks in this group
follow the same opt-in pattern.

## References

- `src/ansible/roles/keycloak-setup/tasks/setup_realm.yml` (`neteye/keycloak`
  repository) — the legacy ansible task this CRD reaches parity with.
