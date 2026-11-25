# Chat loading slow (prod), 2026-11-18 — investigation briefing

Owner report: "chat loading has been slow again... check traces". Investigation
started 2026-11-18 ~20:30Z. This file is the shared briefing for sub-agents;
append findings under "Findings" with your section name.

## How to query (settled — do not rediscover)

Run from `control-plane/` (its `prod` env is the cloud backend for reliant's
services): `reliant forge obs <verb> prod ...`. A version-skew banner on stderr is
harmless. Useful verbs: `endpoints --since/--from/--to --top 200 --json`,
`traces --route <rpc> --slow --since 6h`, `trace <id> prod [--full --json]`,
`logs prod --trace <id>`, `metrics list prod --contains <x>`, `metrics query`.
`--explain` prints the compiled SQL; `obs query --sql` runs raw SQL.
In `trace --full --json`, span SQL is in `.key_attributes.db` (truncated).
Prod DB reads: `reliant forge db query prod "<sql>" --confirm-env prod --reason "<why>"`
(needs `db:query` grant; read-only only — never mutate prod).

## Verified facts (do not re-derive)

1. **Opening a chat's snapshot is fast now.** StreamChatUpdates
   `chat_stream.initial_sync` / `chat_snapshot.build` = 36–102ms in traces
   ee728e77…, fa7cf9a6…, aa5e114f… (20:22–20:29Z). The rest of those stream
   spans is the open stream itself.
2. **ListMessages' prior slow tail** is the O(chat) `count(*)` (PR #2910, merged
   2026-11-18 16:39Z, removed the client hedge; root cause still open).
3. **Sidebar/list RPCs are slow and very high-volume**, and latency tracks volume
   (3h windows, prod, reliant-api-server):

   | window (Z) | ListInbox n / p50 / p95 | ListChats n / p50 / p95 | ListArchivedChats n / p50 |
   |---|---|---|---|
   | 11-17 02:00–17:00 (quiet) | 13–45 / 204–262ms / 283–543ms | 60–121 / 192–293ms | 4–41 / 105–280ms |
   | 11-17 17:00 | 1398 / 1540 / 3182 | 539 / 492 / 1592 | 406 / 458 |
   | 11-17 20:00 | 4502 / 1628 / 3627 | 1783 / 501 / 1101 | 1590 / 555 |
   | 11-18 02:00 | 3146 / 1587 / 3344 | 776 / 424 / 1005 | 997 / 516 |
   | 11-18 11:00 | 3472 / 762 / 1361 | 1129 / 514 / 1148 | 862 / 561 |

   24h totals: ListInbox 14,079 (p50 1.16s, p95 2.87s), ListChats 5,940
   (p50 448ms), ListArchivedChats 5,277 (p50 483ms). 4,502 ListInbox / 3h ≈ one
   every 2.4s — about what ONE client produces if it refetches back-to-back.
4. **Web coalescing**: `frontends/web/src/store/globalUpdatesStore.ts`
   `STREAM_REFETCH_INTERVAL_MS = 2_000` via `lib/coalescedInvalidation.ts`
   (introduced after 2026-10-23: one client sent 363 ListInbox/min). With
   ListInbox itself taking 1.5–3s, a client whose user-update stream is busy
   refetches it essentially continuously.
5. **ListInbox trace ed2301a0cdc373ac191544b9ccf2d25b (20:20Z, 3.08s server, 65 spans)**:
   - approvals UNION `SELECT * FROM ( SELECT ?::integer AS kind, a.id::text AS item_key, c.id::text AS chat_id, COALESCE(c.workflow_id, c.id)…` = **2.57s**
   - trigger events `SELECT e."id", e."trigger_id", e."user_id", e."kind", e."dedupe_key", e."occurred_at", NULL::jsonb AS "payload", e."out…` = **1.06s and 1.03s, run sequentially** (offsets +520ms and +1718ms)
   - `inbox.declarationItems` 2.24s: `SELECT project_machine_catalogs` ×2 = 602ms, 718ms; `SELECT workflow_drafts` ×32 (N+1, ~12ms each)
   - worktree_recoveries 213ms, worktrees 198ms, triggers select 190ms
6. **ListChats trace 0c768027fe97ec8721d215100311c27f (20:19Z, 2.25s server)**:
   `orm.ListChatsWithActivity` → `SELECT chats_with_activity` **1.40s**; `orm.GetProject` ×19 (N+1).
7. Browser-only ListChats/ListInbox traces of 20.8s at 17:30:08Z
   (8ac826e9…, e3b9a3ea…, 97ec0492…) have ONE span — the server never recorded
   them. 10.00s / 6.00s browser-only ListInbox at 17:07Z (e46c4748…, d6310031…,
   187bbdb4…) look like client deadline/hedge timers.
8. No deploys in the window; alerts: `IdentityLatency` open 18:44–18:59Z.

## Open questions (assigned)

- A (backend SQL): why are the approvals UNION, trigger-events, project_machine_catalogs
  and chats_with_activity queries slow; what plans/indexes; N+1s; fixes.
- B (frontend): what triggers ListInbox / ListChats / ListArchivedChats refetches,
  why ListArchivedChats refetches as often as ListChats, does the chat view wait on
  them, and what the 20.8s server-less browser spans are.
- C (attribution/DB health): how many distinct users/clients produce the volume;
  is Postgres / the api-server DB pool saturated (DB span p50 of trivial queries
  over time, pool metrics); what else loads the DB at those times.

## Findings

(append below)

### B (frontend)

Paths under `frontends/web/src`. Query defaults (`lib/query-client.ts:8,12`): staleTime 30s, refetchOnWindowFocus true.

**1. What refetches each list**
- **ListInbox** (key `inboxServiceKeys.all`). Observers: `InboxNavItem` (`components/inbox/InboxNavItem.tsx:51`, always mounted) and `InboxPage`. Both use `refetchOnWindowFocus: "always"` (`hooks/inbox-queries.ts:116,130`), so every window focus refetches whether or not the data is stale. That path skips the coalescer.
  Stream: every `CHAT_ACTIVITY_CHANGED` and `CHAT_STATE_CHANGE` for ANY chat, plus REFETCH type "inbox" (`inbox-queries.ts:84-90`), goes through the coalescer (`store/globalUpdatesStore.ts:611-612`). An agent run emits activity changes continuously.
  Others: the reconnect sweep (`globalUpdatesStore.ts:173-178,456`, coalesced), and every generated InboxService mutation (`inboxServiceKeys.all`, not coalesced).
- **ListChats** (hand key `chatKeys.list(scope)`). Coalesced: `CHAT_CREATED` (`globalUpdatesStore.ts:1047`). Not coalesced:
  - reconnect `loadChats()` (`:453`), which runs beside the sweep that also covers `chatKeys.all`
  - hand mutations that invalidate `chatKeys.lists()` (`hooks/chat-queries.ts:411,426,439,450,463`)
  - `chatStore.ts:1214-1215`
  - focus refetch once 30s old

  Scopes are separate keys: the sidebar (`useChatListWithWorkspaces(scope)`, `ModernApp.tsx:100`) and the Worktrees panels (`useChatList(projectId)`). With a null filter that is two ListChats per invalidation.
- **ListArchivedChats** (`chatServiceKeys.archivedChat`, generated, a child of `chatServiceKeys.all`). Coalesced: archive, restore and delete updates (`globalUpdatesStore.ts:785,816,1084`). Not coalesced:
  - EVERY generated ChatService mutation (`useStartChat`, `useRunNow`, `useAdoptChat`, … invalidate `chatServiceKeys.all`, `hooks/chat-service-hooks_gen.ts:220-237`)
  - hand archive/restore/delete (`chat-queries.ts:428,451,464`)
  - the reconnect sweep (`STREAM_INVALIDATED_SCOPES.chatService`)
  - focus once 30s old
- **Background tabs keep refetching.** The coalescer calls `invalidateQueries` (`lib/coalescedInvalidation.ts:92`), which refetches every *active* observer. In React Query "active" means mounted, not visible, so hidden tabs and windows refetch at the full rate.
- **Hedging doubles slow reads.** ListInbox, ListChats and ListArchivedChats are all in `FAST_READS` (`api/rpcDeadline.ts:66-82`), so a read with no answer after 4s gets a second request, and the first is NOT withdrawn (`rpcDeadline.ts:~109`). The list assumes sub-1s reads. ListInbox p95 is 2.9s, and higher under load, so in busy windows a share of ListInbox becomes 2 server calls. That adds load exactly when the server is slow.
- **Estimate.** Per tab while any run streams: ListInbox ≈ one per max(2s, RTT) ≈ 20–30/min at a 1.5–3s RTT, plus hedges, plus one per focus. ListChats and ListArchivedChats are event-driven, a few per minute. N mounted tabs or windows ≈ N× (no cross-tab dedup), all while hidden. One busy tab at ~25/min ≈ 4,500/3h, which matches the 20:00 window.

**2. Why ListArchivedChats ≈ ListChats.** It is fetched unconditionally: `Sidebar.tsx:1038` calls `useArchivedChats()` at the Sidebar top level, whether or not the archived section is open, so it is always observed. Each automation run produces CHAT_CREATED, which refetches ListChats, and on success it archives itself (`AUTOMATION_SUCCEEDED_REASON`, `:785`), which refetches ListArchivedChats. That is one of each per run, hence ~1:1. Generated ChatService mutations and reconnect sweeps add more archived refetches. Fix candidates: mount it only when the section is expanded (`enabled`), and stop the generated mutations' `chatServiceKeys.all` blanket from reaching `archivedChat`.

**3. Chat-open critical path.** It does not wait on any list. `openChatInWorkspace` calls `selectChat` synchronously before its first await (`lib/openChatInWorkspace.ts:~84`). The awaits that follow are `findProject` (cache first, then ListProjects) and `resolveWorktree`/`switchWorktreeContext`, and they affect only the workspace context. The transcript comes from the chat-stream snapshot (fast, fact 1) and ListMessages. GetChat (`resolveChat`) runs only when the chat is missing from the list cache, i.e. notification or automation chats. The indirect cost is real, though: the list flood competes for the api-server DB pool. The 10-29 note at `globalUpdatesStore.ts:1043-1046` records GetChat p50 rising from 67ms to 2.3s during a ListChats flood.

**4. The 20.8s browser-only spans.** Prod is HTTP/2 (`curl -w %{http_version}` gives `2` for api.reliantapi.com and app.reliantlabs.io), so the HTTP/1.1 6-connection limit is ruled out. The client budget is 10s of ACTIVE page time (`DEFAULT_GRPC_TIMEOUT_MS=10000`, `lib/constants.ts:224`; `rpcDeadline.ts` points 1–2). The browser span measures wall time, so a 20.8s span means the page was hidden or frozen for at least 10.8s mid-call. Three calls of identical duration starting at the same instant fit requests stranded on one dead h2 connection: the server never received them, and they ended when the page resumed and replaced them or the active budget ran out. `rpcDeadline.ts:20-23` describes exactly this failure mode.
  The 10.00s spans are the budget abort. The 6.00s spans are very likely the hedged second attempt: it starts at 4s and is aborted by the shared 10s budget at 10s, so 10 − 4 = 6. Those are client timers, not server latency.
  I could not fetch the short ids (`obs trace 8ac826e9` returns 404, which needs the full id); they have also aged out of the 6h slow list.

**Highest-leverage client fixes (not implemented):**
- (a) Pause stream-driven refetches in hidden tabs: mark stale, refetch on focus.
- (b) Raise the inbox interval, or make it adaptive to RTT (≥ 2×RTT), and drop `"always"` focus refetch.
- (c) Remove ListInbox, and maybe ListChats, from `FAST_READS` until they are fast.
- (d) Lazy-mount the archived list and narrow the generated mutations' invalidation.

### C (attribution/DB)

Measured 2026-11-18 ~20:45Z with `obs query --sql` on `otel_traces` / `otel_metrics_{sum,gauge}`.

**1. Attribution: one user, 3–4 open tabs.** Server-side `user.id` on reliant-api-server,
11-17 19:00–23:00Z: user `30542add-1c3e-4c1f-b0cd-954bd7d782f4` = ListInbox 5,219 / ListChats 690 /
ListArchivedChats 609. Every other user together = 16 / 11 / 9 (`6d093c9d…` Electron, `d6397e04…`).
Browser side (`reliant-web`, `rum.scriptInstance` = one tab; `rum.sessionId` rotates every ~15 min, so
ignore session counts): the same user had **3–4 concurrent Chrome tabs on `https://app.reliantlabs.io/`**,
each producing ~7–8 ListInbox/min (one every ~8s). Busy-window tabs: `caabe278…` 1,796, `a904c54e…` 1,511,
`2fe6ca47…` 804, `3fdbbe66…` 791. Several run **stale builds** (`364a94f24aa7`, `f20c16c2039a`,
`6f82baaa2935` side by side), plus a Mobile Safari tab. 11-18 12–15Z: four tabs, 1,071 / 1,068 / 1,065 / 919.
Last 30 min: two tabs (283, 265), same user: 240 server ListInbox at **p50 1,025ms**. Quiet windows:
that user sends ~0, and the residue is other users' 20–45/h. So volume = this user's tabs being
active (user-update stream busy: agents running), × number of open tabs. No hammering client
from anyone else.

**Same moment, other user:** last 30 min ListInbox p50 is 75ms for `6d093c9d…` and 203ms for `247c983a…`,
vs 1,025ms for `30542add…`. The cost is that user's data size, not DB contention.

**2. DB health: Postgres is not saturated; trivial queries stay flat.** Hourly over 36h:
- worker `SELECT threads` p50 2.1–2.6ms and p95 4–9.5ms across every hour, including the busiest
  (91k/h at 11-18 14:00). Worker `BEGIN` p50 1.5–2.1ms. `db.connect` p50 ~50ms, flat.
- api-server `SELECT chats` p50 2.5–4.0ms (p95 5ms quiet → 27–34ms busy). `SELECT daemons` p50
  2.6 → 5.2ms (p95 5 → 35ms). That is a mild api-pod-local queueing tail, not Postgres.
- The expensive api queries are expensive in themselves: the unnamed `SELECT` (approvals UNION)
  p50 is 4–9ms in quiet hours (other users) and 108–237ms whenever 30542add is active.
  `project_machine_catalogs` p50 drifted from 20–30ms (11-17) to 86–114ms (11-18) at similar
  volume (growing data?).
- **Pool (go.sql, max_open=25 per api pod, 2 pods):** wait_count is 0 every hour until 11-17 18:00,
  then 100–370 waits/h (avg wait 25–100ms, ≤37s total/h) in busy hours. Two real exhaustion
  events: **11-18 08:30–08:35Z (3,196 waits, 1,263s waited, in_use hit 24)** and **11-18
  11:40–11:45Z (1,806 waits, 510s, in_use 25 = max)**. Both coincide with bursts of `orm.GetChat`
  at p50 100ms / max 2.1–2.3s whose roots are `StreamChatUpdates` (19 slow spans across 3 traces) and
  `StreamUserUpdates`: a stream fan-out/reconnect burst, not the sidebar. Average in_use is
  0.1–1.2, so the pool is idle most of the time.
- No Cloud SQL CPU or pgxpool metrics in telemetry: only `go.sql.*` / `db.sql.connection.*`.

**3. What else loads the DB.** reliant-temporal-worker went from ~55 workflows / ~400 activities /
~85s DB-time per hour (11-17 ≤17:00) to 700–1,060 workflows / 8k–20k activities / 2k–7k s of DB
time per hour from 11-17 19:00 onward. It **stays high continuously**, including 11-18 15–19Z when
ListInbox was quiet. TriggerEventFireWorkflow: 0/h before 19:00, then 233–640/h. Top DB time,
busy hour 11-17 20Z: api `SELECT` (UNION) 6,338× p50 148ms = **2,649s**; worker `UPDATE
workflow_catalog_snapshots` 8,035× p50 53ms = **878s**; worker `SELECT budgeted` 11× = 311s; api
`project_machine_catalogs` 3,205× p50 54ms = 194s; worker `SELECT trigger_events` 3,283× = 192s; api
`SELECT workflow_drafts` 27,794× (N+1) = 173s. The rest are high-count 2–3ms point queries
(threads, chat_updates, messages, daemons). Last 3h: control-plane-workers is also heavy on the same
Cloud SQL instance (`SELECT` 64k× = 2,248s, `SELECT moves` p50 342ms = 488s, `SELECT counted` 444s).
Its DB is control_plane_cp, but it shares reliant-pg's CPU.

**4. Now (last 30 min, ~20:15–20:45Z):** the same pattern is back at a lower rate. Two owner tabs,
ListInbox 240 (p50 1.0s), ListChats 82 (585ms), ListArchivedChats 62 (518ms). The api UNION
`SELECT` 1,546× p50 97ms = 441s. Worker `workflow_catalog_snapshots` 5,382× = 425s. Trivial
queries are still 2.5–3ms. Pool: 126 waits in the 20:00 hour.

**Conclusion:** not a saturated database. Per-request cost (approvals UNION, trigger_events,
project_machine_catalogs, workflow_drafts N+1) on one heavy account is multiplied by N open tabs
× a ~2s refetch cadence. Fix the query cost (A) and the per-tab refetch (B: cross-tab dedupe, or
skip the refetch when the tab is hidden). Separately: `UPDATE workflow_catalog_snapshots`
(~30k/3h, p50 40–53ms) is the biggest worker DB consumer, and the stream fan-out bursts are what
actually exhausted the api pool, twice.

### A (backend SQL)

Evidence: prod `reliant` DB via `forge db query --database reliant` (role forge_ro;
**it cannot read the view `chats_with_activity`**, so EXPLAIN of the real
statements was refused — base-table equivalents were explained instead), and
`obs query` over otel_traces (36h, 3h buckets, reliant-api-server spans).

**The data shape is the root of it: one user (30542add…) owns 46,523 of 46,624
chats (44,283 archived automation runs) and 201,387 of 201,565 trigger_events**
(965 MB table; 43 triggers total in prod). step_executions 3.7M/12 GB,
messages 1.78M. project_machine_catalogs: 84 rows / **107 MB (~1.3 MB per row)**.

**Plan cost vs contention** (span p50 / p95 by 3h bucket):

| statement | quiet (11-17 09–15Z, 11-18 15Z) | busy (11-17 18Z – 11-18 03Z) |
|---|---|---|
| ListInboxPending (approvals UNION) | 104–166 / 173–477 ms | 594–717 / 1191–1469 ms |
| trigger firings (×2) | 66 / 179 ms | 187–235 / 534–830 ms |
| project_machine_catalogs | 22–26 / 29–37 ms | 55–105 / 154–382 ms |
| workflow_drafts | 2–3 / 12–15 ms (~80/3h) | 3–4 / 11–21 ms (**~45,000/3h**) |
| chats_with_activity (all reads) | 5 ms p50 | 9–14 / 545–952 ms |

So every statement has a real base cost on this user's data, and it is
multiplied 4–5× under the busy-hour RPC volume (a contention effect, not a
plan change). Count×cost: the busy window is ~1,500–1,850 inbox pending
statements / 3h at ~0.65s = the inbox alone keeps ≈0.1 DB core busy on that
statement, plus ~15k catalog reads moving ~1.3 MB each (≈ 6–7 GB / 3h of TOAST
detoast+transfer — likely the biggest single DB/network load of the inbox).

Per query:

1. **ListInboxPending** (`internal/store/inbox_dismissal_repo_ext.go:111–302`).
   Approvals/questions branches are already fenced and cheap (base-table probe:
   1.1 ms). The base cost is the branches that walk *all* of the user's chats
   through the view: kind 3 (`display_state = 8`) and especially kind 10
   (`launch_kind = ANY($2) AND display_state = 4`, :284–288) — `launch_kind`
   is a correlated `trigger_events` subquery per chat
   (view `db/migrations/20261102040920_chat_origin_fails_closed.up.sql:175–186`)
   and kind 10 also LATERALs workflows×step_executions (12 GB) per match;
   kind 5 aggregates every failed event per trigger. Base-table equivalent of
   kind 10's filter: Seq Scan of chats (46,618 rows), 64 ms. No `chats` index
   leads with `user_id` + `state` for non-archived rows (only
   `idx_chats_user_created_id`, `idx_chats_user_project`).
   Fix: filter kind 3/10 on base columns before the view (`chats.state <> 3`
   + join `workflows rw` on `rw.state=3 AND rw.stop_reason=3` first, behind an
   OFFSET 0 fence like the approvals branch), and denormalize `launch_kind`/
   `trigger_id` onto `chats` (written at launch, as `origin` already is) so the
   view stops probing trigger_events per chat. Partial index
   `chats(user_id, last_active DESC) WHERE state IS DISTINCT FROM 3`.
2. **Trigger firings** (`internal/store/trigger_event_repo_ext.go:451–555`,
   called from `internal/handlers/inbox/inbox.go:404–425`).
   `firingsSinceLastSuccessSQL` (:522) walks launched events newest-first and,
   for each with `run_status IS NULL`, joins `chats_with_activity` to compute
   `display_state` until it finds a success — on a 200k-event trigger that is
   many view evaluations. Both statements use `db.Bun().NewRaw` with
   `pgdialect.Array(ids)` (:563), which formats values into the text: a fresh
   statement per user/trigger set (the SCHEMA.md anti-pattern the pending
   query avoids). They are not sequential by design: `inboxReadConcurrency = 3`
   (`inbox.go:162`) and the pending read, storage, recoveries and catalog loop
   hold the tokens, so the two firing reads queue for a token behind the 2.5s
   pending read. Fix: backfill/always write `run_status` so the view join is
   never needed (or drop the `sc.display_state` fallback), use bound `$N`
   params via `db.Query`, and raise the limiter to ≥5 or give the advisory
   reads their own budget.
3. **project_machine_catalogs ×2** (`internal/store/project_machine_catalog.go:52–75`,
   via `triggers.LaunchWorkflows` in `inbox.declarationItems`, `inbox.go:525`).
   Reads *every* column (skills_json, workflows_json, repo_memories_json,
   user_config_yaml, scenarios_json, … ~1.3 MB/row) of every machine to resolve
   one workflow; `CachedWorkflows` (`internal/triggers/declaration.go:551–576`)
   caches per (user, workflow, project, daemon), so distinct workflows/daemons
   re-read the same rows. Quiet cost 22–26 ms is pure detoast+transfer. Fix:
   select only `workflows_json` (or a per-workflow lookup), cache the catalog
   per (project, daemon) for the request, and ideally per process keyed by
   `updated_at`.
4. **workflow_drafts N+1** (32 per inbox read; ~45k statements / 3h busy):
   per-trigger draft lookup from the same `LaunchWorkflows` resolution. Each is
   cheap (3–4 ms) but serial inside one limiter token → ~130 ms + pool churn.
   Fix: one `WHERE user_id=? AND slug = ANY(?)` read up front into the cache.
5. **ListChats `SELECT chats_with_activity` 1.40s + 19× GetProject**
   (`internal/store/chats_with_activity_repo_ext.go:92–118`). Its filter is
   cheap: the base-table equivalent (user, non-archived, list_in_sidebar,
   ORDER BY last_active LIMIT 100) is a 28 ms Seq Scan returning 26 rows, and the
   statement's quiet p50 is 5 ms, so the 1.4 s in trace 0c768027… is
   contention (busy p95 545–952 ms), not plan. Still: it Seq Scans all 46k
   chats (no `(user_id, last_active)` index) and computes `last_message_at`
   (MAX over messages per row; over all of this user's chats that subquery alone
   is 1.73 s). Fix: the partial index above; batch project names (one
   `WHERE id = ANY(?)` or join `projects` in the list query) instead of
   `orm.GetProject` per chat. ListArchivedChats (`:183–186`) is the same
   shape over the 44k archived rows; it needs a LIMIT/pagination.

Not verifiable with forge_ro: real EXPLAIN of anything that reads
`chats_with_activity` (permission denied). Granting forge_ro SELECT on the view
would let the next investigation EXPLAIN the exact statements.
