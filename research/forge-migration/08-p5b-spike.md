# P5b spike: the forge ORM on reliant's five hairiest data paths

Status: **spike complete**, 2026-10-19. Branch `spike/p5b-orm` (reliant). Nothing is
merged, and production code imports nothing from it.
Inputs: `PLAN.md` D3 + P5b ("Spike first"), forge pinned at `v0.1.46-0.20261019044744-357579470b0d`.

## Verdict

**The forge ORM can carry reliant's hot paths, with no performance regression that
matters. It cannot be turned on for reliant today**: two forge gaps block it
(F12.1, F12.2), and three more would make a mechanical port silently wrong or
lossy (F12.3–F12.5). All five paths were ported and proven against real
Postgres, side by side with the sqlc versions and interleaved with them:

| # | Path | Generated store, no SQL | bun builder (`_repo_ext.go`) | Hand SQL in `_repo_ext.go` (`NewRaw`) | Correct vs sqlc |
|---|---|---|---|---|---|
| 1 | Message streaming + `message_content_blocks` | `CreateChatUpdate`, batch block read (`ListMessageContentBlock` + `WhereIn`/`WithOrderBy`), `CountMessage` | `AppendToContentBlock` | `SaveMessageAtomic`'s single statement (counters + message + `unnest()` blocks) | ✅ interleaved writers share one allocator; seq gapless under 8 mixed-layer writers |
| 2 | Computed chat state (`chats_with_activity`) | — | `ListChats` filters as composed `orm.QueryOption`s | `GetRootWorkflowStatusForChat` | ✅ 5 activity states, root status, 4 list filter shapes |
| 3 | `run_control_lock` | — | — (an `orm.Tx` + two raw `Exec`s) | the advisory lock statements | ✅ an ORM holder blocks a sqlc taker; ctx-cancel releases; refused inside a tx |
| 4 | Thread/workflow recursive cascades | fixtures (`CreateWorkflow`, `CreateThread`) | `WITH RECURSIVE … UPDATE … FROM` for both cascades + the recursive read | — | ✅ 40-row tree: identical end state per row |
| 5 | `SKIP LOCKED` outbox claim | `MarkRunEventDispatched` = `UpdateRunEventMasked` | `UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED) RETURNING *` | — | ✅ 8 mixed-layer claimers over 120 rows: each row exactly once |

Recommended shape for the full port, in one line: **generated stores for
key/filter CRUD, the bun builder where predicates are composed at runtime, and
`NewRaw` hand SQL for every statement whose value is that it is one hand-tuned
statement** (the CTE writes, the view-derived state, the locks). Every
`_repo_ext.go` query gets a pgtest test, as this spike's do.

## How the spike was built (reproducible)

1. **Projection.** The forge ORM only projects a table that a service proto
   declares CRUD rpcs for (F12.1), and always into `internal/db`, where 30 of the
   names collide (F12.2). So the pinned forge projected reliant's
   `db/migrations` in a **scratch project** (`forge project new scratch
   --service spike`, the 4 migrations copied in, one `Get<E>` rpc + empty
   message per table, `forge generate`), and the output was re-packaged
   unchanged except `package db` → `package store` into **`internal/store/`**:
   55 `<entity>_orm_gen.go`, `store_gen.go` (55 `<Entity>Store` interfaces +
   the aggregate `Store`), and the 55 scaffold-once `_repo_ext.go` seams. That
   is exactly what an ORM target package (F12.2) would emit. The generator
   script is in the appendix.
2. **Ports** live in the owned seams: `message_repo_ext.go`,
   `message_content_block_repo_ext.go`, `chat_repo_ext.go`,
   `workflow_repo_ext.go`, `thread_repo_ext.go`, `run_event_repo_ext.go`, plus
   `run_control_lock.go` (no table).
3. **Harness** (`internal/store/spike_harness_test.go`): `pgtest.NewFromTemplate`
   builds a template once per migration-set hash with reliant's own `db.Migrate`
   (goose bridge, then migratekit, the production path) and clones it per
   test. The clone is reopened with **pgx** (pgtest hands back lib/pq, F12.11),
   and **one pool** serves both `db.Repo` (sqlc/hand) and `orm.Client`, so a
   benchmark differs only in the data-layer code. A second pool on the same
   database runs pgx's `default_query_exec_mode=exec` (see F12.8). DB tests and
   benchmarks skip under `-short`.
4. **Run it:**
   `FORGE_TEST_POSTGRES_URL=postgres://postgres:postgres@127.0.0.1:<port>/postgres?sslmode=disable go test ./internal/store/ -count=1`
   (`-run '^$' -bench P -benchmem -benchtime=500x -count=6` for the
   benchmarks). The tests are `TestP0…TestP5…`, the benchmarks `BenchmarkP1…P5…`.

## Performance

benchstat, n=6 per cell, 500 ops per sample (P4: 200). `sqlc` is today's path
(sqlc or the hand SQL in `internal/db`). `orm` is the port on the same pgx pool.
`orm-exec` is the port on a pool with `default_query_exec_mode=exec`.

| Benchmark | sqlc | orm | orm-exec | B/op sqlc → orm | allocs/op sqlc → orm |
|---|---|---|---|---|---|
| P1 SaveMessageAtomic (3 blocks) | 5.50 ms | 7.97 ms (~, p=0.052) | 5.76 ms (~) | 23.5 K → 40.3 K (+71%) | 684 → 216 (−68%) |
| P1 AppendDelta (60 B delta) | 2.05 ms | 2.11 ms (~) | 2.53 ms (~) | 398 → 6 183 | 10 → 26 |
| P1 ListBlocks (20 msgs × 3) | 3.02 ms | **1.35 ms (−55%)** | 1.52 ms (−50%) | 112 K → 39 K | 1 410 → 1 298 |
| P2 GetChat + 40-row ListChats | 10.06 ms | **4.22 ms (−58%)** | 13.72 ms (+36%) | 142 K → 47 K | 1 094 → 907 |
| P3 run-control lock + release | 1.26 ms | 1.74 ms (+38%, p=0.041) | 1.31 ms (~) | 982 → 2 623 | 27 → 51 |
| P4 both cascades (13-node tree) | 5.91 ms | 6.66 ms (~) | 6.58 ms (~) | 600 → 15 168 | 15 → 73 |
| P5 ClaimRunEvents (10 of 1000) | 4.86 ms | 3.43 ms (~) | 2.64 ms (~) | 20.7 K → 15.0 K | 329 → 292 |
| P5 MarkDispatched | 1.21 ms | 1.91 ms (+59%) | 1.36 ms (~) | 360 → 9 776 | 10 → 102 |

How to read the table:

- **Latency is noisy.** The Postgres is a docker container shared with other
  agents' tests (confidence intervals of ±15–100%). Only the p<0.05 deltas
  count as findings. Bytes and allocs are deterministic.
- **No path regressed in a way users would see.** Statement execution
  dominates, and the shape of each statement is unchanged.
- **The ORM allocates more per statement** (+16 to +90 allocs). bun builds SQL
  text, and the generated masked update reads back every column it did not
  write (F12.10). It allocates *less* where sqlc scanned into
  `interface{}`/`sql.Null*` and then mapped into `core` types: ListBlocks, P2,
  and Save, where bun formats the arrays instead of `pq.*Array` valuers.
- **Part of the read wins goes away in a real port.** ListBlocks and P2 compare
  sqlc-plus-`core`-mapping against raw entity rows. A port that still returns
  `core.Chat`/`core.MessageContentBlock` pays some of that mapping back. P2's
  win is also a **query-shape win**: the ORM sends only the predicates a filter
  uses, where sqlc's `(narg IS NULL OR col = narg)` makes one plan cover every
  filter.
- **The pool mode has a real trade-off** (F12.8). bun interpolates every
  argument into the SQL text. Under pgx's default `cache_statement`, any
  statement whose values vary is a new prepared statement each call: an extra
  Parse/Describe round trip, plus server-side statement churn. That accounts
  for MarkDispatched's +59%, which is gone in exec mode. But exec mode gives up
  plan caching, and the planning-heavy view read (P2) becomes 3.2× slower than
  the ORM in default mode. Neither mode is right for every query; the fix
  belongs in forge.

## Per path

### 1. Message streaming + `message_content_blocks`

- **Native.**
  - The chat_update ledger insert goes through the generated `CreateChatUpdate`,
    inside the transaction.
  - The post-commit publish moved from reliant's `runAfterCommit` to
    `orm.AfterCommit`. It fires once, after commit, and never on rollback
    (`TestP1SaveMessageAtomic_RollsBackWithoutCommitHook`).
  - The read path's batch block load is just
    `ListMessageContentBlock(ctx, db, orm.WhereIn(message_id, ids), orm.WithOrderBy(message_id), orm.WithOrderBy(position))`.
  - `RunTxWithOptions(IsolationReadCommitted)` replaces reliant's READ COMMITTED
    `RunTxWithOptions`, and adds orm's 40001/40P01 retry.
- **Builder.** `AppendToContentBlock` is
  `NewUpdate().Model(block).Set("content = COALESCE(content, '') || ?", delta).Set("version = version + 1")…`.
  Quotes, backslashes and `?` inside a delta survive bun's interpolation (pinned
  in `TestP1AppendToContentBlock_InterleavesWithSQLC`).
- **Hand SQL, deliberately.** `SaveMessageAtomic`'s one statement (both counter
  upserts, the message insert, all blocks through `unnest()`) moved
  byte-for-byte into `NewRaw`, with `$N` rewritten to bun's positional `?N-1`
  and the 14 arrays passed as `pgdialect.Array`. The statement exists so that
  locks are held for one statement, not 5+N round trips, and no builder chain
  expresses that more clearly than the SQL itself.
- **Gaps.**
  - `message_order_counters` and `update_stream_counters` have composite PKs, so
    they get no entity at all (F12.3). They are only written inside the raw
    statement, so this path doesn't feel it.
  - The projected block type is `int64`, not `reliantv1.ContentBlockType`
    (F12.6).
- **Perf.** Save and append are at parity (allocs −68% / +16), and the batch
  read is faster.
- **Port shape.** As spiked.

### 2. Computed chat state

- **Native.** `ListChats`'s optional filters become `orm.QueryOption`s, appended
  only when set. A `QueryOption` is just `func(*bun.SelectQuery)`, so it applies
  to any select, including a select from a view.
- **Hand-written.** forge projects BASE TABLEs only (F12.7), so
  `ChatWithActivity` (the generated `Chat` embedded, plus the 11 computed view
  columns) is hand-typed in `chat_repo_ext.go`. It is selected with
  `chats_with_activity.*`, because the view lacks
  `chats.paused_for_machine_at`: a column list derived from the struct would
  fail. `GetRootWorkflowStatusForChat` is a `NewRaw` scan into typed `int64`s.
  sqlc typed its `CASE` columns `interface{}` and needed `coerceInt32`, so this
  is a small improvement.
- **The view stays SQL.** It is the one definition of "activity" and
  "list_in_sidebar"; nothing about the ORM argues for moving it into Go.
- **Perf.** −58% on the combined read (see the caveat above).
- **Port shape.** As spiked. When F12.7 lands, drop the hand row type for a
  projected read-only view entity.

### 3. `run_control_lock`

- **Expressed** as `client.BeginTx(READ COMMITTED)` on an `orm.Tx`, then
  `SET LOCAL lock_timeout` and `pg_advisory_xact_lock`, with release = rollback.
  The lock is cross-layer compatible: the hash key is unchanged, so a lane-by-lane
  cutover never has two critical sections running at once (proven).
- **Gaps (F12.9).**
  - `orm` has no "is ctx inside a transaction?" probe. The spike uses
    `orm.AfterCommit(ctx, noop) == nil` as one, which is a hack.
  - `orm.Context` cannot open a transaction that outlives the call (only
    closure-scoped `RunTx*`), so this function needs the concrete
    `*orm.Client`, not the handle the generated stores take.
- `forge/pkg/lease` is a process-level singleton (a session lock plus
  heartbeat), not a per-request critical section, so it does not fit here.
- **Perf.** Four round trips either way. +24 allocs from bun's `Tx` wrapper;
  +38% latency in default mode, within noise in exec mode.
- **Port shape.** An ext function taking `*orm.Client`, as spiked.

### 4. Thread/workflow cascades

- **Builder, no raw SQL.**
  `NewUpdate().WithRecursive("descendants", anchor.UnionAll(step)).Model(…).ModelTableExpr("workflows AS t").TableExpr("descendants").Set(…).Where(…)`
  renders exactly the sqlc statement:
  `WITH RECURSIVE "descendants" AS ((SELECT …) UNION ALL (SELECT … JOIN descendants …)) UPDATE workflows AS t SET … FROM descendants WHERE …`.
  A parameter-only CTE anchor is `NewSelect().ColumnExpr("?::text AS id", id)`.
  Recursive deletes are `ON DELETE CASCADE` foreign keys in the schema, so they
  need no code.
- **Ergonomics.** The builder buys little here: the CTE has no model, columns
  are strings, and the self-reference is a `TableExpr`. Prefer `NewRaw` for
  multi-CTE statements, so reviewers read SQL. Use the builder when predicates
  are composed at runtime.
- **Found here: F12.5.** Fixtures written with the generated `CreateThread`
  stored `status = 0`, although the column is `NOT NULL DEFAULT 2`.
- **Perf.** Latency at parity. 600 B → 15 KB per op for building two CTE
  strings.

### 5. `SKIP LOCKED` queue claim

- **Builder, no raw SQL:**
  `NewUpdate().Model(RunEvent).Set("claimed_until = ?").Where("id IN (?)", NewSelect()…OrderExpr("created_at, id").Limit(n).For("UPDATE SKIP LOCKED")).Returning("*").Scan(ctx, &rows)`.
  `RETURNING *` scans straight into the generated `RunEvent`.
- **Native.** `MarkRunEventDispatched` is `UpdateRunEventMasked(…, []string{RunEventFieldDispatchedAt})`.
  It costs 10 → 102 allocs, because the masked update RETURNs every column it
  did not write (F12.10).
- **Gaps.** `payload jsonb` projects as `string` and the CHECK-constrained
  `outcome` as `string`. `core.RunEvent`'s `map[string]any` payload and typed
  outcome therefore still need a conversion (F12.6).
- **Port shape.** Builder for the claim. Mark through the generated masked
  update once F12.10 adds a no-read-back option; until then, a one-line hand
  UPDATE.

## F12 — forge work items

Each item lists what forge did, what it should do, and the command and code to
start from. Blockers first.

| # | Blocks P5b? | Gap | Evidence: command / file | Forge should |
|---|---|---|---|---|
| **F12.1** | **Yes** | The ORM projects a table only when a service declares CRUD rpcs for it. In reliant every CRUD-named rpc is `// forge:custom`, and most tables (`message_content_blocks`, `threads`, `workflows`, `run_events`, `context_windows`, `chat_updates`, …) have no CRUD rpc at all, so `forge generate` projects **zero** entities | `forge generate` in reliant writes no `internal/db/*_orm_gen.go`. Gate: `stepInternalDBORM` (`internal/cli/generate_pipeline.go:1979`) ← `codegen.ParseEntityProtos` (`internal/codegen/parser_stubs.go:251`) | Decouple the store from the wire. **Project every applied BASE TABLE** as entity + store + delegates (opt-out per table, e.g. `COMMENT ON TABLE t IS 'forge:no-orm'`). CRUD *handler* wiring stays rpc-driven, as now. "Tables without CRUD rpcs are plain schema" is the wrong default for the data layer |
| **F12.2** | **Yes** | The ORM always lands in `internal/db`, `package db`, and refuses a name that `internal/db` already declares (#713). 30 of reliant's 55 projected entities collide: `Chat`, `Message`, `Workflow`, `Thread`, `Task`, `Project`, `Worktree`, `Trigger`, `Daemon`, `ModelEndpoint`, … and `Repo` (the `repos` table vs reliant's Repository struct) | `internal/generator/plan_orm_gen.go:47` (`dbDir`), `:480` (`package db`), `store_gen.go` likewise | An **ORM target package**: `database.orm.package: internal/store` in forge.yaml (one key, not a mode). Every consumer honors it: `_repo_ext.go` seam, `store_gen.go`, the stale-ORM sweep, the drift guard, CRUD-ops imports, `forge project shapes --kind store`. See "Entity-name collisions" below for the alternative |
| **F12.3** | Yes, for counters and join tables | A composite-PK table gets no entity at all, only an info line. reliant has 6: `message_order_counters`, `update_stream_counters`, `connection_secrets`, `inbox_dismissals`, `project_daemons`, `thread_provider_accounts` | `internal/codegen/schema_entities.go:116` ("CRUD projection and ULID id-generation are skipped; the table is plain schema") | Project the struct, `List`/`Count`, and Get/Delete/Upsert keyed by the PK tuple. Skip only CRUD-rpc wiring and ULID generation |
| **F12.4** | Yes (6 tables) | An entity's table is found by `Pluralize(snake(MessageName))`. Singular-named tables match no message: `background_process_output`, `connector_audit_log`, `daemon_attachment`, `integration_access_refresh`, `integration_event_access`, `provider_backoff`. Their `Get<E>` rpcs silently became stubs | Scratch `forge generate`. Naming at `plan_orm_gen.go:433` | With F12.1, name entities **from the table** (singularize). Never silently stub a CRUD-shaped rpc whose entity has no table: warn, naming the table it looked for |
| **F12.5** | Yes (silent wrong data) | For `NOT NULL` non-timestamp columns the generator deliberately emits no bun `default:` tag, so `Create<E>` writes the Go zero and the column `DEFAULT` never fires. `threads.status DEFAULT 2` is stored as 0. A `jsonb NOT NULL DEFAULT '{}'` projects as `string`, so the zero `""` fails the insert (22P02). Reliant has ~90 NOT NULL non-timestamp DEFAULTs; at least 30 differ from the Go zero (`1`, `2`, `true`, `'{}'::jsonb`, `'{}'::text[]`, `'[]'`, `'draft'`, `'user'`, `'owner'`, `'llm'`, `'installed'`, `'deny'`) | `bunTag` (`plan_orm_gen.go:737`). Pinned by `TestP0CreateIgnoresNotNullDefaults` | Generate `New<Entity>()` pre-filled with every literal-evaluable column default (and document that `Create` writes what the struct says). Or have `crud.Repo.Create` omit a defaulted NOT NULL column the caller did not set, which needs set-tracking. Constructor first |
| F12.6 | No (decides whether `core` types collapse) | Projected types are not idiomatic and are lossy against reliant's domain types: `Id`/`UserId` instead of `ID`; `int4` → `int64`; `jsonb` → `string`; enum-like CHECK text → `string`; reliant's 0/1 `bigint` flags (`chats.unread`) → `int64` | `naming.ColumnGoName`; `crud_convert.go:1176` ("JSON/JSONB … project to string") | Go initialisms; `int4` → `int32`; `jsonb` → `json.RawMessage`. A per-column Go type via `COMMENT ON COLUMN … 'forge:go-type=reliantv1.ContentBlockType'` (enum) or `'forge:go-type=map[string]any'` |
| F12.7 | No (hand row type works) | Views are not projected: `schemadef` enumerates `table_type = 'BASE TABLE'` | `pkg/schemadef/schemadef.go:1010` | Opt-in read-only view entities (`COMMENT ON VIEW v IS 'forge:entity'`): struct + Get/List/Count, no writes |
| F12.8 | No (perf, and the decision is forge's) | bun interpolates arguments into SQL. Over pgx's default `cache_statement` (neither `serverkit.OpenDB` nor `orm.NewClient*` sets a mode), every distinct argument value prepares a new statement: an extra round trip plus server-side churn of up to 512 statements per connection. Exec mode removes that but loses plan caching | `BenchmarkP5MarkDispatched` (+59% → ~), `BenchmarkP2ChatState` (exec +225% vs default) | Choose and document the pool mode for ORM traffic. Durable fix: `crud.Repo`'s by-PK paths (Get/Update/Delete, the highest-cardinality statements, all forge-owned) issue bind-parameter SQL so the text is stable. Add the benchmark to forge |
| F12.9 | No | No `orm.InTx(ctx)` probe. `orm.Context` has no way to open a transaction that outlives a call | `store.LockChatRunControl` (`AfterCommit == nil` hack, needs `*orm.Client`) | `orm.InTx(ctx) bool`. Document the detached-transaction pattern (or add `Client.BeginDetached`) |
| F12.10 | No | A masked update always RETURNs every unwritten column. A one-column stamp costs 10 → 102 allocs and 360 B → 9.8 KB | `pkg/crud/repo.go:1153` | `crud.NoReadBack()` update option, or an exec-only `Update<E>Columns` |
| F12.11 | No | `pgtest` opens **lib/pq** while production is pgx (`serverkit.OpenDB`), so DB tests run on a different driver (arrays, time scanning and error types differ) | `pkg/pgtest/template.go:55`, `pgtest.go:272` | Open pgx, or take the driver name |
| F12.12 | No (porting hazard) | Two placeholder dialects on one API. The raw passthroughs (`Client/Tx.Exec/Query/QueryRow`) take `$N`. `Bun()` builders and `NewRaw` take `?`/`?N`, and treat **every** `?` in the SQL text as a placeholder, including in comments and the jsonb `?`/`?|`/`?&` operators. Porting 359 sqlc queries means rewriting `$N` → `?N-1` (mechanical, as done for `SaveMessageAtomic`) | spike | Document it in `db/crud-overrides`. A lint flags `$1` inside `NewRaw`/`bun.Raw` strings and an unescaped jsonb `?` |
| F12.13 | No | Test ergonomics. `testkit.NewMigratedPostgresDB` takes `*testing.T`, so benchmarks can't use it. `pgtest.NewFromTemplate` templates persist on an external `FORGE_TEST_POSTGRES_URL` server forever ("dies with the server"), which leaks on shared servers | spike harness | Accept `testing.TB`. Reap templates by age/key on an external server |

What did **not** need forge work: recursive CTEs, `UPDATE … FROM`,
`FOR UPDATE SKIP LOCKED`, `RETURNING *` into entities, advisory locks,
`unnest()` array inserts (`pgdialect.Array`), READ COMMITTED transactions,
after-commit hooks, constraint names, and `NotFound` mapping. bun and `orm`
already cover all of them.

## Entity-name collisions with `internal/db` — ⚖️ owner decision

**Background:** 30 of the 55 projected entity names are already declared in
`internal/db`: 27 are aliases to `internal/db/core` types (`type Chat = core.Chat`
in `models.go`), plus `Daemon` (a struct), `ModelEndpoint` (`model_endpoints.go`)
and `Repo` (the Repository implementation, `repo.go:115`). The colliding names
are referenced **6,105 times in 631 files** outside `internal/db`. The top
references are `db.Message` 491, `db.Project` 444, `db.Chat` 405 and
`db.Workflow` 372.

**Option A — ORM target package (F12.2), recommended.**
- Generated rows live in `internal/store` (`store.Chat`, `store.ChatStore`).
- `internal/db` keeps the hand Repository and the `core` aliases, and shrinks
  one lane at a time until it is deleted.
- *For:*
  - Zero churn at the 631 call sites.
  - Lanes port in parallel without one giant rename conflicting with every
    in-flight branch.
  - Generated rows and domain types stay visibly distinct while both exist.
  - Any brownfield forge user adopting the ORM gradually needs the same
    primitive.
- *Against:*
  - Forge work: one key, but every forge feature that assumes `internal/db`
    has to honor it.
  - Departs from forge's convention that `internal/db` is the ORM.
- *Optional end step:* once the hand layer is gone, rename `internal/store` →
  `internal/db` in one move and drop the key.

**Option B — vacate the names in `internal/db`, then project there.**
- A mechanical rewrite of the 6,105 `db.X` → `core.X` references.
- Real renames for `Repo`, `Daemon`, `ModelEndpoint` and the other non-alias
  types.
- *For:*
  - No forge work.
  - Forge's convention kept.
- *Against:*
  - One huge cross-cutting PR (gofmt/gopls-able, but it touches 631 files that
    other agents are editing).
  - `db.Chat` silently changes meaning from domain object to row, which is a
    trap for every agent and memory that knows the old meaning.

**Collapsing `core` into the generated entities is a separate, later decision,
and today the answer is no.** The projected types are shape-incompatible with
the domain types (F12.6): `Id` vs `ID`, `int64` vs `reliantv1` enums, `string`
vs `map[string]any` payloads, `int64` vs `bool` flags, view-computed fields.
Collapse a type when, after F12.6, its entity says the same thing. Keep `core`
types that carry behavior (`ThreadStatusForStopReason`, `ComputeStreamingState`,
chat activity semantics) as domain types. Until then, the ext layer maps entity
→ `core` at the consumer-facing boundary.

## The 52 CRUD-name collisions as a class — ⚖️ owner decision

Today 60 CRUD-shaped rpcs carry `// forge:custom`:

- Chat, Message, Attachment: Get/Update/Delete/List
- Project, Worktree, Setting, Task, Trigger, Plan, ModelEndpoint: the quintet,
  or most of it
- Connection, ProviderAccount, Workflow: Get/Delete/List or Update/Delete/List
- Scenario, Server: Create/Get/Delete/List
- Parent-scoped lists: `ListConnectionEvents`, `ListTriggerEvents`,
  `ListStepExecutions`, `ListProjectDaemons`, `ListRepos`

52 of them are the true collisions (table + message + name).

**Recommendation: opt out as a class. Keep `// forge:custom` on all of them,
and have the handlers call the generated stores and ext functions.** Why:

1. **The wire shapes are not forge CRUD shapes.** Examples:
   - `UpdateSettingRequest` is keyed by `key` plus optional patch fields.
   - `UpdateTaskRequest` uses `task_id` plus optional fields, not
     `update_mask`.
   - `UpdateModelEndpointRequest`/`UpdateTriggerRequest` carry a nested
     `*Input`/`*Definition`.
   - The List requests are parent-scoped with no pagination.

   Adopting forge CRUD means wire changes across the web app, the CLI and
   control-plane.
2. **reliant's UI is its own SPA**, so the generated pages and nav (half of
   forge CRUD's value) go unused.
3. **With F12.1, P5b no longer depends on this decision.** The store layer
   comes from the tables, not the rpcs. The opt-out then means only "this rpc's
   handler is ours", which is true for every one of them: chats, messages,
   worktrees and projects carry real side effects, and settings/triggers carry
   privacy and registration logic.

**The trade-off for the owner:** the config-like aggregates (Setting, Task,
Plan, ModelEndpoint, Trigger, Preset) could be reshaped to AIP CRUD and adopt
forge CRUD with `handlers_crud.go` overrides (`op.Filters/Fetch/Persist`). That
buys generated handlers, lifecycle tests and uniform paging/filtering, at the
cost of a wire break in web and CLI and of moving their business rules into
overrides. Recommended: not as part of P5b. If wanted, it is an API-v2
decision of its own.

## Lane plan for the full port

**Lane 0 — forge (serial, first):**
- F12.1–F12.5 (the blockers).
- The F12.8 pool-mode decision, plus the PK-path bind parameters.
- Then F12.6, F12.7, F12.9–F12.13, which can trail the reliant lanes.

**Prep — reliant (1 PR, after the re-pin):**
- Turn the ORM on into `internal/store` and delete the scratch-projection
  hack.
- Promote `spike_harness_test.go` to a shared `internal/store/storetest`.
- Write the porting rules into `internal/db/SCHEMA.md`:
  - `$N` → `?N`;
  - set every defaulted NOT NULL column (until F12.5);
  - map entities to `core` at the boundary;
  - one pgtest test per ext query, interleaved with the sqlc twin until the
    twin is deleted.

**Aggregate lanes — parallel, disjoint file ownership.** Each lane owns its
`internal/db/postgres/queries/*.sql`, its `*_store.go`, its `internal/store/*_repo_ext.go`,
its Repository methods, and the narrow consumer-side interfaces that replace them
(the P6 rule: least knowledge, never the whole Repository):

| Lane | Tables / query files (sqlc queries) | Notes |
|---|---|---|
| L1a stream | messages 27, content_blocks 7, chat_updates 3, tool_calls 19, llm_context 4, context_usage 3 | `SaveMessageAtomic` as spiked; the composite-PK counters need F12.3 |
| L1b conversation | chats 18, threads 17, context_win 11, agent_messages 16, inbox 4 | the view row type (F12.7); cascades as spiked |
| L2 workflow/run | workflows 22, workflow_drafts 18, workflow_scenarios 11, workflow_checkpoints 3, step_executions 8, runs 2, run_events 9 | `SKIP LOCKED` as spiked; `run_control_lock` |
| L3 project/worktree | projects 11, worktrees 16, repos 7, project_daemons 8, visibility 12, command_favorites 4 | `project_daemons` is composite-PK (F12.3) |
| L4 triggers | triggers 34 (+ `trigger_store.go` 827 LOC) | trigger events and registrations |
| L5 connections/integrations | `connection_store.go` 572 LOC, integration_access 9, integration_writes 5 | `connection_secrets` is composite-PK; `connector_audit_log` is singular (F12.4) |
| L6 accounts/tokens | `provider_accounts_store.go` 1,141 LOC (all hand SQL), the auth-token tables, api_keys, access_tokens, vault | the heaviest hand SQL; sealed columns |
| L7 config | settings 8, presets 13, plans 7, tasks 5, task_dependencies 7, approvals 6, attachments 5, model endpoints | the most generated-store coverage |

**Finish (serial, last):**
- Delete the sqlc `generate.hooks` entry, `sqlc.yaml`, `db/sqlc.yaml`,
  `internal/db/postgres/` (`pgdb` + stores), the `make sqlc` target, the sqlc
  buildmode/array-param tests and the sqlc tool pin.
- Collapse what remains of `internal/db` into `internal/store`, or delete it.
- P6's Repository split is then done against the stores.

## Effort estimate

**Measured inputs:**
- 359 sqlc queries across 34 files, plus 268 hand-SQL call sites in
  `internal/db` and its stores (`ExecContext`/`QueryContext`).
- 528 `Repo` methods; 448 on the `Repository` interface.
- ~8.3K LOC of `*_store.go`.

**Spike ratio.** Of the 13 statements ported, 3 were generated delegates, 7 the
builder and 3 `NewRaw`. These are the hairiest paths; across the whole layer,
expect more generated delegates (by-id gets, simple lists and stamps), roughly
⅓ / ⅓ / ⅓. Each statement took one ext function plus one pgtest test.

| Work | Estimate |
|---|---|
| Lane 0: forge F12.1–F12.5 + F12.8 | 5–8 agent-days; F12.1 and F12.2 are the design-heavy ones |
| Prep | 1 agent-day |
| L1a–L7 (8 lanes, ~630 query sites + consumer interfaces) | 3–5 agent-days each, so 25–40 agent-days; ~1.5–2 weeks wall-clock in parallel |
| Finish | 1–2 agent-days |
| **Total** | **~32–50 agent-days; ~3 weeks wall-clock with 8 parallel lanes after Lane 0** |

PLAN's "L" stands.

**Risks:**
- Consumer churn, if lanes return entities instead of `core` types. Mitigation:
  map at the boundary.
- F12.1/F12.2 design time in forge.

## Decisions for the owner — decided 2026-10-19

The owner accepted every recommendation:
- **Entity-name collisions:** forge grows an ORM target-package option (F12.2); reliant's `internal/db` names stay.
- **The 52 CRUD-name collisions:** all stay `// forge:custom`. Reshaping the config-like APIs is a separate API-v2 question, not part of P5b.
- **Pool mode for ORM traffic:** keep pgx's default statement cache and fix forge's by-PK paths to bind parameters (F12.8).

The spike's proof code stays on branch `spike/p5b-orm` (not merged): the port lanes rebuild it on the forge fixes.

## Appendix — regenerate the projection

```bash
FORGE=~/.cache/fm-forge-bin/<forge_version>/forge
$FORGE project new scratch --mod example.com/scratch --service spike \
  --disable ci,deploy,observability,hot_reload,frontend
cp reliant/db/migrations/*.up.sql scratch/db/migrations/
# One `rpc Get<E>(Get<E>Request) returns (Get<E>Response);` plus an EMPTY
# `message <E> {}` per CREATE TABLE in the migrations (<E> = CamelCase of the
# singularized table; an `id` field mis-types the bigint-PK tables), then:
(cd scratch && FORGE_TEST_POSTGRES_URL=<maintenance dsn> $FORGE generate)
for f in scratch/internal/db/{*_orm_gen.go,store_gen.go,*_repo_ext.go}; do
  sed -e 's/^package db$/package store/' -e '/^\/\/ forge:hash=/d' "$f" > reliant/internal/store/$(basename "$f")
done
```
