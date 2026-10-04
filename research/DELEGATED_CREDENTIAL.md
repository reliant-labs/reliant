# Delegated automation credential: waking a cloud daemon for an unattended run

Status: design only, not implemented. Companion to `research/TRIGGERS.md`
("Identity" settled facts).

## 1. The problem

A scheduled fire runs on the reliant worker with no user request, so there is
no user JWT. Three call sites need the user's identity toward control-plane:

| # | Site | What it does today |
|---|------|--------------------|
| A | `internal/mcpserver/resumer.go:62-83` `ControlPlaneResumer.ResumeDaemon` | Forwards `CallerToken(ctx)` (the user's OAuth token). Errors when it is empty. |
| B | `internal/toolexec/daemon_router_nats.go:288-296` `resolveViaControlPlane` | Sends `auth.GetUserJWT(userID)` as Bearer. With no JWT it sends no header, gets a 401, and resolution contributes nothing. |
| B' | `internal/toolexec/daemon_router_nats.go:331` (same function) | Calls `ResumeDaemon` **with no Authorization header at all**. This is a latent bug even in attended runs. It can only work if the client carries an interceptor that I did not find. Verify it. |
| C | `internal/workflow/runtime/activities/handlers/preflight_daemon.go:~95` | Wakes by sending a fake `__preflight_ping` through `SendToolRequestSyncWithSelector`, which runs B and B'. |

On the control-plane side:

- `ResumeDaemon` (`control-plane/internal/svcdaemon/service.go:765`) and
  `ResolveDaemonEndpoint` (`service.go:865`) both authorize through
  `auth.GetOwner(ctx)` and then `lookupOwnedDaemon` (`service.go:404`).
- The reliant.v1 adapter (`internal/daemonregistry/adapter.go:77-105`) only
  rewrites the path and re-dispatches through the mux. The inner Connect
  handlers' auth interceptor sees the original Bearer.

## 2. Key finding: `GetOwner` does NOT work for an `rlat_` today

`pkg/middleware/auth.go:223-233` `decorateContext` handles the two kinds of
caller differently:

- **Machine principal** (any `rlat_`, validated by
  `internal/auth/deploy_token_validator.go`): it calls
  `WithMachinePrincipal` and returns early. It never calls `WithAuthFull`, so
  `ownerIDKey` is unset and `auth.GetOwner` returns `ok=false`. ResumeDaemon
  and ResolveDaemonEndpoint therefore answer `Unauthenticated`.
- **Human (JWT)**: it calls `WithAuthFull(..., claims.UserID, "user")`.

This is deliberate. `internal/auth/machine.go:1-37` explains that the empty
user identity keeps machine credentials out of every human-only handler
structurally. Surfaces opt in explicitly by reading
`GetMachinePrincipal().ActingUserID` and checking a scope. The precedent is
`internal/llmproxy/proxy.go:291` and `:387`: it checks `llm:invoke`, then
installs `WithAuthFull(userID, …, userID, "user")` for that one request.

**So the design must NOT widen `decorateContext`.** Instead, the two daemon
handlers opt in.

## 3. Recommendation

1. **New scope `daemon:resume`** in forge's closed set. It requires an acting
   user and is optionally bindable to a daemon (rule: `ResourceDaemon`, not
   required). Authority: resolve the acting user's daemons and wake the
   suspended ones. Nothing else: no create, delete, suspend, list-all or
   connect.
2. **One token per user**, not one per trigger (reasons in §6). Name it with
   a fixed device name, e.g. `controlplane.AutomationKeyName = "reliant-automation"`,
   and mint it with `Rotate: true` exactly like the LLM key.
3. **Mint** through the existing user-JWT path, `AccessTokenService.CreateMyToken`
   (`reliant/internal/controlplane/client.go:183` `MintLLMKey` is the
   template). This happens while the user is signed in:
   - when the first enabled trigger is created or enabled (TriggerService
     handler; it has the JWT in `auth.GetUserJWT`), and
   - opportunistically in `SyncReliantProvider`
     (`internal/grpc/services/settings.go:1625`), which already runs at sign-in
     and rotates the LLM key. Rotating both together keeps them fresh.
4. **Store** it as a provider-style secret beside the LLM key: a row in
   `api_keys` (`internal/db/postgres/settings_store.go:142`) under a reserved
   provider name such as `reliant-automation`. **Caveat:** `api_keys.api_key`
   is plaintext at rest today (`settings_store.go:145`; no encryption found).
   That matches the LLM key's existing exposure. It is not made worse, but it
   is not good either (see §7).
5. **Accept in control-plane** with a per-handler opt-in, modelled on
   llmproxy. See §5.
6. **Wire in reliant by looking the token up at call time.** Do not thread it
   through Temporal (§8). The single seam is a credential resolver used by
   B, B' and A: user JWT if present, otherwise the stored automation token.

## 4. Scope changes (§Q3)

| Repo | Change | Migration |
|------|--------|-----------|
| forge | `pkg/accesstoken/scope.go`: add `ScopeDaemonResume Scope = "daemon:resume"` to the consts and to `AllScopes`. `grant.go`: `scopeResourceRules[ScopeDaemonResume] = {kind: ResourceDaemon, required: false}`, plus `RequiresActingUser` → true. Consider whether `bindableToDaemon` (`grant.go:96`) needs it. | none (library) |
| control-plane | `internal/accesstoken/scope.go`: alias `ScopeDaemonResume`. `internal/handlers/access_token/service.go:644` `accesstokenRequiresUser`: add it. Check whether `CreateMyToken` restricts self-mintable scopes or org grants (`service.go:~620-640` `missing` check against org member grants, migration 00113). `daemon:resume` must be self-mintable by any member, as `llm:invoke` is. | New roll-forward migration: drop and re-add `ck_cp_access_tokens_scopes` with the full current list plus `'daemon:resume'`. Copy the list from the latest migration that rewrote it (00085 started it; later migrations added cluster/domain scopes; `scope_test.go` reads the file to pin it). No down file. |
| reliant | `internal/db/migrations/postgres/<goose ts>_access_tokens_daemon_resume_scope.sql` (via `make migration`): `ALTER TABLE access_tokens DROP CONSTRAINT access_tokens_scopes, ADD CONSTRAINT access_tokens_scopes CHECK (... + 'daemon:resume')`. **Note:** reliant's CHECK (`20260925000000_access_tokens_retire_pats.sql:56-64`) already lags forge. It lacks `cluster:manage`, `domain:read` and `domain:write`. Decide whether to add those too, or keep reliant's set deliberately narrower and adjust its pinning test. Regenerate `schema.sql` and sqlc. Bump `github.com/reliant-labs/forge` in `go.mod`, which is currently **v0.1.23**; control-plane is on v0.1.42. | yes |

## 5. Accept in control-plane (§Q2)

The validator chain (`internal/auth/zitadel.go:226` `NewValidatorChain`, with
`NewMachineTokenValidator`) already authenticates any live `rlat_`, so no
validator change is needed. The opt-in belongs in the daemon service, as one
helper:

```go
// internal/svcdaemon (or internal/auth): ownerForDaemonAutomation
func ownerFor(ctx, want accesstoken.Scope) (ownerID, ownerType string, err error) {
    if id, typ, ok := auth.GetOwner(ctx); ok { return id, typ, nil } // human
    p, ok := auth.GetMachinePrincipal(ctx)
    if !ok { return Unauthenticated }
    scopes, err := accesstoken.NewSet(p.Scopes)        // fail closed on unknown
    if err != nil || !scopes.Permits(want) || p.ActingUserID == "" { return PermissionDenied }
    return p.ActingUserID, "user", nil
}
```

- `ResumeDaemon` (`service.go:765`) and `ResolveDaemonEndpoint`
  (`service.go:865`) replace `auth.GetOwner` with `ownerFor(ctx, ScopeDaemonResume)`.
- If the principal has a daemon resource binding, also require
  `ResourceID == req.DaemonId`. For an unbound per-user token, ownership
  scoping comes from `lookupOwnedDaemon(ownerID="user")`, so the token can
  only touch daemons the acting user owns.
- **Gap to verify:** ownerType. Daemons can be org-owned. A JWT caller's
  `GetOwner` always yields `ownerType "user"` (`pkg/middleware/auth.go:230`),
  so this matches today's human behavior. Confirm org-owned workspace resume
  is not expected.
- Do **not** opt in `GetDaemon`, `ListDaemons`, `SuspendDaemon`, `CreateDaemon`
  or `DeleteDaemon`. The adapter routes `ListDaemons` and `GetDaemon` too. With
  the token those keep 401-ing, which is correct.
- Funding and limits: ResumeDaemon's `checkWorkspaceLimit` and funding
  re-check stay in force unchanged, because they key on `ownerID`. An
  unattended run cannot wake a workspace the user can no longer pay for.
- `RecordMachineTokenUse` (`deploy_token_validator.go:214`) gives
  last-used attribution for free.
- Any rate-limit or authz interceptor keyed on procedure must not reject
  machine principals for these two procedures. I did not find a per-procedure
  machine allowlist (`rg` on `pkg/middleware` and `internal/auth`), but confirm
  with an integration test.
- Audit: log `token_id` on resume when the caller is a machine.

## 6. Revocation; per user vs per trigger (§Q4)

| Event | Per-user token | Per-trigger token |
|-------|----------------|-------------------|
| Trigger disabled/deleted | Revoke when the user has no remaining enabled trigger (`RevokeForUser`, or `ListForUser(scope=daemon:resume)` then revoke). Otherwise keep it; the trigger simply stops firing. | Revoke that token. |
| User deleted | `acting_user_id … ON DELETE CASCADE` (00085) removes it in control-plane. Reliant's `api_keys` row goes with the user. | same |
| Access lapses (subscription/coupon) | Token still authenticates, but ResumeDaemon's funding check refuses. That is the right layer. | same |
| Removed from org | Owner is user-scoped, so membership is not consulted. Same as a JWT caller today. | same |
| Leak | One revoke kills automation for that user. Re-mint on next sign-in. | Smaller blast radius per token, but every token carries the same authority (all of the user's daemons), so the radius is the same unless each is daemon-bound. |

**Recommend per user.**

- A trigger does not own a daemon: its selector resolves at fire time, so
  per-trigger binding buys no narrower authority.
- N triggers would mean N secrets at rest and N mints, each needing a live
  JWT. A trigger edited by an API/connector caller with no JWT could not be
  minted at all.
- It mirrors the LLM key exactly: one `Rotate: true` named device token per
  user. The UX ("Automation access: active/revoked") stays simple.

Revisit if triggers gain an explicit pinned daemon. At that point a
daemon-bound per-trigger token becomes strictly narrower.

## 7. Security analysis

- **Authority:** `daemon:resume` resolves the acting user's daemons and wakes
  suspended ones, nothing more. It cannot connect as a daemon (that is
  `daemon:connect`), run tools, read secrets, or mint (no `token:write`). A
  leaked token costs the user compute at most, and only within their funding.
  It is narrower than the `llm:invoke` key already stored beside it, which can
  spend money directly.
- **At rest:** `api_keys.api_key` is plaintext. That is an existing weakness
  shared with the LLM key and BYO provider keys. Recommend a separate
  follow-up: envelope-encrypt `api_keys` (KMS/secret-provider key). Do not
  block on it; this token is the least dangerous item in that table.
- **In transit through Temporal:** avoided. The recommended design never
  places the token in workflow inputs. `internal/temporal/data_converter.go`
  installs only a claim-check codec (size offload, `:63-79`), not encryption,
  so anything in history is plaintext in Temporal's DB. Note that `UserJWT`
  is already in history today (`runtime/context.go:67`). That is a
  pre-existing exposure, mitigated only by the JWT's short life. A long-lived
  `rlat_` must not follow it.
- **Process-global cache:** do not push the token into `auth.SetUserJWT`'s
  map. That map is "latest JWT per user" and is read by unrelated code (LLM
  driver, analytics). Mixing in an `rlat_` would hand a machine credential to
  callers expecting a JWT.
- **Fail-closed:** an older control-plane given an unknown scope rejects the
  token at `NewSet`. That is the correct direction during a skewed rollout.

## 8. Wiring in reliant (§Q5)

Options:

| Option | Pro | Con |
|--------|-----|-----|
| (a) Thread a `DelegatedToken` field on `ExecContext` → `RuntimeContext`, like `UserJWT` (`step_executor.go:1088`, `call_llm.go:218`) | Mirrors existing flow | Plaintext long-lived secret in Temporal history. Stale after rotation (an in-flight run holds a revoked token). Needs plumbing through launcher, `launch/contract.go:78` and every activity input. |
| (b) **Look up by user at call time** | Never in history. Always the current token. One seam. | One DB read per resolve (cheap; can be cached briefly in-process) |

**Choose (b).** Concretely:

- New narrow interface in `internal/toolexec` (consumer-side):
  `type ControlPlaneCredentials interface { BearerFor(ctx, userID string) (string, error) }`.
- Implementation in a small package (e.g. `internal/automationcred`):
  1. If `auth.GetUserJWT(userID)` is present, return it (attended runs are
     unchanged and act as the human).
  2. Otherwise read the `api_keys` row (`provider="reliant-automation"`).
  3. Otherwise return `""`. The caller produces a typed error:
     "automation access not granted, sign in to re-enable".
- `daemon_router_nats.go:294`: replace the direct `GetUserJWT` with
  `BearerFor`. Also attach the same header at `:331` (fixes B').
- `mcpserver/resumer.go`: fall back to `BearerFor(userID)` when
  `CallerToken(ctx)` is empty. `userID` is already a parameter (currently
  `_ = userID`). Keep the connector-credential refusal for MCP callers. The
  fallback should apply only when the caller context is a trigger/worker, not
  a third-party connector. A connector should not borrow the user's
  automation token without an explicit decision.
- `preflight_daemon.go`: no change. It runs through the router.
  Optionally replace the `__preflight_ping` hack later with an explicit
  `router.EnsureAwake(userID, selector)`.
- Construction: wire the resolver into `NATSDaemonRouter` in workersetup
  next to `controlPlaneClient`.

## 9. Self-hosted mode (§Q6)

`tokenauthority.New` picks `ModeLocal` when there is no control-plane
(`authority.go:50-65`). Then `NewControlPlaneResumer("")` returns nil, and the
router has no `controlPlaneClient`. There are no managed/suspendable
workspaces: daemons are user-run and either connected or not. So:

- Do not mint. Make the mint step a no-op when no control-plane is
  configured. The reliant CHECK migration is still needed, so that forge's
  `AllScopes` pin test passes and LocalStore can represent the scope.
- An unattended run needing an offline local daemon fails with the existing
  preflight message. That is correct behavior.

## 10. Rollout order

1. **forge**: add the scope and its grant rule, with tests. Tag a release
   (v0.1.4x).
2. **control-plane**: bump forge, then add the CHECK migration, the alias, the
   `ownerFor` opt-in on the two handlers, and `accesstokenRequiresUser`.
   Deploy. Old reliant is unaffected.
3. **reliant**: bump forge from v0.1.23, which skips many versions, so expect
   API drift and budget for it. Then add the CHECK migration, mint on trigger
   enable and in `SyncReliantProvider`, the credential resolver, and router
   and resumer use. If reliant ships first, `CreateMyToken` rejects the unknown
   scope. Handle that by logging and leaving the trigger LLM-only, not by
   failing trigger create.

## 11. Test plan (§Q7)

- **forge:**
  - `accesstoken_test`: `daemon:resume` parses.
  - `Grant.Validate` rejects it without an acting user, accepts the daemon
    binding, and rejects a port binding.
  - `Covers` does not let `daemon:connect` mint `daemon:resume`.
- **control-plane:**
  - `scope_test` pins the CHECK (it fails until the migration lands; confirm
    it fails first).
  - Unit tests in svcdaemon, against a mock repo, for ResumeDaemon and
    ResolveDaemonEndpoint:
    - JWT path unchanged.
    - `rlat_` with `daemon:resume` resolves and resumes the acting user's
      daemon.
    - Another user's daemon gives NotFound.
    - Missing scope (`llm:invoke` only) gives PermissionDenied.
    - Daemon-bound token with a different daemon id gives PermissionDenied.
    - Funding lapsed gives the existing refusal.
  - Integration test through the real mux plus the `daemonregistry` adapter,
    with a real minted token, proving the interceptor chain passes it.
  - `ListDaemons`, `SuspendDaemon` and `CreateDaemon` with the token still
    refuse.
  - `CreateMyToken` with `daemon:resume` succeeds for a plain member.
- **reliant:**
  - Resolver unit tests: JWT beats stored token, then the stored token, then
    empty.
  - Router test with a fake Connect server asserting the Authorization header
    on both ResolveDaemon **and ResumeDaemon** (the B' regression).
  - Resumer fallback test.
  - Settings/Trigger handler test: mint called with
    `Scopes=["daemon:resume"]`, `Rotate:true`, and stored. No mint in
    self-hosted.
  - Migration test: the CHECK accepts the scope.
  - E2E: a scheduled fire with no JWT and a suspended managed daemon. The
    preflight wakes it and the tool runs.

## 12. Alternatives rejected

- **Service credential** (`INTERNAL_SERVICE_SECRET`) with a userID in the
  body. The resumer header comment explicitly rejects this: one leaked
  secret could wake anyone's workspace, and the body userID is an unverified
  claim.
- **Reuse the `llm:invoke` key** for resume. That widens a credential the
  gateway hands around, and breaks "scopes are the only thing that
  distinguishes credentials".
- **Reuse `reliant:api`.** Far too broad: it is full API-as-user.
- **Widen `decorateContext`** to set owner for every acting-user `rlat_`.
  That undoes the structural exclusion in one line (`auth.go:218-222`).
- **Store a refresh token** and mint JWTs on demand. That puts IdP
  refresh-token custody in reliant: broader authority (everything the user can
  do), and it is IdP-specific.

## 13. Could not determine / open questions

1. ~~How B' succeeds today~~ **Resolved: it did not.** The router's client is
   a plain `http.DefaultClient` with no interceptors, so every router-driven
   resume was rejected as unauthenticated. This is fixed on `triggers` in
   commit 0e8204c3: the resume reuses the resolve call's Bearer. The
   regression test is `internal/toolexec/daemon_router_resume_auth_test.go`,
   and it was confirmed to fail before the fix. The credential resolver in §8
   must keep both calls on the same Bearer.
2. ~~Whether `CreateMyToken` gates scopes on org member grants.~~ **Resolved:**
   `requireHeld` (`control-plane/internal/handlers/access_token/service.go:607`)
   gates only the scopes listed in `orggrants.OrgScopes`. If `daemon:resume`
   stays out of `OrgScopes`, any member can mint it for themselves, the same
   as `llm:invoke`. That is the intended behavior, because the token only
   wakes the member's OWN daemons. Keep it out of `OrgScopes` deliberately,
   and add a test pinning that.
3. The exact current scope list in control-plane's latest CHECK. I saw 00085's
   partial list. Later migrations (cluster and domain scopes) must be read
   before writing the new one.
4. Whether org-owned daemons can be resumed by an acting user (ownerType).
5. Whether the reliant ↔ control-plane user-id mapping holds for
   `acting_user_id`. Reliant ids are the IdP subject, while control-plane's
   `users.id` is an internal UUID. `MintLLMKey` works today, so CreateMyToken
   must resolve it, but `ActingUserID` in the principal is control-plane's
   id, which is what `lookupOwnedDaemon` wants. Confirm with a test.
6. The size of the forge v0.1.23 → current bump in reliant.
7. Whether the MCP resumer (A) should ever use the automation token for
   connector callers. This is proposed as no, which needs a product decision.
