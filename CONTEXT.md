# NetEye Operator

The NetEye Operator reconciles a singleton `NetEye` custom resource and its
delegated component resources (including the Keycloak identity service) into
a running NetEye installation on Kubernetes.

## Language

### Tenants

**Tenant**:
A NetEye tenant: one namespace that contains one `NetEyeTenant` custom
resource with the same name, `neteye-tenant-<id>`. Its dedicated resources
live in that namespace.
_Avoid_: Realm (the Keycloak mechanism a tenant maps to)

**Display name**:
The `spec.displayName` of a tenant, the name that people see and can change,
except while the tenant waits for its upgrade. Only one tenant holds a display
name: admission refuses a name that another tenant uses, and when two writes
pass admission at the same time, the operator gives the name to one tenant
and reports the other one as `Failed`. Derived names use `metadata.name`,
which never changes.
_Avoid_: Tenant name, when the stable `metadata.name` is meant

### Keycloak identity

**Realm**:
An isolated Keycloak tenant boundary that owns its own users, clients, and
authentication configuration. Declared by the `KeycloakRealm` custom
resource.
_Avoid_: Tenant (a NetEye-level concept; a realm is the Keycloak-level
mechanism a tenant may map to)

**Events**:
The realm's login and admin action event logging configuration (whether
logging is enabled, and how long entries are retained). Always enforced by
the operator with ansible-parity defaults; not opt-in.
_Avoid_: Audit log, auditing

**Brute Force Protection**:
The realm's account-lockout defense against repeated failed logins (failure
threshold, wait times, lockout duration). Always enforced by the operator
with ansible-parity defaults; not opt-in. Permanent lockout is never enabled.
_Avoid_: Security defenses, lockout policy

**Theme**:
The set of Keycloak themes (login, admin console, account console, email)
applied to a realm's pages and emails. Optional and opt-in: unlike Events and
Brute Force Protection, theme names are installation-specific branding, not
a Keycloak-side default worth enforcing.

### Install and upgrade

**Procedure Gate**:
A record that one side of a NetEye install or upgrade (the operator or the
NetEye Ansible procedure) has completed a step the other side waits for.
Declared by the `NetEyeProcedureGate` custom resource; its existence is the
signal. Each release lists, per component, the gates its install and its
upgrade require and produce. The gates of a tenant live in the tenant
namespace. Disabled with `spec.useProcedureGates: false` where no Ansible
procedure runs.
_Avoid_: Upgrade gate (gates apply to installs too), lock, flag

**Current Version**:
The NetEye release whose install or upgrade has completed, reported in
`status.currentVersion` of the `NetEye` resource and of every tenant. Empty
means the operator is installing; a value different from `spec.version` means
it is upgrading. The `NetEye` resource upgrades first, and only when every
tenant that completed a version is at the version that it leaves. A tenant
upgrade then waits until the `NetEye` resource has completed the new
version.
_Avoid_: Installed version (it is set only once every component is ready)
