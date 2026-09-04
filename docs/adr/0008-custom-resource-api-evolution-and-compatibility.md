# ADR-0008: Custom Resource API Evolution and Compatibility

- **Status:** Proposed
- **Date:** 2026-09-04

## Context

The `NetEye`, `NetEyeTenant`, and `NetEyeUpgrade` APIs are the supported
interfaces between users, lifecycle automation, and the NetEye Operator. Their
specifications and status will grow as more NetEye components move to
Kubernetes.

An operator update can be installed before a NetEye product upgrade is
authorized. As defined by ADR-0003, the new operator must continue managing the
immediately previous NetEye release while it waits for a `NetEyeUpgrade`.
Existing custom resources and automation must therefore remain usable when the
operator and its CRDs are updated.

Kubernetes stores custom resources in one selected storage version but may
serve several API versions. Adding a new served version does not by itself
rewrite already stored objects. Removing or changing a schema without a
conversion and storage-migration policy can make existing resources unreadable,
reject previously valid manifests, or lose fields during a version round trip.

The project needs rules for API maturity, compatible changes, conversion,
defaulting, validation, deprecation, and removal. These rules must preserve a
declarative API and must not turn Ansible or upgrade Jobs into custom-resource
conversion mechanisms.

## Decision

### API maturity and compatibility

The first public versions of the NetEye custom resources use `v1alpha1`. An API
is promoted to `v1beta1` after its shape and semantics have been exercised by
supported installation and upgrade workflows. It is promoted to `v1` only when
the project is prepared to maintain long-term compatibility.

API versions describe the custom-resource contract. They are independent of
NetEye product releases and operator versions. A NetEye product upgrade does
not require a new Kubernetes API version unless the API contract itself must
change incompatibly.

The alpha designation permits replacement by a new API version; it does not
permit silently changing the meaning of an already published field. Within
every served API version:

- existing field names and values retain their meaning;
- existing fields are not reused for a different purpose;
- previously valid values are not rejected by a narrower schema;
- backward-compatible optional fields and status information may be added;
- a new required field may be added only when a stable default makes existing
  objects and manifests valid;
- removing or renaming a field, changing its type, or materially changing its
  semantics requires a new API version.

An API change is compatible only when an old manifest retains its meaning and
an existing client can continue using the fields it understands. Compilation
compatibility of the Go types alone is not sufficient.

### Supported version window

Every operator line must serve, convert, validate, and reconcile the API
versions required by both NetEye releases in its support window: its target
release and the immediately previous supported upgrade source from ADR-0003.
Installing the new operator must not require lifecycle automation to rewrite
the existing custom resources before the operator can manage them.

When a replacement API version is introduced, the previous version remains
served for at least the supported forward-upgrade window. The exact number of
simultaneously served versions may vary, but an operator must accept manifests
using every non-removed version advertised by the immediately previous
supported operator line.

An API version can be removed only after all of the following are true:

1. Its replacement has been served through the required compatibility window.
2. The version has been marked deprecated and its replacement is documented.
3. All stored objects have been migrated to a retained storage version.
4. The removed version no longer appears in the CRD's
   `status.storedVersions`.
5. Supported lifecycle automation and maintained manifests no longer depend on
   it.

Deprecation uses the CRD version's deprecation flag and warning message in
addition to release documentation. A deprecated version remains functional
until it is removed.

### Schema, defaulting, and validation

Every served version uses a structural OpenAPI schema. The schema is the first
choice for types, required fields, enumerations, formats, ranges, and static
defaults.

Defaults are observable API semantics. They must be deterministic and must not
depend on cluster state. Once published in an API version, a default is not
changed when doing so would materially change the meaning of an omitted field.
The reconciler must handle old objects for which a newly introduced optional
field was never explicitly stored.

Validation is implemented at the least dynamic layer that can express the
rule:

1. OpenAPI schema validation for field shape and local constraints.
2. CRD validation rules using CEL for deterministic relationships between
   fields, including immutability where expressible.
3. A validating admission webhook only for rules that require live cluster
   state, coordination with another resource, or richer transition checks.

The singleton rule, active-upgrade exclusion, upgrade-transition validation,
and the specification lock are examples of rules that can require admission
logic. Controllers repeat safety-critical checks and report violations in
status because admission may be bypassed or temporarily unavailable.

Static defaulting does not use a mutating webhook. A mutating webhook may be
introduced only for a separately justified behavior that cannot be represented
by CRD defaulting and remains deterministic.

Unknown fields are pruned. `x-kubernetes-preserve-unknown-fields` is used only
for an explicitly designed opaque extension point whose ownership and
compatibility contract are documented. The public APIs do not expose a general
free-form escape hatch for arbitrary generated-resource specifications.

### Field mutability and ownership

Specifications contain desired state owned by their documented user or
automation actor. Status contains observed state owned by the operator. API
evolution must not move workflow input into status or make a controller write
user-owned specification fields.

Fields remain mutable unless their identity or lifecycle contract requires
otherwise. Immutability is explicit in the schema or validating webhook and is
documented as part of the API. In particular:

- `NetEyeUpgrade.spec.targetVersion` and its installation reference are
  immutable after creation;
- upgrade gate acknowledgements are append-only after acceptance;
- tenant technical identity follows the immutable namespace identity from
  ADR-0007;
- the temporary `NetEye.spec` lock and its degraded-recovery exception follow
  ADR-0006 and do not make those fields permanently immutable.

The operator rejects or reports unsupported transitions; it does not implement
them by deleting and recreating the primary custom resource.

### Conversion and storage

Each CRD has exactly one storage version. When more than one version is served,
the operator uses a conversion webhook and a single internal hub model. Every
served version converts to and from that model.

Conversion is deterministic and side-effect free. It performs no Kubernetes
lookups, network calls, migrations, or external actions. It must preserve all
information needed to round-trip an object between served versions. If a new
representation cannot coexist with an older served version without losing
information, the design must supply an explicit round-trip preservation
mechanism or defer the incompatible change until the older version can be
removed.

Conversion changes representation, not product state. Component migrations,
data migrations, and external Ansible work remain in the lifecycle and upgrade
graphs defined by ADR-0005 and ADR-0006. Ansible does not rewrite custom
resources merely to convert their API version.

Before an old storage version is removed, existing objects are rewritten
through a controlled storage migration and the CRD's `status.storedVersions`
is verified. Merely changing which version has `storage: true` is not a storage
migration.

### Status compatibility

Status evolves additively within a served API version. Existing condition
types, machine-readable reasons, component identifiers, and field meanings are
not repurposed.

The stable readiness contract from ADR-0002 remains the top-level `Ready`
condition together with `observedGeneration`. Clients must tolerate additional
conditions, component keys, status fields, and enum values unless the schema
explicitly defines a closed set.

Human-readable condition messages are not a compatibility interface. A client
must not parse them to make lifecycle decisions.

### Delivery and verification

Every operator bundle contains the complete CRDs and conversion configuration
required for its supported window. The conversion service must remain
available throughout an operator upgrade whenever stored or requested objects
need it.

Introducing or removing a served version is staged when bundle application
order cannot guarantee conversion availability. At every intermediate point,
the installed conversion webhook must understand every version advertised by
the installed CRD. This may require one operator release to deploy compatible
conversion code before a later release changes the CRD.

CI verifies at least:

- structural schemas and generated CRDs;
- compatibility of existing manifest fixtures with the new schemas;
- defaulting and validation behavior;
- conversion in both directions for every served version;
- semantic round trips through the hub representation;
- upgrade from objects stored by the immediately previous supported operator
  line;
- bundle validation and the planned `storedVersions` migration.

Every public field and condition has user-facing documentation. A compatibility
change is reviewed as an API change even when it requires no controller-code
change.

## Alternatives considered

### Change v1alpha1 in place until it becomes stable

Alpha APIs are expected to evolve, but changing the meaning or validation of an
existing served version would make stored objects and automation depend on the
order in which operator releases were installed. A new version makes an
incompatible contract explicit and convertible.

### Keep one API version and have Ansible rewrite resources

This would avoid a conversion webhook. It was not chosen because the operator
must manage the previous release immediately after its own installation, and
API representation changes belong to the Kubernetes API boundary rather than
external lifecycle scripts.

### Tie API versions to NetEye product releases

Versions such as one Kubernetes API version per NetEye release would make the
product and API lifecycles appear identical. Most product releases do not
require an incompatible API change, so this would create unnecessary versions
and misleading compatibility boundaries.

### Use admission webhooks for all validation and defaulting

This would provide one implementation language for every rule. It was not
chosen because schema and CEL validation are visible through the CRD, work
without an additional service call, and are sufficient for most deterministic
rules.

### Preserve arbitrary unknown fields

This would permit users to pass unsupported configuration through the custom
resources. It was not chosen because those fields would have undefined
ownership, validation, upgrade, and compatibility semantics.

### Keep every historical API version served indefinitely

This would avoid removal for old clients but would make conversion and testing
complexity grow without a bound. The supported upgrade window and explicit
deprecation process provide a finite compatibility obligation.

### Perform migrations during conversion

This would let conversion adapt external state while changing object shape. It
was not chosen because conversion can be invoked by ordinary reads and must be
fast, deterministic, and free of side effects.

## Consequences

Operator updates can safely precede product upgrades. Existing custom
resources remain manageable throughout the two-release support window, and
lifecycle automation is not coupled to internal API-storage changes.

Even alpha APIs require deliberate evolution. Incompatible experiments create
new versions and conversion work instead of silently changing persisted
contracts.

Compatible additions remain inexpensive, but field defaults and validation
rules require API-level review because they can change the meaning or validity
of existing manifests.

Serving several versions adds conversion code, webhook availability,
certificates, staged delivery, storage migration, and test fixtures. These
costs are accepted to provide deterministic upgrades and avoid data loss.

The API-version support window remains bounded by the operator support model in
ADR-0003. Removing a version still requires evidence that stored resources and
supported clients have moved away from it.

The custom resources remain typed interfaces rather than generic wrappers for
Helm values or Kubernetes objects. New supported configuration requires an
intentional schema addition.

## References

- [ADR-0001: NetEye Resource Scope and Ownership](0001-neteye-resource-scope-and-ownership.md)
- [ADR-0002: Reconciliation and Resource Application](0002-reconciliation-and-resource-application.md)
- [ADR-0003: NetEye and Operator Version Model](0003-neteye-and-operator-version-model.md)
- [ADR-0005: Component Lifecycle and Dependency Orchestration](0005-component-lifecycle-and-dependency-orchestration.md)
- [ADR-0006: NetEye Upgrade Coordination](0006-neteye-upgrade-coordination.md)
- [ADR-0007: Tenant Namespace and Isolation Model](0007-tenant-namespace-and-isolation-model.md)
- [Kubernetes CustomResourceDefinition versioning](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
- [Kubernetes validation rules](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#validation-rules)
- [Kubernetes API deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)
