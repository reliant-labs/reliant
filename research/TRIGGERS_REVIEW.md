# Review: triggers / launch / StartChat / spawn_stop

**Reviewed:** the `triggers` branch work. It was uncommitted when the review
started, and partway through it landed as commit `2779b758` ("feat: triggers
(schedule) + StartChat as the one launch door + spawn_stop"). Two later commits
(`0e8204c3`, `56c20313`) touch no in-scope file. `git diff 2779b758 HEAD` over
every reviewed path is empty, so everything below applies to `HEAD` as it stands.

**Verdict: REQUEST CHANGES.** Risk: HIGH.

- Two blockers:
  - With the default overlap policy, one transient failure wedges a schedule
    trigger for good.
  - A branched chat's first send fails, because `BranchChat` returns
    `WORKFLOW_STATE_UNSPECIFIED`.
- Four majors:
  - A second start into a half-launched or concurrently-started chat silently
    drops the caller's message and workflow switch.
  - The first send on a branch wipes the branch's visible history in the web
    client.
  - A stopped spawn can never be resumed in the same run.
  - The `SyncAll` orphan sweep can delete the schedule of a trigger created
    while it runs.
- The core idempotency design holds once these are fixed: the event row in the
  session transaction, `USE_EXISTING`, the pending→active CAS, and deterministic
  ids.

---

## How this was verified

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet` on launch, triggers, temporal, threadcancel, llm/tools, cmd/reliant/commands; `-tags e2e ./e2e/stories`; `-tags replayfixtures ./internal/workflow/runtime/replaytest` | clean |
| `go test` (test DB `:55433`) on `./internal/launch/... ./internal/triggers/... ./internal/temporal/... ./internal/workflow/threadcancel/... ./cmd/reliant/commands/ ./internal/grpc/services/ ./internal/db/ ./internal/llm/tools/` | all `ok` |
| Temporal dev-server tests | `TestScheduleFiresThroughToTheLauncher`, `TestManualFireReachesTheLauncher`, `TestTriggerFiresEndToEnd` and `TestManualFireEndToEnd` all PASS (they ran, not skipped). `TestCronTriggerFiresEndToEnd` SKIPs without `REQUIRE_CRON_E2E`. |
| `web`: `tsc --noEmit` | exit 0 |
| `web`: vitest on the 7 changed or added test files | 38/38 pass |
| sqlc drift (`sqlc diff`) | none apart from the version header (committed with v1.31.1, local v1.30.0) |
| `tools.md`, `cli.md`, tool catalog generators | no drift |
| Proto regeneration | **Not verified.** BSR returned `resource_exhausted: too many requests`. I spot-checked that `StartChatRequest.chat_id` (field 17) is present in `gen/` and `web/src/gen`, and that `TriggerService` stubs exist. |
| View migration body vs previous definition (`20260928193449`) | identical except the two appended columns and the `LEFT JOIN` |

I wrote three reproductions as `go test -overlay` files in a temp dir; none were
written into the worktree. Each **fails on the current code** (output quoted
under each finding). Findings marked *code-read* were not reproduced.

---

## BLOCKERS

### B1. With overlap SKIP (the default), a half-launched fire skips its own retry and wedges the trigger permanently

- **Severity:** blocker. **Reproduced.**
- **Where:**
  - `internal/triggers/fire.go:74-82`: overlap runs before anything looks at
    this fire's own dedupe key.
  - `fire.go:136-171`: PENDING counts as live (`:167`).
  - `fire.go:276-283`: a conflicting insert is reported as success.
  - `internal/triggers/fire_test.go:216` asserts PENDING blocks, so the test
    encodes the bug.

**What's wrong:** `Launch` commits the event row and the PENDING chat, then calls
Temporal. If that call fails, or the worker dies between commit and start, the
activity retries. The retry calls `overlapSkip` first. The latest `launched`
event is the fire's **own** row, and its chat is PENDING, which `Live()` counts
as live. So the retry records `skipped`, which conflicts on its own dedupe key
and is silently ignored, and returns success. The launcher's resumability path
(`finishExisting`) is never reached.

From then on every scheduled fire sees that PENDING chat and is skipped. The
chat never runs, and the trigger produces nothing until someone deletes the
stuck chat (`chat_id` is `SET NULL`) or flips overlap to `allow`.

The launcher's CAS made "pending" mean "never started". For a launched event,
that now means "half-launched", which is exactly the case overlap must not treat
as a running predecessor.

**Repro** (real launcher, real DB, starter fails until "Temporal recovers"):
```
fire#1 attempt 1 err=launch trigger …: failed to start workflow: temporal unavailable
fire#1 attempt 2 err=<nil>          ← retry SKIPPED itself
fire#1 attempt 3..5 err=<nil>
fire#2 out=&{Outcome:skipped Reason:previous run is pending (chat 1eada50c-…) …}
WEDGED: fire#2 skipped because fire#1's never-started chat is PENDING
```

**Fix:**
1. Make the fire's own identity decide first. At the top of `Fire`, call
   `GetTriggerEventByDedupe(schedule, req.FireWorkflowID)`.
   - If a `launched` row exists, skip the enabled and overlap checks and call
     `Launch`, so `finishExisting` resumes the start.
   - If a `skipped` or `failed` row exists, return that recorded outcome.
2. Overlap must not block on a predecessor whose root is PENDING. Either treat
   PENDING as not-live for overlap, or better, finish that stranded launch.
3. Fix the test at `fire_test.go:216` and add the repro above as a regression
   test.

Also see M3: on conflict, `recordOutcome` returns an `EventID` that was never
written.

### B2. `BranchChat` returns `WORKFLOW_STATE_UNSPECIFIED`, so the web sends a branch's first message to SendMessage, which now rejects it

- **Severity:** blocker. **Reproduced** server-side; the web half is from code
  reading.
- **Where:**
  - `internal/grpc/services/chat_branch.go:186-200` builds `branchChat` in
    memory, and `:306` calls `chatToProto(branchChat)`. `RootStatus` is the zero
    value, because only the store fills it from the view.
  - Web: `chatStore.ts:3455` (`seedChatDetail(newChat)`) → `ChatContainer.tsx:90`
    (`useChat`) → `ChatContainer.tsx:184` (`sendOnExistingChat`).
  - `lib/query-client.ts:8` (`staleTime: 30_000`).

**What's wrong:** TRIGGERS_C2.md requires "Ensure BranchChat's response carries
PENDING". It does not.

The web seeds the branch response into the React Query detail cache. That entry
is fresh for 30s, and nothing invalidates chat detail on activity events. So
`chatNeedsStart(currentChat)` returns false and the first send goes to
`sendMessage`. The server then returns
`FailedPrecondition: chat has not started; call StartChat`.

The previous flow (SendMessage's pending path) worked. This is a regression in
the primary branch flow: branch, then type straight away.

**Repro:**
```
BranchChat response workflow_state = WORKFLOW_STATE_UNSPECIFIED
GetChat(branch) workflow_state      = WORKFLOW_STATE_PENDING
```

**Fix:**
- Set `branchChat.RootStatus = db.Pending()` before `chatToProto`, or re-read
  the chat after commit.
- Add a test asserting the response carries PENDING. None of the BranchChat
  tests check `WorkflowState`.
- Defence in depth: on that specific FailedPrecondition, the web client could
  retry once via `startExistingChat`. A typed error detail would be better than
  matching on the message text.

---

## MAJORS

### M-A. A start into an already-recorded pending chat silently drops the caller's message, workflow switch and presets, and runs a workflow the chat row disagrees with

- **Severity:** major. **Reproduced.**
- **Where:** `internal/launch/launcher.go`:
  - `:440-455`: `created=false` returns before the switch, preset update and
    message save.
  - `:405-408` and `:430`: `workflowName` and `initialData` come from the new
    spec.
  - `:480-488`: `start` runs with them.

**What's wrong:** `launchPending` treats `created=false` as "an exact retry of
myself". It reuses the committed messages and skips every write, but it still
builds inputs and starts Temporal from the **new** Spec.

Three ways to get there:

1. **Half-launched interactive chat.** StartChat creates the chat, then
   `ExecuteWorkflow` fails (Temporal blip, the 30s RPC timeout, or a cancelled
   context). The handler returns Internal. The web cannot retry with the same
   id, because the server minted it and the response never arrived. But the chat
   shows up in the sidebar via `chat_created`, and its state is PENDING. The
   user's next send is `StartChat(chat_id=…)` with a **new** message, and it
   lands here.
2. **Two concurrent starts of the same pending branch,** for example two tabs.
   Both pass the pending check outside the transaction, and the second one's
   message is dropped.
3. **Schedule retry after the trigger was edited.** `finishExisting` rebuilds
   the spec from the current trigger row.

The response is a success, the optimistic message shows, and on reload it is
gone. The run executes the switched workflow while `chats.workflow_name` and the
root row keep the old one.

**Repro:**
```
saved messages=["first"] chat.workflow=builtin://agent temporal workflow=builtin://structured-agent
--- FAIL: the second send's message was silently dropped
```

**Fix:** decide "retry or new turn" from durable state, not from whether an
insert happened.
- Record a seed fingerprint (a hash of the seed messages and attachment ids) in
  the event payload.
- On `created=false`:
  - If the fingerprint matches, it is a true retry: start from the
    **persisted** state. That means the chat's `workflow_name` and presets, and
    the original params. Persist those too, for example in the payload, because
    today they exist only in memory.
  - If it differs, finish the original start first, then deliver the new
    messages as a continuation (SendMessage semantics). Alternatively, apply the
    switch/presets and save them in the same transaction before starting.
- Never build `initialData` for a workflow name the rows do not hold.

### M-B. A branch's first send replaces its message cache, so its inherited history disappears from view

- **Severity:** major. *Code-read.*
- **Where:** `web/src/store/chatStore.ts:1063` in `applyFirstSend` calls
  `setMessagesInCache(chatId, [optimisticUserMessage])`. `startExistingChat`
  shares it (`:1238`). Compare the old path at `:1536`, which appends with
  `patchMessagesCache(chatId, msgs => [...msgs, optimistic])`.

**What's wrong:** for a brand-new chat, replacing the cache is harmless because
it is empty. A branch arrives with its forked history already in the cache
through the chat snapshot. `makeMessageEnvelope` replaces `messages` wholesale,
and `initChatState` returns early because a cache entry exists. So after the
first send only the new message and the stream's incremental updates are
visible, until a reconnect or snapshot or a navigation reloads the list. Before
this change a branch's first send went through `sendMessage`, which appends.

**Fix:**
- In `applyFirstSend`, append (`patchMessagesCache`) instead of replacing, or
  take a `replace` flag that only `startChat` sets.
- Add a vitest test: `startExistingChat` keeps pre-existing cached messages.

### M-C. Stopping a spawn poisons its thread id for the life of the root execution, so a later resume of that agent is cancelled at once

- **Severity:** major. *Code-read.*
- **Where:**
  - `internal/workflow/runtime/workflow.go:2437` (`cancelled[sig.Thread] = true`).
    Nothing ever deletes from the map: `rg 'delete\(cancelled'` finds nothing.
  - `:1082` and `:3004-3009`: a thread's `Cancelled()` reads that map.
  - `:2878`: a resumption reuses `childThread = agentID`.
  - Senders that now name the original thread: `internal/llm/tools/spawn_stop.go:180`
    and `internal/grpc/services/tool_call.go:192-196`.

**What's wrong:** `spawn_stop` (and, after this change, the UI cancel of a
**resumed** spawn) records the cancellation under the agent's thread id. An
orchestrator that stops agent X and later calls `spawn(agent_id=X)` builds a
pause controller keyed on X. Its first step-boundary check returns
`ErrThreadCancelled`, so the resumption ends as "cancelled" before it does
anything. The agent can never use X again in this run.

Before this change, the UI cancel of a resumed spawn poisoned only the derived
workflow-row id, which is never reused. The first-spawn case was already
latent. The new tool makes this a normal agent move: stop, change approach,
resume.

**Fix:**
- Scope the cancellation to a resumption: key it on the tool call id, plus the
  thread only while that tool call is the live one.
- Or clear `cancelledThreads[thread]` when a new resumption of that thread is
  dispatched. This is workflow-code state, so gate it with `GetVersion`.
- Add a runtime test: stop, then resume the same agent; the resumption runs.

### M-D. The `SyncAll` orphan sweep can delete a live trigger's schedule

- **Severity:** major. *Code-read.*
- **Where:** `internal/triggers/syncer.go`:
  - `:172-183`: the `live` set is a DB snapshot taken at the start.
  - `:191-212`: the schedule list is read after the whole converge loop, and
    anything with the prefix that is not in `live` is deleted.

**What's wrong:** the comment argues the stale-visibility direction is safe. The
opposite direction is not. Suppose a trigger is created after the `ListTriggers`
snapshot but before `schedules.List`, during the N×(Describe+Update) converge
loop. That is a rolling deploy or a replica restart while someone creates a
trigger. Its schedule appears in the list, is missing from `live`, and is
deleted. Nothing repairs it until the next api-server restart, and the user's
trigger silently never fires. The startup path retries `SyncAll` up to 6 times,
so the window recurs.

**Fix:**
- Before deleting an orphan, re-check `GetTrigger(id)` and keep the schedule if
  the row exists.
- Better still, have `Sync` converge from a fresh read of the row, which also
  fixes the update race in M5 below.

---

## MINORS

| # | Where | Issue | Fix |
|---|---|---|---|
| M1 | `launcher.go:177-180`, `:368-371`; `workflows.go:355-357` | Any DB error from `GetProjectWithUserCheck` / `GetChat` becomes `NotFoundError`. A lookup error in `loadCreateChatWorkflowForValidation` becomes `ValidationError`. The fire path treats both as **non-retryable** and records `failed` (`fire.go:114-125`), so a DB blip during a scheduled fire permanently loses that fire. `ValidateModelSelector` → `GetAvailableDrivers` has the same problem: a failed settings read reads as "no API keys". | Return `InternalError` for anything other than a confirmed not-found or ownership mismatch. |
| M2 | `fire.go:74-90` with `config.go:TemporalOverlap` (ALLOW_ALL) | Overlap is check-then-launch with no lock. After an outage, the 10m catch-up releases several fires at once, and they all pass `overlapSkip` together, so overlap=skip launches concurrent runs. | Do the overlap check inside the launch transaction under a row lock on `triggers(id)`, for example through a guard hook on `Spec`. |
| M3 | `fire.go:137-142`, `:176-179`, `:184-187`; `:276-283` | A config or params error returns non-retryable **without** a `failed` event row, so a trigger that stops working leaves no explanation. Separately, `recordOutcome` on a dedupe conflict returns a fresh `ev.ID` that was never written. | Record `failed` for these. On conflict, load and return the existing row. |
| M4 | `launcher.go:304-343` | Inside the transaction, `CreateChat` runs before `CreateTriggerEvent`. For deterministic `NewChatID` launches, a concurrent duplicate fails on the chats primary key (surfacing as Internal) before the event's `ON CONFLICT` can fire, so the `errEventExists` "lost a race" branch is practically unreachable. It does converge on the activity retry. | Insert the event row first, so the dedupe constraint decides deterministically. Add a real-DB concurrency test. |
| M5 | `services/trigger.go:121-128`; `:204-214`; `:258-264` | (a) The Create rollback deletes the row but not a schedule that `Sync` may have half-created; it orphans until `SyncAll`. (b) Concurrent Update/SetEnabled calls can converge Temporal to the older definition, because `Sync` pushes the caller's in-memory struct. Drift lasts until the next restart. | (a) Call `syncer.Delete` in the rollback. (b) Use `Sync(ctx, id)` that re-reads the row. |
| M6 | `services/run.go:308` | `SignalRun` uses `Live()`, which includes PENDING. On a pending run it forwards to SendMessage, which now returns FailedPrecondition, where the doc contract implies `delivered=false`. The `!Live` comment also says messages are persisted, but nothing persists them. | Treat PENDING as not deliverable, and fix the comment. |
| M7 | `launch/launcher.go:49-56`, `:64-70` | `Launcher` takes the whole `db.Repository`. That is against the repo rule (consumer-side, thin interfaces) and against TRIGGERS.md's own contract. `triggers.Repo` follows the rule. | Declare the roughly 15 methods `launch` actually uses. |
| M8 | `queries/triggers.sql:17` | `ListTriggers` with an empty `user_id` returns every tenant's triggers. The handler is safe today, but a tenant-scoped query with an "empty means all" escape hatch is a footgun. | Add a separate `ListAllTriggers` for `SyncAll`. |
| M9 | `web/src/store/chatStore.ts:1225-1226` | `startExistingChat` sends `currentProject.id`. If the branch's project is not the selected project (deep link, cross-project tab), the launcher rejects it with "project_id does not match". | Omit `project_id` when `chat_id` is set, or send `chat.projectId`. |
| M10 | `runtime/approval_flow.go:17` | Unattended runs are covered for `ask_user`, `ask_question` and self-pause, but an approval **node** in a scheduled run waits `defaultApprovalTimeout` (1h) and then resolves `timeout`. | Auto-resolve under `IsUnattended` the way questions do, or document it. |
| M11 | `syncer.go:21`, `:202` | Schedule ids are not namespaced per deployment or database. Two deployments sharing one Temporal namespace (different DBs) would delete each other's schedules as orphans. Fires would also land on the other deployment's worker and skip as "trigger deleted". | Check the topology. If sharing is possible, add a deployment discriminator to the prefix or a memo. |
| nit | `call_llm.go:2009` vs `:2177-2186` | `spawn_stop` is added to the `load_tool` allowlist even when the agent cannot spawn, which contradicts the comment that non-spawners must not be offered it. Harmless, because the tool rejects non-children. | |
| nit | `triggers/startup.go:11-14` | The comment says "SyncAll is also called by every write path". Write paths call `Sync`, not `SyncAll`. | |
| nit | `queries/chats.sql:79-` | `ListArchivedChats` reads `chats`, not the view, so archived chats return `workflow_state` UNSPECIFIED. | |

---

## Tests missing for risky paths

1. Fire retry after a half-launch with the default overlap (B1). The existing
   `TestFireSkipsWhileThePreviousRunIsLive/pending` asserts the buggy behaviour.
2. The BranchChat response carries PENDING (B2). No BranchChat test checks
   `WorkflowState`.
3. StartChat with `chat_id` into an event-recorded pending chat, with
   **different** content and workflow (M-A).
4. Two concurrent `Launch` calls with the same `(kind, dedupe_key)` against a
   real DB (M4).
5. Stopping a spawn, then resuming the same agent in the same run (M-C).
6. Web: `startExistingChat` preserves existing cached messages (M-B).
7. `SyncAll` racing a create (M-D).
8. `SignalRun` on a pending run (M6).

---

## Checked and sound

**Launch / idempotency**
- The event row is written in the same `RunTx` (SERIALIZABLE) as the chat, root
  workflow, thread, seed messages and `chat_created` update. The
  `CreateTriggerEventRollsBackWithTransaction` test pins this.
- `UNIQUE(kind, dedupe_key)` exists in the migration and in `schema.sql`.
- `chat.start` dedupe key = chat id, minted server-side, so a client cannot
  choose `NewChatID`.
- Schedule dedupe key = fire workflow id. The chat id is `uuid.NewSHA1(fixed ns,
  fire id)`, which is deterministic and tested.
- Exact-retry resumability works: `TestLaunchResumesAfterTemporalStartFailure`
  shows one event, one message, then ACTIVE.
- `ExecuteWorkflow` uses `USE_EXISTING`. A retry that finds a closed execution
  starts a fresh run under the default reuse policy. That is acceptable, because
  the first run never progressed past PENDING and messages are not re-saved.
- The pending→active CAS runs after a successful start. A lost CAS is harmless:
  `workflow_status.go:245-255` flips pending to active itself and never
  clobbers a terminal state.
- The `ErrNotPending` path makes no Temporal call (tested).
- `launch` imports neither `connect` nor `auth`. The owner is always explicit.
- Error mapping in `launchErrorToConnect` keeps the historical codes.

**Authorization**
- `TriggerService` uses `ownedTrigger` (NotFound for others' triggers) on Get,
  Update, Delete, SetEnabled, Fire and ListEvents.
- Create and Update check project ownership with `GetProjectWithUserCheck` and
  check that the worktree belongs to the project. The kind is not updatable.
- `ListTriggers` always filters by the caller.
- The fire path re-checks project ownership as the owner, and re-validates the
  worktree→project relationship in `ResolveChatWorktreeID`.
- StartChat with `chat_id` checks the chat's owner and that project and worktree
  match.
- `spawn_stop` only acts on direct children, checked via `ListSpawnChildren`.
- The CancelToolCall ownership check is unchanged.

**Schedules**
- Delete removes the schedule before the row. Create rolls back the row when
  `Sync` fails.
- Paused exactly when the trigger is disabled. Update preserves the action
  budget.
- Overlap is ALLOW_ALL at the Temporal level, so declines are recorded rather
  than dropped. The catch-up window defaults to 10m.
- 5-field cron validation, and a 1-minute minimum interval.
- `TemporalScheduledStartTime` is read in workflow code, which is
  deterministic, and is verified against the dev server. Manual fires fall back
  to the workflow start time.
- Validation failures are non-retryable through `NonRetryableErrorTypes`
  (tested).
- A deleted trigger ends the fire quietly.
- Manual fires bypass the enabled and overlap checks and get unique ids.
- The worker registers the fire workflow always, and the activity only when a
  launcher is wired.
- `SyncAllOnStartup` runs in the background with bounded retries.

**StartChat / SendMessage**
- The pending arm rejects before saving anything.
- The paused (including discuss), active, failed (inspect, reset-replay,
  question resume, coarse resume), completed/cancelled and ghost-resurrection
  arms are unchanged; the diff only reroutes helpers through `launcher()`.
- Restarts keep `TERMINATE_EXISTING`.
- A workflow switch on a started chat returns the existing FailedPrecondition
  message.
- `RunService.StartRun` routes a pending session to StartChat and everything
  else to SendMessage. A new session goes through StartChat.
- The auth timeout key was renamed. `mode` becomes `workflow_params.mode`
  (tested). `UserJWT` is carried on the launch (tested).

**Schema**
- `20261003220140`: DROP and CREATE, `c.*` first, the new columns appended after
  `activity` (positional scans stay aligned). The `LEFT JOIN` on the primary key
  cannot fan out. The body is otherwise byte-identical to the previous view.
- `20261003213733`: CHECKs, FKs (`CASCADE` on project, `SET NULL` on worktree,
  trigger and chat) and indexes all match TRIGGERS.md. No `-- +goose Down` in
  either migration.
- `chatToProto` sets `workflow_state` and `workflow_stop_reason` from
  `RootStatus`. The store maps NULL to UNSPECIFIED. Tested through GetChat,
  ListChats and SearchChats.

**spawn_stop**
- It picks the **latest** resumption (tested).
- The signal carries the thread id and the tool call id. The reconcile CAS
  targets the resumption's workflow row (tested in the tool, the stopper and
  CancelToolCall).
- A signal failure propagates as "STILL RUNNING". A nil stopper reports that it
  is unavailable.
- The signal name is aliased from the `threadcancel` leaf package, which keeps
  replay safe (same string, pinned by a test).
- It is wired on both the worker and the api-server.
- It is offered only when `canSpawnChildren`, apart from the nit above.

**Generated code**
- No hand edits detected. sqlc, the tool catalog, `tools.md` and `cli.md` match
  their generators.

## Not verified

- Proto regeneration drift: BSR rate limit, as noted in the table above.
- M-B, M-C and M-D in a running app or runtime. They come from code reading.
- The web half of B2: the server half is reproduced, and the web staleness is
  inferred from `staleTime` and the seeded cache.
- `TestCronTriggerFiresEndToEnd`, which is opt-in.
