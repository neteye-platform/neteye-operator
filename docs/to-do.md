# To-do — CodeRabbit review follow-up

Review of the open CodeRabbit/gitar-bot comments on branch
`NEOP-6-Handle-User-crd`. Each item was checked against the current code.

## To fix

### 1. `GenerationChangedPredicate` blocks delete on all reconcilers

- **File:** `src/controllers/keycloakclient_controller.go:169-175`,
  `src/controllers/keycloakuser_controller.go:257-263`,
  `src/controllers/neteye_controller.go:445-448`
- **Problem:** `SetupWithManager` filters events with a bare
  `predicate.GenerationChangedPredicate{}`. Kubernetes only bumps
  `.metadata.generation` on `.spec` changes: adding a finalizer or setting
  `deletionTimestamp` is a metadata-only update and does not touch
  `generation`. The in-code comment ("Setting a deletion timestamp bumps
  the generation") is wrong.
- **Effect:** on CR deletion, the Update event carrying the
  `deletionTimestamp` is dropped by the predicate, so `reconcileDelete`
  never runs via watch, the finalizer is never removed, and the resource
  stays stuck in `Terminating`.
- **Fix:** replace the predicate with
  `predicate.Or(predicate.GenerationChangedPredicate{},
  predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool { return
  !e.ObjectNew.GetDeletionTimestamp().IsZero() }})` (or drop the predicate
  entirely) on all three controllers, and fix the misleading comment.
- **Priority:** high — real, reproducible bug on every CR managed by the
  operator.

### 2. Username not normalized causes a PUT on every reconcile (KeycloakUser)

- **File:** `src/internal/keycloak/usersync.go:52,65-68,128`;
  `src/internal/keycloak/clientsync.go:155-170` (`mergeRepresentation`,
  shared)
- **Problem:** `desiredUserRepresentation` writes `spec.Username` verbatim;
  `mergeRepresentation` always overwrites `username` in the merged state
  with the desired value, even though Keycloak normalizes (lowercases) the
  stored username. The subsequent `reflect.DeepEqual` then always detects
  drift for a user adopted with a different-case username, and
  `UpdateUser` fires on every reconcile in a loop (Keycloak re-normalizes
  it, the drift reappears).
- **Fix:** compare usernames case-insensitively before the drift check, or
  keep `stringValue(live, "username")` instead of overwriting it when the
  two values match case-insensitively.
- **Priority:** medium — doesn't break functionality but causes constant
  reconcile/PUT churn and log noise for adopted users.

### 3. `desiredClientRepresentation` never clears removed fields (KeycloakClient)

- **File:** `src/internal/keycloak/clientsync.go:125-139`
  (`desiredClientRepresentation`), `mergeRepresentation`
- **Problem:** `name`, `description`, `rootUrl`, `redirectUris`,
  `webOrigins` are only written into `desired` when non-empty/non-nil.
  `mergeRepresentation` starts from `live` and only overwrites the keys
  present in `desired`: if a key is missing, the live value stays
  unchanged. So if a user sets `spec.RedirectUris`, reconciles, then
  removes the field from the spec, the old value stays on Keycloak forever
  (e.g. stale redirect URIs or webOrigins remain in the allowlist).
- **Note:** unlike the documented "nil = not managed here" contract for
  protocol mappers/client scopes, these scalar fields are meant to always
  be managed, so this is a real inconsistency, not intended behavior.
- **Fix:** always populate these keys in `desired` (using empty
  string/empty slice as the zero value) so the merge can actually clear
  them.
- **Priority:** medium-high — loss of control over security configuration
  (redirect URI / CORS).

### 4. `spec.ClientID`/`spec.Realm` not immutable, orphans remote client (KeycloakClient)

- **File:** `src/api/v1alpha1/keycloakclient_types.go` (fields `Realm`
  around line 58, `ClientID` around line 64, no
  `+kubebuilder:validation:XValidation` immutability marker);
  `src/internal/keycloak/clientsync.go:44` (`ReconcileClient`), delete/
  finalizer logic in the controller
- **Problem:** no CEL validation or webhook prevents changing
  `clientId`/`realm` after creation. `ReconcileClient` looks up the remote
  client using the spec's *current* identity: if it changed, nothing is
  found, a new client is created, and `status.clientUUID` is overwritten
  with the new UUID, losing the reference to the old one. On deletion, the
  finalizer only looks up and deletes the client with the current
  identity: the original client stays orphaned on Keycloak forever.
- **Fix:** add CEL immutability validation on `clientId`/`realm`, or
  explicitly handle the identity change (delete the old client before
  creating the new one).
- **Priority:** medium — less common scenario (requires a manual spec
  edit) but leads to silent orphaned resources in Keycloak.

### 5. `ProtocolMappers` accepts duplicate names, causing creation failure (KeycloakClient)

- **File:** `src/api/v1alpha1/keycloakclient_types.go:127-131`
  (`ProtocolMappers` field, only `+kubebuilder:validation:Optional`);
  `src/internal/keycloak/clientsync.go:185-223`
  (`reconcileProtocolMappers`)
- **Problem:** without `+listType=map`/`+listMapKey=name`, the API server
  doesn't reject duplicate names in the list. `reconcileProtocolMappers`
  indexes the live mappers by name once at startup but doesn't refresh it
  after each create: if the spec has two mappers with the same name, both
  come up as "not found" and get created twice, and the second call fails
  because Keycloak requires unique names.
- **Fix:** add `+listType=map` and `+listMapKey=name` on the field
  (admission-level rejection, cheaper fix) and/or refresh the
  `liveByName` map after each create as an extra safeguard. Regenerate the
  CRD manifests.
- **Priority:** low — quick win, edge case (requires a malformed spec from
  the user).

### 6. RBAC: missing marker for `secrets` in `keycloakclient_controller.go`

- **File:** `src/controllers/keycloakclient_controller.go:54-56`
  (existing markers), Secret read around line 147
- **Problem:** the controller reads a Secret (client secret) via `r.Get`
  but doesn't declare
  `+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch`. It
  only works because `neteye_controller.go:66` already declares that
  permission and controller-gen aggregates the markers into a single
  ClusterRole. `keycloakuser_controller.go`, on the other hand, declares
  its own `secrets` marker independently — an inconsistency between
  "sibling" controllers.
- **Fix:** add the missing marker alongside the others in
  `keycloakclient_controller.go`.
- **Priority:** low — quick win, not an active bug, but makes the
  permission self-documented and resilient to future changes in
  `neteye_controller.go`.

## To track (not a code bug, but known work)

### 7. Admin bootstrap password sent over plaintext HTTP in-cluster

- **File:** `src/internal/keycloak/admin.go:64-66,77-89`
- **Problem:** `InClusterBaseURL` builds a `http://...` URL; the bootstrap
  admin credentials travel unencrypted over the pod network on every
  reconcile. The NetworkPolicy restricts which namespace can reach
  Keycloak, but doesn't protect against node/CNI-level capture. No ADR or
  doc in the repo yet documents a migration plan.
- **Note:** consistent with the direction already decided (see memory:
  the operator should use a `KeycloakClient` with a service account
  instead of the bootstrap admin — [[operator-admin-service-account]]),
  but not yet written up as an ADR/issue.
- **Suggested action:** open an ADR/issue for the migration to a service
  account, which would also fix this as a side effect.
- **Priority:** medium — security hardening, not urgent but shouldn't be
  lost.

## False positives (verified, no action needed)

- **"KeycloakClient reconciler shadows shared adminAPI, breaks after
  bootstrap disable"** — `adminAPI`/`adminProvider` are defined once on
  `KeycloakAPIReconciler` and inherited via embedding by both
  `KeycloakClientReconciler` and `KeycloakUserReconciler`, with the same
  provider instance injected in `main.go`. No duplication/shadowing.
- **"ElasticStack phase can mask a failed Keycloak phase"** — in
  `neteye_controller.go:139-151`, `keycloakErr` is checked and returned
  before the `elasticErr` check; `combineResults` only merges
  `RequeueAfter`, it doesn't touch errors. No masking.
- **"Adopting an existing account with Generate=true fails permanently"**
  — already fixed by commit `3f0a78b`. The current code
  (`keycloakuser_controller.go:110-134`) explicitly handles the
  adoption + `Generate=true` (no rotate) case: it logs and proceeds to
  `Ready`, it doesn't fail.
- **"Setting defaultClientScopes removes Keycloak's auto-assigned
  scopes"** — `reconcileClientScopes` (`clientsync.go:242-273`) is purely
  additive: it adds missing scopes but never removes undeclared ones (no
  `RemoveClientScope` call anywhere in the codebase). The in-code comment
  states this explicitly.
- **"CSV OLM is missing RBAC rules for KeycloakClient"** —
  `neteye-operator.v0.1.0-alpha6.clusterserviceversion.yaml` already
  contains `keycloakclients` (line 255), `keycloakclients/status` (line
  266) and `keycloakclients/finalizers` (line 274) in
  `clusterPermissions`. The comment is inaccurate.

## Duplicates (already covered above, same comment from different bots)

- "Setting a deletion timestamp bumps the generation" / "Process
  deletion-timestamp updates" → see item 1.
- "Keycloak admin password sent over plaintext HTTP in-cluster" / "Use
  HTTPS for the credential-bearing Admin API path" → see item 7 ("to
  track" section). Extra note from the second bot: the default HTTP
  client follows redirects — if HTTPS is introduced, redirects to
  unexpected hosts/schemes must also be blocked.
- "Missing kubebuilder RBAC marker for secrets" → see item 6.
- "Setting defaultClientScopes removes Keycloak's auto-assigned scopes" →
  false positive, see above.
