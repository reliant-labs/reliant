# 10 — P6: the Repository split

Status: **done** — every lane merged; Y (the last) deleted `db.Repository`. The
numbers below are the measurement the plan was built on, not the end state. Y
re-measured the dead set with a type-checked scan: 41 methods, not 31 (the 31
here, the 8 only `internal/db`'s own tests called, and
`UpdateWorkflowWorkerStarted`/`Stopped`, which have no caller either); the 7 "test-only" methods that are live through wrapper interfaces
stayed. Of the internal-only methods, 11 are unexported; `CreateMessageOnce`,
`GetContextWindowWithThread`, `GetItemDefault`, `GetThreadTokenCount`,
`GetVisibilityOverride`, `MigrateLegacyCredentials` and `WithSessionPool` stay
exported because tests outside the package (the `internal/store` twin checks,
`threads`) call them. It had one owner decision (§2.4). Shared
context: `00-BRIEF.md`, `PLAN.md` (P6), `02-contracts-classification.md` §2 (the first
Repository census), `08-p5b-spike.md` (where the stores went), `09-roots-plan.md` (the
graph roots).

Measured on reliant `origin/main` **`444afb7b`** (#1238) with forge pinned at
`v0.1.47-0.20261021213236-2472900c2b41`. The P5b finish (#1199) and roots R3–R5 are in that
tree. Numbers come from a go/types scan, not from grep (see "How this was measured" at the
end), so a call through an embedded field or a local alias still counts, and a method name
that is merely shared with an unrelated type does not.

---

## 0. Summary

- **The split is already more than half done.** 61 packages declare **95 role interfaces**
  that `*db.Repo` satisfies structurally (Appendix C). 23 of the 34 handler packages already
  take a narrow `Repository` in their `Deps`. What remains is **27 packages that still hold
  the whole `db.Repository`** (223 fields and params in non-test code). Five of them carry
  most of the weight: `workflow/runtime/activities/handlers` (76 methods), `handlers/chat`
  (74), `llm/tools` (48), `workflow/reconciliation` (26) and `handlers/tools_daemon` (21).
- **Recommendation (§2): keep `*db.Repo` as the one concrete persistence type and the one
  graph node. Delete the `db.Repository` interface. Every consumer declares the interface
  it calls, where it calls it.** Do not dissolve the Repo into per-aggregate store nodes.
  Interfaces are structural, so this leaves a later per-aggregate split of the
  *implementation* possible without touching a single consumer. Doing it the other way
  round can't be done incrementally.
- **Transactions don't change.** `RunTx` stays on `*db.Repo`, and the ctx-carried tx bridge
  means any method called with the tx ctx joins, whichever interface the caller reached it
  through (§3).
- **Tests mostly don't change.** 196 DB-backed test files (501 `db.SetupTestDB` /
  `NewTestRepo` calls) keep passing a real `*db.Repo`. The 100 test files that name
  `db.Repository` as a type are converted by the lane that owns their package: a stateless
  fake becomes a generated `Mock<Role>`, and a stateful fake stays, asserted against its
  role (§4).
- **Lanes (§6):** one small leaf wave (W1), then five parallel lanes with disjoint package
  ownership (A chat+streaming · B five handlers · C activities · E llm/tools · F mocks for
  the already-narrowed handlers), D after C, then Z (`internal/app`, after R6) and Y
  (delete `db.Repository`; the compiler is the proof). There's also one forge item, F-P6a,
  in parallel, so `forge lint` can gate regressions.
- **`internal/db` survives (§7)** as *the database component*: the concrete Repo, `RunTx` +
  the tx bridge, pools, the migrator and goose bridge, and the test harness. P6 removes the
  interface, 31 dead methods and the implementation-side `core.*Store` declarations. It does
  not remove the package.

---

## 1. Inventory

### 1.1 The surface

| Thing | Count |
|---|---:|
| `db.Repository` interface methods (`internal/db/repository.go:15`, incl. the embedded `core.ProviderAccountStore` and `core.RunEventStore`) | **463** |
| exported methods on `*db.Repo` | **492** (29 not on the interface: `ORM`, `Connections`, `IntegrationWrites`, `SetUpdateNotifiers`, `EnableCredentialSealing`, …) |
| …of which ≤6-line passthroughs over an `internal/store` function | 282 (107 of them one line) |
| …that carry logic (validation, multi-statement transactions, hub notifications, sealing, session locks) | 210 (15 open their own transaction; 4 emit to the update hubs) |
| interface methods called by **anything** outside `internal/db` in non-test code (direct calls ∪ role-interface methods) | 387 |
| interface methods the selection scan finds no non-test caller for outside `internal/db` (Appendix D) | 84: **31 dead** (name-checked), 34 reached only from tests outside `internal/db` by the scan (7 of those are live through wrapper or anonymous interfaces), 10 called only inside `internal/db`, 8 called only from `internal/db` tests |

So `*db.Repo` is **not** a thin facade that would fall apart into generated stores: 43% of
its methods are behavior. That fact drives §2.

### 1.2 The three consumer populations

1. **Already narrowed:** 95 role interfaces in 61 packages (Appendix C). Examples:
   `threads.Repository` (43), `launch.Store` (31) plus 8 smaller `launch` roles,
   `handlers/inbox.Repository` (27), `handlers/project.Repository` (24),
   `handlers/workflow.Repository` (20). In `internal/app/compose.go`, 26 handler `Deps.Repo`
   fields already have a narrow type and are filled with `g.DBRepo().Repo`. These need
   nothing from P6 except mocks (lane F).
2. **Fat holders:** 27 packages, 223 non-test fields and params typed `db.Repository`. Each
   one's measured direct method set and the collaborators it forwards the repo to are in
   Appendix A; per-file breakdowns of the three largest are in Appendix B. Summary:

   | consumer | direct methods | holds | forwards the repo to | lane |
   |---|---:|---:|---|---|
   | `workflow/runtime/activities/handlers` | 76 | 73 | `threads.NewService` ×7, `messageconv`, `repo.AdoptFromDaemon`, `workspacecreate`, `runevents`, `toolbindings`, `db.UpsertToolCallStatus` | C |
   | `handlers/chat` | 74 | 1 | `threads.NewService` ×5, `runs.NewService`, `launch.{NewLauncher,LoadChatTrigger×6,ResolveRunWorkflow}`, `runtime.ResumeInputFromDurableState` ×6, `chatselection`, `daemonchoice`, `daemonplacement`, `messageproto` | A |
   | `llm/tools` | 48 | 87 | `threads.NewService` ×2, `workflowsource`, `repo.AdoptFromDaemon`, `workspacecreate` | E |
   | `workflow/reconciliation` | 26 | 2 | 3 exported helpers of `activities/handlers`, `runevents`, `db.UpsertToolCallStatus` | D |
   | `handlers/tools_daemon` | 21 | 4 | `projectfacts` | B |
   | `handlers/worktree` | 18 | 1 | `workspacecreate`, `repo.AdoptFromDaemon`, `daemonplacement`, `machinewake` | B |
   | `handlers/streaming` | 16 | 2 | `threads.NewService`, `messageproto` | A |
   | `handlers/trigger` | 16 | 2 | `launch.ResolveRunWorkflow` ×5, `triggers.*` | B |
   | `handlers/preset` | 15 | 1 | `workflowsource`, `workflowcatalog` | B |
   | `tools/reliant-dev` | 14 | 1 | — | E |
   | `handlers/settings` | 11 | 1 | `drivers.HasStaleReliantKey` | B |
   | `workflow` (PauseService) | 10 | 2 | — | D |
   | `bgprocess` 8 · `toolexec` 5 · `repo` 4 · `workflow/runtime` 4 · `workflow/messageconv` 4 · `mcpconfig` 3 · `llm/tools/shell` 2 · `pkgmgr` 2 · `configadapter` 1 · `llm/drivers` 0 (forwards only) · `grpc` 0 (forwards only) | ≤8 | 1–7 | small | W1 |
   | `workersetup` (registry) · `workflow/runtime/activities` (registry) · `workflow/workflowlisttest` | 0–2 | 1–6 | every activity constructor / trigger firer | C / D / E |

3. **Composition (`internal/app`):** the graph node is `*app.DBRepo`
   (`providers.go:63`, `struct{ Repo *db.Repo }`). It is a wrapper because forge reads
   lifecycle off the node's type (`Close(ctx)`/`Ready(ctx)`), and `*db.Repo`'s `Close()`
   takes no context. 47 provider `Deps` fields take `*app.DBRepo`. 74 `g.DBRepo()` and 42
   `.Repo.Repo` expressions in `compose.go`/`providers*.go` hand the inner `*db.Repo` to
   about 90 constructors. Because no node *produces* `*db.Repo`, forge can never resolve a
   persistence field by type; every key is hand-written.

### 1.3 Why `forge lint` is silent about all of this

`forge lint --conventions` today reports **zero** `forgeconv-deps-surface` findings (214
findings in total, all activity-declared, error-mapping, file-size or getenv). There are
two reasons, both in forge:

- `internal/depsurface` marks a field `Escapes` when its value is passed on, and skips
  escaping fields entirely (`depsurface.go:513`, `least_knowledge.go` `if … f.Escapes`).
  **Every one of the seven fat handler `Deps.Repo` fields escapes** (into `threads.NewService`,
  `workflowsource.Resolve`, `drivers.HasStaleReliantKey`, `projectfacts.*`, …), so the one
  rule written for this exact smell can't see it. → **F-P6a** (§8).
- It only scans packages that declare a `Deps` struct (`FindComponentDirs`), so
  `llm/tools` (87 holds) and `activities/handlers` (73), which take the repo through
  constructor params, are invisible to it. P6 doesn't need forge to fix this, because
  deleting `db.Repository` (lane Y) makes the compiler the gate. Noted as F-P6b.

---

## 2. Target shape

### 2.1 Alternatives

**A — One concrete `*db.Repo`; delete `db.Repository`; consumer-declared roles.**
`*db.Repo` stays the single implementation. The graph has one node producing it (§2.3).
Every consumer's field or param becomes an interface the consumer declares, holding exactly
what it calls. Forge fills each one with that node, by type.
- *For:*
  - Zero change to transactions, notifiers, sealing and the session pool. They are state on
    one struct.
  - Zero change to the 196 DB-backed test files.
  - One producer per persistence interface, so the graph has nothing ambiguous to resolve.
  - Each lane is pure type narrowing, which compiles or doesn't.
  - Matches the project rule exactly: declare where consumed, accept interfaces, return structs.
- *Against:*
  - The implementation stays one 492-method type (`repository_impl.go` alone is 3,900 lines).
  - Interface signatures keep naming `db.*` alias types.

**B — Dissolve the Repo into per-aggregate concrete stores, each a graph node**
(`ChatStore`, `MessageStore`, `WorkflowStore`, … in `internal/store` or `internal/db/<agg>`).
- *For:*
  - Cohesive units.
  - A root reaches only the aggregates it uses (the gateway would get daemon and attachment
    stores, not workflow).
  - Per-aggregate tests.
- *Against:*
  - The 210 behavior methods cross aggregates. `SaveMessageToThread` touches chat, thread,
    message, counters and the update ledger. The workflow cascades touch workflows, threads
    and chats. `CreateChatUpdate` is called by everyone.
  - The shared state (update notifiers, credential sealer, session pool for advisory locks,
    `RunTx`'s retry policy and its `retrySleep` seam) becomes a "base" node that every
    aggregate depends on, plus cross-aggregate calls or a new interactor layer.
  - `activities/handlers` would take 8–10 store fields.
  - Every DB-backed test needs a fixture struct of stores. That's 501 call sites.
  - Roughly 3–5× A's churn. Least privilege per root buys nothing real, because every root
    shares one pool and one Postgres role, and that role is the actual security boundary.

**C — Keep `db.Repository` and generate its mock.** Rejected, as PLAN P6 and 02 §2 already
did: a 463-method `MockRepository` is the mega-mock, and it couples every test to the whole
surface.

**D — Aggregate-scoped interfaces declared in `internal/db`** (`db.ChatStore`,
`db.WorkflowStore` implemented by `*db.Repo`). This is what the `core.*Store` interfaces
were, and P5b deleted most of them: implementation-side declarations, which the project
rule forbids. A consumer would still depend on 30 methods to call 3.

**E — Consumers take forge's generated `store.<Entity>Store` directly** (the framework seam:
`store.NewStore(g.<orm.Client node>()).<Plural>()`). Only usable for pure CRUD, and those
stores speak **row** types (`store.Chat`: `Id`, `int64` flags, `jsonb` as `string`), not
domain types (`core.Chat`). 08 decided rows map to `core` at the boundary (F12.6), and
nothing in 1.1's 107 one-line passthroughs is worth leaking row shapes for. It would also
add an `*orm.Client` node, whose `RunTx` has the same signature as `Repo.RunTx` but a
narrower retry set (40001/40P01 only, versus Repo's 40001/40P01/40002/25P02/23505,
`repo.go:236`). Rejected for P6. Revisit if `core` ever collapses into the entities.

### 2.2 Recommendation: A

Concretely:
1. Delete `db.Repository` (`repository.go`) and its assertion (`repository_impl.go:26`).
2. Every package that holds a repo declares its own interface:
   - a handler, worker or plain component package → `deps.go` (forge's
     `// forge:deps — consumer-declared interfaces for this package's Deps.` marker, which
     `forge generate` mocks into `mock_gen.go`);
   - a contract package → `contract.go`;
   - an unexported helper → next to the function that takes it.
3. **Composition code takes the concrete type.** Code whose job is to *wire* many
   collaborators over one repo is composition, not a consumer: `activities.NewActivities`,
   `workersetup.Config.Database`, `tools.ToolsOptions.Repo`, the `internal/app` providers
   and the `tools/reliant-dev` CLI take `*db.Repo`. Each thing they construct takes its
   own narrow interface. The alternative, a 76-method `activities.Store` union interface,
   would be the god interface again under a new name.
4. **A consumer that forwards the repo embeds the callee's exported role interface**
   (`type Repository interface { threads.Repository; launch.ChatTriggerReader; GetChat(…) … }`).
   That's precise: "I hand this to `threads.NewService`, so I need what threads needs." When
   threads' needs change, the compiler points here. WET is the default; embedding is only
   for forwarding.

### 2.3 The graph node (lane Z, after R6)

Add one provider in `internal/app/providers.go`:

```go
// Database is the persistence API every consumer's role interface is filled with.
type DatabaseDeps struct{ Pool *DBRepo }
func NewDatabase(d DatabaseDeps) (*db.Repo, error) { return d.Pool.Repo, nil }
```

- `DBRepo` stays the **lifecycle** node (the name `db_repo` is pinned by the stop-order
  tests in `roots_{api,worker,gateway}_test.go`).
- `Database` is the **value** node. It is the only node producing `*db.Repo`, so forge's
  step-4 resolution ("the one node whose value is assignable") fills every persistence role
  with `g.Database()` when a key is new.
- Provider `Deps` fields `Repo *DBRepo` become `DB *db.Repo`, and the `.Repo.Repo`
  expressions disappear.

The alternative was one node of type `*db.Repo` with `Ready(ctx)` and `Close(ctx)`. It was
rejected because `*db.Repo` already has `Close() error`, called 37 times (31 of them in
tests), and Go has no overloading.

**Ambiguity and `//forge:root-only` (forge #1095).**
- #1095 exists because resolution fills an *interface* field with any node whose value is
  assignable to it. That's how the worker-only `WorkerDaemonRouter` reached api handlers.
- Narrowing makes persistence fields smaller, so *more* nodes could satisfy one. Two
  producers is a loud generate error (AMBIGUOUS), so that case is safe.
- The silent case is a narrow interface whose **only** assignable producer is a node other
  than `Database`. Example: a 1-method `GetChat` role that some caching node also
  implements, with `Database` not yet existing.
- Mitigations:
  - Every lane keeps the persistence keys **hand-written** in `compose.go`, and forge never
    re-resolves an author's key.
  - Lane Z adds `TestPersistenceRolesAreTheDatabase` in `internal/app`. For each composite
    key in `compose.go` whose field type `*db.Repo` implements, it requires the value to be
    `g.Database()`, with an explicit allowlist (for example `system.Pinger`, which several
    nodes implement). It loads packages, so it is slow: `if testing.Short() { t.Skip(...) }`.
- `Database` is shared by all three roots, so it is **not** root-only. A future
  root-specific variant (a read replica for the worker, say) would be a distinct type
  marked `//forge:root-only worker`, which resolution then offers only to fields of that
  exact type.

### 2.4 Devil's advocate (against my own recommendation)

- *"A hides a god object behind thirty masks."*
  - Yes, deliberately. The coupling defect was consumers *knowing* 463 methods. A large
    concrete type with one responsibility (persistence over one pool, one transaction model,
    one notifier) is not a package-boundary leak.
  - After A, splitting the implementation per aggregate (B's real benefit) is invisible to
    consumers: it changes only `compose.go` keys. B before A has to rewrite every consumer
    *and* the implementation in the same change. So A is strictly on the path to B, and B
    can be decided later, on evidence.
- *"`handlers/chat.Repository` will have ~74 methods plus embedded `threads.Repository`
  (43) and `launch` roles — a mega-mock again."*
  - The mock is per consumer and generated, and tests set only the `XxxFunc` they touch.
  - The width is real: it's the chat handler's own breadth (18 files, 198 error sites).
    Narrowing it further means splitting the handler, which is a product decision outside
    P6.
  - Lane A should still move collaborators the graph already builds out of the handler:
    `runs.Service` is the `RunLifecycle` node and `launch.Launcher` is `RunLauncher`. That
    is an *inject, don't construct* fix, and only if R5's root reach allows it (check that
    they aren't worker-only before adding the Deps fields).
- *"Signatures still say `db.Chat`; consumers still import `internal/db`."*
  - Those are aliases to `core`, and the import is for types, not for the Repo.
  - Removing the aliases is a 6,105-reference rename that 08 already rejected. It is not
    what P6 is for.
  - Binary size is no argument either, because `cmd/reliant` is one binary that links
    `internal/db` anyway.
- *"Concrete `*db.Repo` in `ToolsOptions`/`Activities`/`workersetup.Config` violates
  'accept interfaces'."*
  - These are composition points. They construct 37–40 things each over one repo, and the
    things they construct accept interfaces.
  - An interface at the composition point has no substitution point: no test swaps the
    whole registry's repo for a fake, and the registry tests that exist use a real DB. The
    project rule calls that kind of interface indirection.

**Owner decision:** A, as above (recommended), or B. Everything below assumes A. B would
add an implementation-split wave after Y and change none of the lanes before it.

---

## 3. Transactions across the split

- `Repo.RunTx(ctx, orm.TxPolicy, fn)` and `RunTxReadOnly` stay on `*db.Repo`, with the
  explicit isolation policy and the attempt loop. A consumer that opens a transaction lists
  `RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error`
  in its own interface (as `launch.Transactor` already does).
- Joining is by **ctx**, not by receiver (`tx_bridge.go`). Inside `fn`, every `*db.Repo`
  method and every `internal/store` call made with the ctx `fn` was handed joins the
  transaction, whichever narrow interface the call went through. Narrowing therefore can't
  split a transaction.
- The one way to break this is to give a consumer **two different producers** for "open a
  tx" and "do the work". For example, an `*orm.Client` node filling a `Transactor` while
  `Database` fills the reads. That would still join (it's the same pool and tx key), but
  it would silently swap Repo's retry set for the ORM's. That's why §2.1 E rejects an
  `*orm.Client` node, and why §2.3's guard test covers `RunTx` roles.
- Mocks: forge's generated mock returns "`RunTxFunc` not set" by default. W1 adds
  `dbtest.RunTxInline` (`func(ctx, _ orm.TxPolicy, fn func(context.Context) error) error
  { return fn(ctx) }`) to `internal/db/dbtest` so tests write `RunTxFunc: dbtest.RunTxInline`.
  Tests that assert rollback or retry behaviour stay DB-backed.
- The tx guarantees keep their existing proofs, which P6 doesn't touch:
  `internal/store/storetest/{txbridge,txisolation}_test.go`,
  `internal/db/repo_runtx_retry_test.go`, `repo_txoptions_test.go`.

---

## 4. Mocks and tests

**What exists:**

| Kind | Where | P6 effect |
|---|---|---|
| Contract mocks | `mock_gen.go` in the 34 packages with a `contract.go` (`launch`, `runs`, `workspacecreate`, `triggers`, …). Forge mocks **every** interface in `contract.go` (`contractkit.Recorder` + `XxxFunc` fields, "Func not set" errors) | Roles declared in `contract.go` get mocks for free |
| Handler role interfaces | declared in `service.go` (e.g. `handlers/inbox/service.go`), so **no mock** | Lane F moves them to `deps.go` (marker line) → `mock_gen.go` |
| `internal/handlers/mocks/*_mock_gen.go` (36 files) | forge's per-**rpc** service mocks (`<Svc>ServiceMock`, one `XxxFunc` per rpc, `Register`/`Name`). They fake a *service* for its callers | **Not a persistence seam, and unaffected by P6.** Reliant has 0 importers today (friction note, §8) |
| DB-backed tests | 196 files, 501 `db.SetupTestDB`/`SetupTestDBWithRawDB`/`NewTestRepo` calls (+ `storetest.New` in `internal/store`) | **Unchanged.** `*db.Repo` satisfies every role |
| Partial fakes `struct{ db.Repository; … }` | 36 embeds; 100 test files in 28 packages name `db.Repository` as a type (list per lane in §6) | They still compile after a field is narrowed (an embedded `db.Repository` satisfies any subset), but **must** change before Y deletes the interface. The owning lane converts them |

**Conversion rule** (refines 02 §3):
- A stateless stub that overrides a few methods and records args → the generated
  `Mock<Role>`.
  - This is strictly better than an embed. An un-overridden call on an embedded nil
    `db.Repository` is a nil-deref panic. The mock returns a named "Func not set" error.
- A stateful fake (maps + mutex modelling uniqueness, dedupe or ordering, e.g.
  `triggers/fake_test.go`) → keep it, embed the *role* instead of `db.Repository`, and
  assert `var _ Role = (*fake)(nil)`.
- An httptest or adapter fake → untouched.
- Don't invent a mock for a test that is really about SQL semantics. Make it DB-backed
  with your own `DATABASE_URL`, never 5434.

---

## 5. Rules every lane follows

1. **Keep the field name.** `Deps.Repo` stays `Repo`; only its type changes. Every
   `s.deps.Repo.X(…)` call site, and so every rpc body the P7 svcerr sweep edits, stays
   byte-identical. This rule is what makes P6 and P7 near-conflict-free.
2. A role interface lists **only** methods the package calls, plus embedded callee roles
   for values it forwards (§2.2.4). Prefer `forge fix narrow-deps <dir>` where it applies.
   It writes `deps.go` + `mock_gen.go`, but it skips escaping fields (§1.3), so the forwarding
   cases are by hand until F-P6a lands.
3. Name: `Repository` in a handler's `deps.go` (matching the 20 that exist); unexported
   `<thing>Store` for per-activity, per-tool and per-helper roles.
4. Composition points take `*db.Repo` (§2.2.3). Nothing else does.
5. No change to `internal/db/**` except W1's `dbtest.RunTxInline` and Y. No change to
   `internal/app/**` except Z. `git diff --exit-code internal/app` is part of every other
   lane's proof (narrowing a type doesn't change Deps keys, so `forge generate` must leave
   `compose.go` alone).
6. Convert the package's own `db.Repository` test usages (§4) in the same PR.

---

## 6. PR sequence and lanes

```
W1 leaf callees (+ dbtest.RunTxInline) ──┬─▶ A chat, streaming ──────────────┐
                                         ├─▶ B worktree, tools_daemon, trigger, preset, settings
F-P6a (forge, any time) ─────────────────┼─▶ C activities (+registry) ─▶ D reconciliation, PauseService, workersetup
                                         ├─▶ E llm/tools, reliant-dev, workflowlisttest
F handler roles → deps.go + mocks (no W1 dependency) ───────────────────────┤
R6 (in flight, internal/app + cmd) ─▶ Z internal/app node `Database` ───────┴─▶ Y delete db.Repository, dead methods, docs
```

**Why W1 comes first.** A caller can narrow only after every callee it forwards to accepts
something narrower than `db.Repository`. Until then, its narrow interface doesn't satisfy
the callee's parameter. A callee can narrow at any time, because its callers still pass
`db.Repository`, which satisfies anything. Hence leaf-first. D waits on C for the same
reason: reconciliation forwards to three `activities/handlers` helpers that C narrows.

| Lane | Owns (files/dirs) | Work | Test files it converts | Size |
|---|---|---|---|---|
| **W1** leaves | `internal/workflow/messageconv`, `internal/workflow/runtime`, `internal/repo`, `internal/mcpconfig`, `internal/llm/tools/shell`, `internal/pkgmgr`, `internal/bgprocess`, `internal/llm/drivers`, `internal/configadapter`, `internal/toolexec` (`RouterConfig.DB`, `NATSDaemonRouter.db`, `dbAdapter`), `internal/grpc` (`APIHTTPRoutesDeps.Repo`, `server.go:255`); `internal/db/dbtest` (add `RunTxInline` only) | narrow each param or field to an unexported role next to its use; drivers and grpc forward only, so their roles embed the callee roles (`modelprefs.SettingsReader`, `terminal`, `attachment`, `mcpserver` readers) | messageconv 2, runtime 2, drivers 3, toolexec 1, grpc 2 | S, 1 agent, 1–2 PRs |
| **A** conversation | `internal/handlers/chat`, `internal/handlers/streaming` | `deps.go` `Repository`. Chat: 74 own methods, embeds `threads.Repository` and the `launch`/`runtime`/`chatselection`/`daemonchoice`/`daemonplacement`/`messageproto` roles it forwards to. Optionally inject `RunLifecycle`/`RunLauncher` instead of constructing (§2.4, check root reach first; touches compose.go, so after Z or coordinated). Streaming: 16 + `threads.Repository` + `messageproto.ToolCallReader` | chat 6, streaming 3 | M |
| **B** workspace/admin handlers | `internal/handlers/{worktree,tools_daemon,trigger,preset,settings}` | one `deps.go` `Repository` each (18 / 21 / 16 / 15 / 11 methods + forwarded roles). Trigger's anonymous `automationGrants` field interfaces become named roles in `deps.go`. Trigger and `workersetup` also read `Connections() core.ConnectionStore`, which stays until Y | tools_daemon 8, worktree 5, settings 3, trigger 3 | M |
| **C** activities | `internal/workflow/runtime/activities/handlers`, `internal/workflow/runtime/activities` | per-activity `<x>Store` (34 files, Appendix B). Exported helpers used outside the package (`TransitionChatOnCompletion`, `EmitTransitionMessage`, `WriteWorkflowError`, `LoadMessagesForLLM`) get exported roles. `activities.Activities.Repo` and `NewActivities` take `*db.Repo`. `threads.NewService(repo)` ×7: embed `threads.Repository` where used | activities/handlers 25, activities 1 | L, the critical path |
| **D** reconciliation | `internal/workflow/reconciliation`, `internal/workflow` (`pause_service.go`), `internal/workersetup` | reconciler `store` (26 + embedded C helper roles), `pauseStore` (10). `workersetup.Config.Database` takes `*db.Repo`. `accountRouterStore`/`credentialMarker` declare their own instead of `core.ProviderAccountStore` | reconciliation 1, workflow 1 | S–M, **merges after C** |
| **E** tools | `internal/llm/tools`, `tools/reliant-dev`, `internal/workflow/workflowlisttest` | per-tool `<tool>Store` (21 files, Appendix B). `ToolsOptions.Repo` takes `*db.Repo` (the factory is composition). reliant-dev takes `*db.Repo` | llm/tools 13 | M |
| **F** mocks for the narrowed | the 20 handler packages with a `Repository` in `service.go` (approval … workflow); test-only `db.Repository` usages in `launch` 7, `project` 3, `projectfacts` 2, `filepreview` 1, `attachment` 1, `catalog` 1, `file_system` 1, `handlers/workflow` 1, `threads` 1, `workflow/scenario/runner` 1, `workspacecreate` 1; move `launch/store.go`'s roles into `launch/contract.go` | move each declaration into `deps.go`/`contract.go` (a cut-paste plus the marker) so `forge generate` writes its mock; convert those tests | 20 | M, mechanical; can split in two |
| **Z** graph | `internal/app/**` | `Database` node (§2.3); provider `Deps` `Repo *DBRepo` → `DB *db.Repo` (47 fields); `g.DBRepo().Repo` → `g.Database()`; `backgroundProcessDaemons.repo` → 1-method role; `TestPersistenceRolesAreTheDatabase` | app 1 | S, **after R6** |
| **Y** delete | `internal/db/**`, `internal/db/core/**`, the `Connections()`/`IntegrationWrites()` consumers (`triggers/declaration.go`, `triggers/origin.go`, `integrations/writeledger/contract.go`, `connections/tokensource.go`), docs | delete `db.Repository`, `var _ Repository`, the embedded `core.ProviderAccountStore`/`core.RunEventStore` (and their `var _` asserts); delete the 31 dead methods; unexport the 10 internal-only ones; `Connections()`/`IntegrationWrites()` return their concrete stores, and `core.{ConnectionStore,SecretsTx,IntegrationWriteStore}` move to their consumers; update `internal/db/doc.go`, `SCHEMA.md`, `PLAN.md` P6, 02 §2 | — | S, last |
| **F-P6a** forge | `forge/internal/depsurface`, `internal/linter/forgeconv/least_knowledge.go` | §8 | — | S, parallel |

**Parallelism:** after W1 (about a day), A, B, C, E and F run as five concurrent agents on
disjoint directories. D starts with them but merges after C. Z waits only on R6. Y waits on
everything. Wall-clock is about a week; C is the critical path.

**Every lane's proof** (in its PR body):
```bash
go build ./... && go vet ./<owned>/...
make forge-generate-check                    # deps.go → mock_gen.go written and committed; compose.go untouched
git diff --exit-code origin/main -- internal/app   # every lane except Z
GOWORK=off go run github.com/reliant-labs/forge/cmd/forge lint --conventions --scope <owned>   # pinned forge, not the embedded one (§8)
make test-affected
DATABASE_URL='postgres://postgres:postgres@localhost:5433/<your_db>?sslmode=disable' REQUIRE_TEST_DB=1 \
  go test ./<owned>/...                       # full mode once, own DB, never 5434
# the guard: no type use of db.Repository left in what you own (comments allowed)
rg -n 'db\.Repository\b' <owned> | rg -v ':\s*//' ; test $? -eq 1
```
Z adds: `go test -race ./internal/app/...` (the `PlanAPI`/`PlanWorker`/`PlanGateway`
stop-order pins must be unchanged, `db_repo` included), an unscoped `forge lint` (graph
lane), and `forge generate` reporting no change to `graph_gen.go` beyond the new node.
Y's proof **is** the compiler: deleting the interface builds only when no consumer is left.
Then `make test-affected TEST_FULL=1`.

**Tracker:** non-test `db.Repository` fields and params: **223 today → 0 before Y**. Count
them with the scan, or quickly with
`rg -n --type go -g '!*_test.go' '\bdb\.Repository\b' internal tools | rg -v ':\s*//' | wc -l`.
That grep is approximate (it counts the type wherever it appears, which is what you want
for a 0 check).

**Conflicts with in-flight work:**
- **R6** (`internal/app` + `cmd`: delete the `lint.rules` graph overrides, the `server`
  shim, `serverapi`/`serverworker` leftovers, `main.go`). Only Z touches `internal/app`, so
  Z goes after R6 (and rebases on it). No other lane edits `internal/app` or `cmd`. R6
  turning `forge-graph-unreachable` back to error doesn't interact with P6.
- **P7 svcerr sweep** (1,569 `connect.NewError` in `internal/`; in the lanes' packages:
  chat 198, worktree 140, trigger 88, settings 54, preset 24, tools_daemon 12,
  streaming 3).
  - Rule 5.1 keeps rpc bodies untouched. P6's edits in those packages are `service.go`
    (one type), a new `deps.go`/`mock_gen.go`, helper signatures (`func f(repo
    db.Repository, …)` lines) and tests.
  - Expected overlap is the helper-signature lines only.
  - Whichever lands second rebases. If both are scheduled at once, run P7 on the 7
    lane-A/B packages after A and B (each is 1–2 days).
- **Feature work in handlers/activities** (chat merged #1240/#1241 this week). Rebase
  daily. Conflicts are again limited to helper signatures and test fakes.
- **`forge generate` reconciliation.** Lanes add `deps.go` files, so `forge generate`
  writes new `mock_gen.go` files *only in that lane's directory*. Two lanes never write the
  same generated file. If `graph_gen.go` or `compose.go` shows a diff in a non-Z lane, that
  is a bug in the lane (a Deps key changed). Stop and look.

---

## 7. The end state of `internal/db`

`internal/db` stays: after P6 it is the **database component**, no longer "the Repository
interface plus everything else".

| Today | After P6 |
|---|---|
| `repository.go` — the 463-method `Repository` interface | **deleted** (Y) |
| `repo.go`, `repository_impl.go` (3,900 lines), `repository_*.go`, `chat_*.go`, `message_*.go`, `provider_accounts*.go`, `model_endpoints.go`, … — the `*Repo` methods | **stay**: the concrete persistence API (guards, multi-statement transactions, hub emission, sealing). Minus the 31 dead methods; the 10 internal-only ones unexported. A later, optional, consumer-invisible PR can split `repository_impl.go` into per-aggregate files (moves only) |
| `tx_bridge.go`, `RunTx`/`RunTxReadOnly`, `isRetryableError` | **stay** (§3) |
| `connect.go` (pools, the `DirectURL` session pool), `run_control_lock.go`, `provider_key_lock.go` (advisory locks) | **stay** |
| `migrate.go`, `migrate_plan.go`, `embed.go` (`db.Migrate`, `WaitForSchema`, `reliant db migrate status`) | **stay**: the migrator entry over forge's `migratekit` |
| `goose_bridge.go` + `internal/db/migrations/postgres/` (156 frozen goose files) | **stay until runbook 04 §9 holds** (every kept DB adopted, a support window for self-hosted users, a forge release shipped to prod). That's a separate `chore/p5a-remove-goose` PR and an owner call, not P6 |
| `migrationcheck/` (`cmd/check-migrations`) | stays |
| `models.go` (109 `type X = core.X` aliases), `internal/db/core/` (domain types with behaviour) | **stay** (08: names stay; `core` collapses into entities only after F12.6, one type at a time) |
| `core.ProviderAccountStore`, `core.RunEventStore` (embedded in `Repository`), `core.ConnectionStore`/`SecretsTx`/`IntegrationWriteStore` (returned by `Connections()`/`IntegrationWrites()`) | **deleted / moved to consumers** (Y): implementation-side interfaces; the accessors return their concrete stores |
| `vault_boot.go`, `api_key_sealing.go`, `settings_*.go`, `chat_update_*.go`, `content_block_payload.go` | stay |
| `testutil.go` (`SetupTestDB`, `NewTestRepo`, …), `dbtest/` | **stay**, plus `dbtest.RunTxInline` |
| `doc.go`, `SCHEMA.md` | rewritten: "the Repo is concrete; declare the role you need where you need it" |

08 had an optional end step: rename `internal/store` → `internal/db` once the hand layer is
gone. With A, the hand layer is permanent (it is the behaviour), so that step is **off**.
`internal/store` remains forge's ORM projection and `internal/db` the component over it.

---

## 8. Forge work and friction

- **F-P6a — `depsurface`: an escape into an interface-typed parameter or field adds that
  interface's method set to `Used`, instead of exempting the field.**
  - Today `f.Escapes` turns `forgeconv-deps-surface` off for exactly the fat-dependency
    case: all 7 fat handler `Deps.Repo` fields escape.
  - With the fix, a 463-method foreign interface of which the package plus its callees use
    74 is an **error**, as the rule intends (`depsSurfaceWideMethods = 10`).
  - `forge fix narrow-deps` can then emit the union, with the callee roles embedded.
  - Only escapes into a concrete type, `any`, a struct literal field or a return value keep
    the exemption.
  - Files: `internal/depsurface/depsurface.go` (`readUses`/the escape branch at :513),
    `narrow.go`, `internal/linter/forgeconv/least_knowledge.go`, tests beside them.
  - Lands in parallel. After Y it's what keeps a regression out (someone writing a fat local
    role and passing it around).
- **F-P6b (noted, not needed for P6):** least-knowledge only scans packages with a `Deps`
  struct (`FindComponentDirs`), so constructor-param holders (`llm/tools` 87 holds,
  `activities/handlers` 73) are invisible. Y's deletion is reliant's gate, but a forge user
  without that luxury has no lint.
- **Friction — embedded vs pinned forge:**
  - The `reliant forge` on this machine is `v0.1.47-0.20261020131546-09b648be1b39`, which is
    older than the pin (`…2472900c2b41`). Its lint can disagree with CI's.
  - The lanes run forge from the go.mod pin (`GOWORK=off go run
    github.com/reliant-labs/forge/cmd/forge …`, as `make forge-generate-check` does).
  - What forge should do: `reliant forge` should warn when `forge.yaml`'s `forge_version`
    differs from the embedded build (it already knows both).
- **Friction — `internal/handlers/mocks`:** forge generates 36 per-rpc service mocks that
  nothing in reliant imports. That's harmless output, but 36 files every `generate` keeps
  current for no consumer. Worth an opt-out knob or a lint hint in forge. Low priority.
- **No forge change is needed for the graph** (§2.3). `//forge:root-only` (#1095) and
  AMBIGUOUS-on-two-producers already give the right semantics. Hand-written keys plus
  reliant's guard test cover the single-wrong-producer case.

---

## 9. Risks, recovery, open questions

- **Behaviour risk is low:** every lane is a type change that compiles or doesn't. No SQL,
  transaction or composition changes until Z, and Z changes only which expression names the
  same `*db.Repo`.
- **Test-semantics risk.** Converting a partial fake to a mock can turn an
  "un-overridden call panics" test into one that sees an error and takes a different branch.
  A converted test must still fail when the behaviour it pins is broken: break it on purpose
  once, locally, per converted file group.
- **Recovery:** roll forward. Any lane reverts cleanly on its own, because `*db.Repo` still
  satisfies `db.Repository` until Y. Y is the only one-way step, and it lands last, when
  the compiler says nothing depends on the interface.
- **Open:**
  1. Owner: A (recommended) or B (§2.4).
  2. Lane A: inject `RunLifecycle`/`RunLauncher` into chat, or leave chat constructing them?
     Decide when R5's root reach is checked, since an api handler can't take a worker-only
     node.
  3. Y: the 34 methods only tests outside `internal/db` call stay on `*db.Repo` as fixture
     surface (recommended), rather than moving to `testutil.go`.

---

## How this was measured

A throwaway go/types scanner (`golang.org/x/tools/go/packages`, `Tests: true`, over
`./...` of reliant at `444afb7b`; 50 s) recorded:
- every method **selection** whose callee is a method of `db.Repository`, `*db.Repo` or a
  type in `internal/db` / `internal/db/core` (so calls through struct embedding count);
- every field and param typed `db.Repository` / `*db.Repo` / `*app.DBRepo`;
- every value of those types passed to a call or a composite-literal field, with the
  receiving field's type;
- every named interface outside `internal/db` that `types.Implements(*db.Repo, …)` with at
  least one Repository method.

The method classification in 1.1 is a body scan of the `func (r *Repo)` methods. "Dead"
was cross-checked by name with `rg` (that's how `DeleteAutomationToken`, called through an
anonymous interface field in `handlers/trigger`, was moved out of the dead list). `forge lint
--conventions --json --no-fix` was run with forge built from the pin. Re-run the scan
before Y. The numbers move with every merge.

---

## Appendix A — what each fat consumer calls (non-test, direct)

- `internal/handlers/chat` (74): CancelQueuedAgentMessage, CountMessagesInChat, CountMessagesInThread, CountQueuedAgentMessagesForThread, CreateChat, CreateChatUpdate, CreateContentBlock, CreateMessage, CreatePlan, CreateTask, CreateUserUpdate, DeleteChat, DeleteUndeliveredAgentMessage, EmitQuestionUpdate, EnqueueAgentMessage, EnqueueAgentMessageOnce, FindMessage, GetAgentMessage, GetAttachmentsByIDs, GetBasicStepExecutionsForChat, GetChat, GetChatWithUserCheck, GetContextWindow, GetLatestUpdateSequence, GetLatestUserUpdateSequence, GetMessage, GetNextOrdinal, GetNextSeq, GetPendingQuestionByChatID, GetProjectWithUserCheck, GetStepExecutionsForChat, GetThread, GetThreadWithParent, GetToolCall, GetToolResultBlock, GetUpdatesSince, GetWorkflow, GetWorkflowByThread, GetWorkflowDraftBySlug, GetWorktree, IsLatestTurnInThread, ListArchivedChats, ListChats, ListContentBlocks, ListContentBlocksForMessages, ListForkedThreadIDs, ListInheritedSpawnChildren, ListMessages, ListPlansByChatID, ListQueuedAgentMessagesForThread, ListStepExecutionRecordsForChat, ListTasksByPlan, ListThreadsByConversation, ListWorkflowsByChat, ListWorktrees, ListWorktreesByIDs, ResolveQuestion, ReviveThread, RunTx, SaveMessageToThread, SaveMessageToThreadOnce, SearchChats, SetChatAdopted, SetChatDaemonBlocked, SetChatPausedForMachine, SetChatQueuedForMachine, ThreadAwaitsReply, UnarchiveWorktree, UpdateChat, UpdateChatActiveDaemon, UpdateChatState, UpdateChatUnread, UpdateWorkflowName, UpdateWorkflowStatus
- `internal/handlers/streaming` (16): CountMessagesInChat, GetAttachmentsByIDs, GetChat, GetContextUsage, GetLatestNonMessageUpdatesPerEntity, GetLatestUpdateSequence, GetLatestUserUpdateSequence, GetTriggerEventByChat, GetUpdatesSince, GetUserUpdatesSince, ListContentBlocksForMessages, ListLiveToolCallsByChat, ListRecentTranscriptSiblingMessages, ListThreadsByConversation, ListToolCallsByIDs, SpawnToolCallIDsByChildThread
- `internal/handlers/tools_daemon` (21): CreateProject, CreateUserUpdate, EmitUserRefetch, GetDaemon, GetDaemonStorageState, GetProjectByPathAndUser, GetProjectByRemoteURLAndUser, ListProjects, MarkProjectDaemonInstalled, ReleaseDaemonAttachment, RenewDaemonAttachment, ResolveQueuedProjectDaemon, SetDaemonLocalModels, SetDaemonStorageState, UpdateDaemonAttachmentMemory, UpdateDaemonAttachmentPorts, UpdateProject, UpsertDaemon, UpsertDaemonAttachment, UpsertProjectConfigRecord, UpsertProjectDaemon
- `internal/handlers/worktree` (18): ArchiveWorktree, CreateWorktree, DeleteWorktree, EmitUserRefetch, GetChat, GetLiveWorktreeByName, GetProject, GetProjectWithUserCheck, GetWorktree, GetWorktreeByIdempotencyKey, ListChats, ListReposByProject, ListWorktrees, MergeWorktreeCleanupMetadata, UnarchiveWorktree, UpdateChatState, UpdateWorktree, UpdateWorktreeCleanupMetadata
- `internal/handlers/trigger` (16): CreateTrigger, DeleteTrigger, GetChat, GetProjectWithUserCheck, GetTrigger, GetTriggerEventByChatID, GetTriggerWebhookCredentials, GetWorktree, ListTriggerEvents, ListTriggerRegistrations, ListTriggers, RecentTriggerFirings, SetTriggerEnabled, SetTriggerWebhookSecret, SetTriggerWebhookTokenHash, UpdateTrigger (+ `automationGrants`' anonymous roles: SetAutomationToken, DeleteAutomationToken, ListTriggers)
- `internal/handlers/preset` (15): CreatePreset, CreateSetting, DeletePreset, GetPresetBySlug, GetPresetBySlugAndProject, GetProjectPresetsJSON, GetProjectWithUserCheck, GetSetting, ListHiddenItemDefaults, ListPresetsByTag, ListSettingsByKey, ListUserPresetsByProject, ListVisibilityOverrides, UpdatePreset, UpdateSetting
- `internal/handlers/settings` (11): CreateSetting, DeleteSetting, EmitUserRefetch, GetPrivacySettings, GetSetting, ListHiddenItemDefaults, ListSettings, ListVisibilityOverrides, SetPrivacySettings, SetVisibilityOverride, UpdateSetting
- `internal/workflow/runtime/activities/handlers` (76): ArchiveWorktree, CascadeTerminalStatusToDescendants, CascadeTerminalStatusToThreadSubtree, ClearProviderBackoff, ClearUsageLimitWait, CreateApproval, CreateAttachment, CreateChatUpdate, CreateContentBlock, CreateMessage, CreateQuestion, CreateUserUpdate, CreateWorkflow, DeleteWorkflowCheckpoint, EmitAgentMessagesDrainedUpdate, EmitChatRefetch, EmitQuestionUpdate, EmitStreamFinalizedUpdate, EmitToolCallUpdate, EmitUserRefetch, EnqueueAgentMessage, EnqueueSpawnReport, FiringsSinceLastSuccess, GetApprovalByEntityID, GetChat, GetContextUsage, GetNextOrdinal, GetNextSeq, GetProject, GetProjectPresetsJSON, GetQuestionByID, GetQuestionsByWorkflowStepIteration, GetThread, GetToolCall, GetToolCallResult, GetToolCallResultForMessage, GetToolResultBlock, GetTrigger, GetTriggerEventByChatID, GetWorkflow, GetWorktree, ListContentBlocks, ListInheritedSpawnChildren, ListMessages, ListPendingApprovalsByChat, ListQueuedAgentMessagesForThread, ListReposByProject, ListSpawnChildren, ListToolCallsByIDs, ListWorktrees, MarkAgentMessagesDelivered, MarkQueuedAgentMessagesUndeliveredForThread, PauseRunningWorkflowsByChat, RecordProviderBackoff, RecordUsageLimitWait, ResolveQuestion, ResumeWorkflowsByChat, ReviveThread, RunTx, SaveMessageToThread, SetAgentMessagesDeliveredMessageID, SetChatDaemonBlocked, SetChatPausedForMachine, SetChatQueuedForMachine, SetCompactionSummaryMessage, SetLaunchEventRunStatus, SetWorkflowOutcome, UpdateApprovalStatus, UpdateChat, UpdateChatState, UpdateChatUnread, UpdateThreadStatus, UpdateThreadWorkflow, UpdateWorkflowStatus, UpsertToolCallResult, UpsertWorkflowCheckpoint
- `internal/workflow/reconciliation` (26): CascadeTerminalStatusToDescendants, CascadeTerminalStatusToThreadSubtree, CompareAndSwapWorkflowStatus, DeleteWorkflowCheckpoint, EmitToolCallUpdate, EnqueueAgentMessageIfAbsent, GetChat, GetDaemon, GetPendingQuestionByChatID, GetThread, GetToolCall, GetWorkflow, GetWorkflowCheckpoint, ListBackgroundedProcessToolCalls, ListPendingApprovalsByChat, ListStrandedBackgroundSpawnToolCalls, ListStrandedSpawnToolCalls, ListThreadsWithOrphanedAgentMessages, ListWorkflowsByChat, ListWorkflowsByStatus, MarkQueuedAgentMessagesUndeliveredForThread, ReapOrphanedThreads, ReapOrphanedWorkflowDescendants, RunTx, SaveMessageToThread, UpsertToolCallResult
- `internal/llm/tools` (48): ArchiveWorktree, CountLiveLaunchedRuns, CountMessagesInThread, CreateAttachment, CreatePlan, CreateTask, CreateTaskDependency, CreateWorkflowDraft, CreateWorkflowScenario, DeleteTaskDependencyByPair, DeleteWorkflowScenario, EmitChatRefetch, EmitUserRefetch, EnqueueAgentMessage, GetAttachment, GetChat, GetLiveWorktreeByName, GetPlanByThreadID, GetTask, GetTaskByPosition, GetTaskStatsByPlan, GetThread, GetTriggerEventByChat, GetTriggerEventByDedupe, GetWorkflowDraft, GetWorkflowDraftByName, GetWorkflowDraftBySlug, GetWorkflowScenarioByName, LastThreadActivityByChat, ListChats, ListDependenciesByPlan, ListInheritedSpawnChildren, ListPendingApprovalsByChat, ListReposByProject, ListRuns, ListSpawnChildren, ListTasksByPlan, ListWorkflowDraftsByUser, ListWorkflowScenariosByDraft, ListWorktrees, PendingQuestionsByThread, ProviderBackoffByChat, UpdatePlan, UpdateTask, UpdateWorkflowDraftDefinition, UpdateWorkflowScenario, UpdateWorkflowScenarioResult, WorkflowSlugExists
- `tools/reliant-dev` (14): Close, GetChat, GetStepExecutionsByWorkflow, GetWorkflowCheckpoint, LastThreadActivityByChat, ListContentBlocksForMessages, ListMessages, ListPendingApprovalsByChat, ListQuestionsByChat, ListThreadsByConversation, ListWorkflowsByChat, ListWorkflowsByStatus, PendingQuestionsByThread, ProviderBackoffByChat
- `internal/workflow` (10): CascadeTerminalStatusToDescendants, CascadeTerminalStatusToThreadSubtree, EmitChatRefetch, GetWorkflow, PauseRunningWorkflowsByChat, ResumeWorkflowsByChat, ReviveSubtreeLiveAt, RunTx, SetChatPausedForMachine, UpdateWorkflowStatus
- `internal/bgprocess` (8): CreateBackgroundProcess, CreateBackgroundProcessOutputBatch, CreateUserUpdate, GetChat, GetProject, GetRunningBackgroundProcesses, GetWorktree, UpdateBackgroundProcessStatus
- `internal/toolexec` (5): DaemonLastSeenAt, IsDaemonAttached, IsDaemonIDAttached, ListAttachedDaemonIDsForUser, ListDaemonsByUserID
- `internal/repo` (4): CreateRepo, GetRepoByProjectAndPath, ListReposByProject, UpdateProject
- `internal/workflow/messageconv` (4): GetAttachment, GetContextWindow, ListContentBlocks, ListContentBlocksForMessages
- `internal/workflow/runtime` (4): CreateChatUpdate, CreateStepExecution, EmitNodeExecutionEvent, EnsureWorkflowRunning
- `internal/mcpconfig` (3): GetProject, GetProjectByPath, GetWorktreeByPath
- `internal/llm/tools/shell` (2): GetBackgroundProcess, UpdateBackgroundProcessStatus
- `internal/pkgmgr` (2): GetRunningBackgroundProcesses, MarkStaleProcesses
- `internal/workersetup` (2): Connections, GetChat
- `internal/configadapter` (1): GetProjectConfigRecord
- `internal/app` (8, composition): Close, Connections, EmitUserRefetch, IntegrationWrites, ListAttachedDaemonIDsForUser, ORM, Ping, SetUpdateNotifiers
- `internal/llm/drivers`, `internal/grpc`, `internal/workflow/runtime/activities`: no direct calls; they forward the repo (drivers → `modelprefs.LoadAll`, own helpers; grpc → `terminal.WithTerminalCheckouts`, `attachment.AttachmentContentHandler`, `mcpserver.NewAttachmentReadiness`, `connectorMCPRoutes`; activities → the 37 `handlers.New*Activity` and 4 `messageconv` functions)

## Appendix B — per-file method sets of the three largest consumers

**`internal/workflow/runtime/activities/handlers`** — 34 files, 76 distinct methods

| file | n | methods |
|---|---:|---|
| `workflow_status.go` | 21 | CascadeTerminalStatusToDescendants, CascadeTerminalStatusToThreadSubtree, CreateChatUpdate, CreateWorkflow, DeleteWorkflowCheckpoint, EmitChatRefetch, FiringsSinceLastSuccess, GetChat, GetThread, GetTrigger, GetWorkflow, PauseRunningWorkflowsByChat, ResumeWorkflowsByChat, ReviveThread, RunTx, SetChatPausedForMachine, SetLaunchEventRunStatus, SetWorkflowOutcome, UpdateChatUnread, UpdateThreadWorkflow, UpdateWorkflowStatus |
| `cleanup.go` | 13 | CreateChatUpdate, CreateContentBlock, CreateMessage, GetNextOrdinal, GetNextSeq, GetToolResultBlock, GetWorkflow, ListContentBlocks, ListMessages, ListPendingApprovalsByChat, ListToolCallsByIDs, RunTx, UpdateApprovalStatus |
| `execute_tools.go` | 12 | EmitToolCallUpdate, EmitUserRefetch, GetChat, GetContextUsage, GetProject, GetToolCall, GetToolCallResult, GetWorktree, ListReposByProject, RunTx, SetChatDaemonBlocked, UpsertToolCallResult |
| `call_llm.go` | 11 | ClearProviderBackoff, EmitToolCallUpdate, GetChat, GetProject, GetProjectPresetsJSON, GetWorktree, ListInheritedSpawnChildren, ListQueuedAgentMessagesForThread, ListReposByProject, ListSpawnChildren, RecordProviderBackoff |
| `compact.go` | 7 | CreateChatUpdate, CreateUserUpdate, GetChat, GetThread, RunTx, SetCompactionSummaryMessage, UpdateChat |
| `question.go` | 7 | CreateQuestion, EmitQuestionUpdate, GetQuestionByID, GetQuestionsByWorkflowStepIteration, ResolveQuestion, RunTx, UpdateChatUnread |
| `worktree.go` | 7 | ArchiveWorktree, EmitUserRefetch, GetChat, GetProject, GetWorktree, ListReposByProject, ListWorktrees |
| `approval.go` | 6 | CreateApproval, CreateChatUpdate, GetApprovalByEntityID, RunTx, UpdateApprovalStatus, UpdateChatUnread |
| `drain_agent_messages.go` | 6 | EmitAgentMessagesDrainedUpdate, GetThread, ListQueuedAgentMessagesForThread, MarkAgentMessagesDelivered, RunTx, SetAgentMessagesDeliveredMessageID |
| `preflight_daemon.go` | 5 | GetChat, GetProject, GetTriggerEventByChatID, SetChatDaemonBlocked, SetChatQueuedForMachine |
| `execute_tools_workspace.go` | 4 | GetChat, GetProject, GetWorktree, ListReposByProject |
| `load_workflow.go` | 4 | GetChat, GetProject, GetProjectPresetsJSON, GetWorktree |
| `machine_resume_wake.go` | 4 | GetChat, GetProject, GetTriggerEventByChatID, SetChatDaemonBlocked |
| `thread_worktree_resolve.go` | 4 | GetChat, GetThread, GetWorktree, ListWorktrees |
| `automation_archive.go` | 3 | GetChat, GetTrigger, UpdateChatState |
| `graduate.go` | 3 | GetChat, SaveMessageToThread, UpdateChat |
| `run_executor.go` | 3 | GetChat, GetProject, GetWorktree |
| `thread_status.go` | 3 | CreateChatUpdate, MarkQueuedAgentMessagesUndeliveredForThread, UpdateThreadStatus |
| `create_workflow_with_thread.go` | 2 | CreateChatUpdate, GetWorktree |
| `enqueue_agent_message.go` | 2 | EnqueueAgentMessage, EnqueueSpawnReport |
| `run_owner.go` | 2 | GetChat, GetWorkflow |
| `usage_limit_wait.go` | 2 | ClearUsageLimitWait, RecordUsageLimitWait |
| `db_helpers.go` · `emit_stream_finalized.go` · `emit_tool_status.go` · `execute_tools_batch_limit.go` · `git_publish.go` · `load_preset.go` · `run_step.go` · `save_message.go` · `thread_worktree.go` · `validate_thread_ownership.go` · `workflow_checkpoint.go` · `workflow_error.go` | 1 each | GetToolCallResultForMessage · EmitStreamFinalizedUpdate · EmitToolCallUpdate · GetToolCall · GetChat · GetProjectPresetsJSON · CreateChatUpdate · CreateAttachment · GetThread · GetThread · UpsertWorkflowCheckpoint · CreateChatUpdate |

`GetChat`/`GetProject`/`GetWorktree`/`ListReposByProject` recur in about 10 activities. A
shared unexported `workspaceReader` (4 methods), embedded by each activity's store, is the
one place DRY beats WET here.

**`internal/llm/tools`** — 21 files, 48 distinct methods

| file | n | methods |
|---|---:|---|
| `dependency_tool.go` | 8 | CreateTaskDependency, DeleteTaskDependencyByPair, EmitChatRefetch, GetPlanByThreadID, GetTask, GetTaskByPosition, ListDependenciesByPlan, ListTasksByPlan |
| `spawn_status.go` | 8 | CountMessagesInThread, GetThread, LastThreadActivityByChat, ListInheritedSpawnChildren, ListPendingApprovalsByChat, ListSpawnChildren, PendingQuestionsByThread, ProviderBackoffByChat |
| `task_tool.go` | 8 | CreateTask, EmitChatRefetch, GetTask, GetTaskByPosition, GetTaskStatsByPlan, ListDependenciesByPlan, ListTasksByPlan, UpdateTask |
| `plan_tool.go` | 7 | CreatePlan, CreateTask, CreateTaskDependency, EmitChatRefetch, GetPlanByThreadID, GetTaskStatsByPlan, UpdatePlan |
| `scenario_tools.go` | 7 | CreateWorkflowScenario, DeleteWorkflowScenario, GetChat, GetWorkflowScenarioByName, ListWorkflowScenariosByDraft, UpdateWorkflowScenario, UpdateWorkflowScenarioResult |
| `worktree_tool.go` | 6 | ArchiveWorktree, EmitUserRefetch, GetLiveWorktreeByName, ListChats, ListReposByProject, ListWorktrees |
| `generate_video.go` | 3 | CreateAttachment, GetAttachment, GetChat |
| `spawn_send.go` | 3 | EnqueueAgentMessage, GetThread, ListSpawnChildren |
| `start_run.go` | 3 | CountLiveLaunchedRuns, GetTriggerEventByChat, GetTriggerEventByDedupe |
| `workflow_editing.go` | 3 | CreateWorkflowDraft, UpdateWorkflowDraftDefinition, WorkflowSlugExists |
| `workflow_resolve.go` | 3 | GetWorkflowDraft, GetWorkflowDraftByName, GetWorkflowDraftBySlug |
| `add_task_tool.go` · `generate_image.go` · `plan_resolve.go` · `save_attachment.go` · `spawn_stop.go` | 2 each | CreateTask, EmitChatRefetch · CreateAttachment, GetChat · GetPlanByThreadID, GetThread · GetAttachment, GetChat · GetThread, ListSpawnChildren |
| `list_runs.go` · `read_attachment.go` · `run_tools.go` · `spawn_lineage.go` · `workflow_discovery.go` | 1 each | ListRuns · GetAttachment · GetChat · ListInheritedSpawnChildren · ListWorkflowDraftsByUser |

The per-file helpers `resolveWorkflowDraft` ×9, `resolvePlanForRead` ×6, `resolveRunCaller`
×6 and `validateWorkflowForTool` ×6 take the repo too. Each gets its own role, and the tools
that call them embed it.

**`internal/handlers/chat`** — 13 files, 74 distinct methods

| file | n | methods |
|---|---:|---|
| `chat_crud.go` | 29 | CountMessagesInChat, CountMessagesInThread, CreateChatUpdate, CreateUserUpdate, DeleteChat, GetAttachmentsByIDs, GetChat, GetLatestUpdateSequence, GetLatestUserUpdateSequence, GetThread, GetUpdatesSince, GetWorkflow, GetWorkflowDraftBySlug, GetWorktree, ListArchivedChats, ListChats, ListContentBlocksForMessages, ListMessages, ListPlansByChatID, ListThreadsByConversation, RunTx, SearchChats, SetChatAdopted, UnarchiveWorktree, UpdateChat, UpdateChatActiveDaemon, UpdateChatState, … |
| `chat_branch.go` | 22 | CreateChat, CreateContentBlock, CreateMessage, CreatePlan, CreateTask, CreateUserUpdate, GetChat, GetContextWindow, GetMessage, GetNextOrdinal, GetNextSeq, GetThreadWithParent, GetToolCall, GetToolResultBlock, GetWorktree, ListChats, ListContentBlocks, ListInheritedSpawnChildren, ListPlansByChatID, ListTasksByPlan, RunTx, SaveMessageToThread |
| `chat_send.go` | 17 | CancelQueuedAgentMessage, CreateChatUpdate, EmitQuestionUpdate, GetAgentMessage, GetPendingQuestionByChatID, GetThread, GetWorkflow, ListQueuedAgentMessagesForThread, ResolveQuestion, ReviveThread, RunTx, SaveMessageToThread, SaveMessageToThreadOnce, SetChatDaemonBlocked, SetChatQueuedForMachine, UpdateChat, UpdateWorkflowStatus |
| `chat_continue.go` | 9 | CountQueuedAgentMessagesForThread, GetChat, GetWorkflow, ReviveThread, SaveMessageToThread, SetChatPausedForMachine, SetChatQueuedForMachine, ThreadAwaitsReply, UpdateWorkflowStatus |
| `chat_workflow.go` | 8 | GetBasicStepExecutionsForChat, GetChat, GetStepExecutionsForChat, GetWorkflow, GetWorkflowByThread, ListForkedThreadIDs, ListThreadsByConversation, ListWorkflowsByChat |
| `chat_send_idempotency.go` | 7 | DeleteUndeliveredAgentMessage, EnqueueAgentMessage, EnqueueAgentMessageOnce, FindMessage, GetAgentMessage, IsLatestTurnInThread, ListContentBlocks |
| `chat_helpers.go` · `chat_resume_agent.go` · `chat_worktrees.go` | 3 each | CountMessagesInThread, GetChatWithUserCheck, SearchChats · CancelQueuedAgentMessage, GetThread, GetWorkflow · GetProjectWithUserCheck, ListWorktrees, ListWorktreesByIDs |
| `chat_step_executions.go` · `chat_model_fallback.go` · `chat_run_now.go` · `chat_selection.go` | 2, 1, 1, 1 | GetChat, ListStepExecutionRecordsForChat · SaveMessageToThread · SetChatAdopted · CreateUserUpdate |

## Appendix C — role interfaces `*db.Repo` already satisfies (95 in 61 packages)

| package | interfaces (method count) |
|---|---|
| `internal/analytics` | `PrivacyChecker` (1) |
| `internal/automationcred` | `KeyStore` (1) |
| `internal/chatselection` | `Store` (3) |
| `internal/connections` | `runOwners` (4) |
| `internal/daemonchoice` | `Reader` (2) |
| `internal/daemonplacement` | `WorktreeReader` (1) |
| `internal/daemonstate` | `DerivationRepository` (5) |
| `internal/filepreview` | `BasePathReader` (3) |
| `internal/handlers/approval` | `Repository` (12) |
| `internal/handlers/attachment` | `Repository` (3) |
| `internal/handlers/catalog` | `TagPrefsReader` (1) |
| `internal/handlers/daemon_registry` | `Repository` (5) |
| `internal/handlers/file_system` | `Repository` (5) |
| `internal/handlers/forge` | `Repository` (2) |
| `internal/handlers/inbox` | `Repository` (27) |
| `internal/handlers/mcp` | `Repository` (7) |
| `internal/handlers/message` | `Repository` (5) |
| `internal/handlers/package_commands` | `Repository` (5) |
| `internal/handlers/plan` | `Repository` (9) |
| `internal/handlers/project` | `Repository` (24) |
| `internal/handlers/provider_account` | `Store` (11) |
| `internal/handlers/question` | `Repository` (6) |
| `internal/handlers/repo` | `Repository` (7) |
| `internal/handlers/run` | `Repository` (7) |
| `internal/handlers/scenario` | `Repository` (11) |
| `internal/handlers/system` | `Pinger` (1) |
| `internal/handlers/task` | `Repository` (9) |
| `internal/handlers/terminal` | `terminalCheckouts` (2) |
| `internal/handlers/tool_call` | `Repository` (10), `chatReader` (1) |
| `internal/handlers/tools_daemon` | `daemonHostnameLookup` (1) |
| `internal/handlers/workflow` | `Repository` (20) |
| `internal/integrations/webhook` | `EventsStore` (3), `HooksStore` (2), `Store` (5) |
| `internal/integrations/writeledger` | `RunLookup` (4) |
| `internal/launch` | `ChatSelections` (3), `ChatWriter` (8), `ProjectLookup` (5), `ProjectWorkflowLoader` (1), `Store` (31), `Transactor` (1), `TriggerEventStore` (5), `WorkflowResolver` (6), `WorkflowStore` (3) |
| `internal/llm/accountlimits` | `benchStore` (2), `leaser` (1), `snapshotStore` (4), `usageStore` (6) |
| `internal/llm/drivers` | `AccountTokenStore` (4), `reliantKeyStore` (4) |
| `internal/llm/drivers/local` | `EndpointSource` (1), `Source` (3) |
| `internal/llm/tools` | `bugReportLookup` (4) |
| `internal/machinewake` | `Owners` (1) |
| `internal/mcpserver` | `AttachmentReader` (1) |
| `internal/messageproto` | `ToolCallReader` (2) |
| `internal/modelendpoints` | `Store` (7) |
| `internal/modelprefs` | `SettingsReader` (1) |
| `internal/projectfacts` | `ForgeNameWriter` (1), `ProjectUpdater` (1) |
| `internal/queueddelivery` | `Store` (3) |
| `internal/runs` | `Repository` (14) |
| `internal/servergateway` | `flowHealthAttachmentLister` (1) |
| `internal/temporal` | `SpawnWorkflowReconciler` (1) |
| `internal/threads` | `Repository` (43) |
| `internal/toolbindings` | `SettingsReader` (1) |
| `internal/triggers` | `AlertingStore` (5), `EventRepo` (6), `ProjectionStore` (2), `Repo` (8), `RunLineage` (1) |
| `internal/triggers/lineage` | `LaunchEventReader` (1) |
| `internal/triggers/runevents` | `Store` (5) |
| `internal/triggers/workflowevent` | `RelayStore` (3), `Repo` (4), `RunEventReader` (1) |
| `internal/workflow/lifecycle` | `WorkflowReader` (1) |
| `internal/workflow/runtime` | `durableResumeSource` (3), `liveBackgroundSpawnLister` (1), `stoppedSpawnReader` (1), `toolGrantLister` (1) |
| `internal/workflow/runtime/activities/handlers` | `daemonPlatformReader` (2), `greenfieldStore` (3), `preemptionStore` (1), `routingSettings` (1), `stoppedSubAgentReader` (1) |
| `internal/workflow/workflowcatalog` | `Reads` (2) |
| `internal/workflow/workflowsource` | `ProjectStore` (1), `Store` (3) |
| `internal/workspacecreate` | `Store` (5) |
| `internal/worktreesweep` | `AutoArchiveStore` (2), `StorageStore` (11), `Store` (10), `settingReader` (1) |

## Appendix D — Repository methods no non-test code outside `internal/db` calls (84)

- **Dead, delete in Y (31):** AppendContentBlockDelta, CountWorkflowDraftsByUser, CreateMessageIfNotExists, DeleteContextWindow, DeleteContextWindowsByThread, DeletePresetBySlug, DeletePresetBySlugAndProject, DeleteStepExecutionsByWorkflow, DeleteThread, DeleteThreadsByConversation, DeleteVisibilityOverride, DeleteWorkflow, DeleteWorkflowScenariosByDraft, DeleteWorkflowsByChat, GetFloatOrDefault, GetIntOrDefault, GetStepExecutionsByStep, GetStringOrDefault, GetThreadByWorkflow, GetWorkflowDraftBySourcePath, IsItemVisible, ListRootTasksByPlan, ListRootWorkflowsByStatus, ListTasksByParent, ListWorkflowScenariosByUser, MarkMessageBlocksComplete, SetFloat, SetInt, UpdateBackgroundProcessPID, UpdateContentBlock, UpdateMessageFields. (`DeleteAutomationToken` looked dead to the selection scan but is called through `handlers/trigger`'s anonymous `automationGrants.keys` interface, so it stays.) Y deletes them and their `internal/store` functions where nothing else uses those; the compiler confirms.
- **Only tests outside `internal/db` call them (34 by the selection scan).** A name check
  (`rg '\.<M>\('` over non-test, non-generated code) finds 7 of them reached in production
  through an interface `*db.Repo` does not satisfy as a whole (a wrapper such as
  `workersetup`'s `accountRouterStore`, an anonymous field, or a same-named rpc client).
  Those are **live; keep them**: GetThreadAssignment, SetThreadAssignment,
  RecentAssignmentCounts (`llm/accountrouter` via `workersetup/account_routing.go`),
  GetTriggerRegistration, UpsertTriggerRegistration (`triggers/poll.go`),
  SetAutomationToken (`handlers/trigger/trigger_automation.go`), ListApprovalsByChat
  (`cmd/reliant/cmd/workflow_{follow,supervise}.go`, which may be the rpc client, so Y
  verifies). The other 27 stay as fixture surface: AppendToContentBlock, AppendToolInputDelta, ClaimQueuedAgentMessagesForThread, CleanupOldBackgroundProcesses, CountThreadsInConversation, CreateContentBlockIfNotExists, GetBackgroundProcessOutput, GetContentBlock, GetDefaultPresetAssignments, GetPreset, GetRunEventByDedupe, GetStepExecution, GetTaskDependency, ListBackgroundProcesses, ListBlockersForTask, ListChildThreads, ListContextWindowsByThread, ListOutboundAttachments, ListPlansByThread, ListTaskDependenciesByTask, ListThreadsByOrigin, ListUserPresets, ListUserPresetsGlobal, RunTxReadOnly, SetString, UpdateMessage, UpsertPreset
- **Only `internal/db` calls them (10 by the scan), unexport in Y:** GetBoolOrDefault, GetContextWindowWithThread, GetCrashReportingEnabled, GetItemDefault, GetMaxContextSequenceInThread, GetVisibilityOverride, SetAnalyticsEnabled, SetBool, SetCrashReportingEnabled. GetThreadTokenCount is name-matched in `internal/threads/tokens.go`, so check it before unexporting. Y's rule: unexport, and keep it unexported only if `go build ./...` agrees.
- **Only `internal/db`'s own tests call them (8), delete or keep with their tests, Y decides:** DeleteTaskDependency, DeleteWorkflowDraft, ListPlansByProject, ListToolCallResultsByMessageIDs, UpdatePlanStatus, UpdateRepo, UpdateTaskStatus, UpdateThreadForkPoint
