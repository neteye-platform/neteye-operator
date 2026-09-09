# NetEye Operator

The NetEye Operator reconciles a singleton `NetEye` custom resource and its
delegated component resources (including the Keycloak identity service) into
a running NetEye installation on Kubernetes.

## Language

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
