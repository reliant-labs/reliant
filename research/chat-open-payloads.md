# Chat-open payloads: bounding what a chat open reads

Working briefing for the `paginate` branch. Settled facts are marked SETTLED —
do not re-derive them.

## The problem (SETTLED, measured)

Opening chat `8bb0a875-ba30-4867-b31f-7d5043e5da2f` took >10s. Messages were
already paginated (snapshot = newest 200; scroll-back via ListMessages
`before_seq`). Two SIDE payloads scaled with the chat's entire history.
Measured with a read-only harness (`internal/grpc/services/zz_profile_chat_load_test.go`,
build tag `profilechatload`) against the dev DB:

| read | rows | payload | server |
|---|---|---|---|
| `GetStepExecutionsForChat` (inside GetWorkflowExecutions) | 93,568 steps | 56.8 MB JSON / 32 MB proto | 0.9–1.3s SQL, 1.2–4.6s RPC |
| `GetLatestNonMessageUpdatesPerEntity` (inside buildChatSnapshot) | 27,279 (25,660 tool_call) | 10.4 MB snapshot | 3.1–4.4s SQL |
| `getNonMessageUpdates` incl. `snapshotToolCalls` reconciliation | | | 4.5–6.2s |
| ListRecentMessages / CountMessagesInChat | 200 | | ~15ms |

The timeline uses **6** of the 93,568 steps. Every in-window tool-call block
already carries durable `tool_call_status`/`child_workflow_id`
(`assembleMessagesForDisplay`, proto_converters.go), so historical tool-call
statuses in the snapshot are pure weight.

## Test / measurement databases (SETTLED)

- Scratch Postgres 17 container `paginate-pg` on **localhost:56433**
  (user/pass postgres/postgres). Created by this branch; safe to use.
  - DB `reliant` — empty; use `DATABASE_URL=postgres://postgres:postgres@localhost:56433/reliant?sslmode=disable`
    for `go test` (testutil migrates a template DB per process).
  - DB `reliant_copy` — full copy of the dev DB (544k step rows, 2.48M
    chat_updates). The four indexes from the new migration were hand-created
    on it for prototyping and it has been VACUUM ANALYZEd. Use it to measure.
    Harness: `PROFILE_DB_URL=postgres://postgres:postgres@localhost:56433/reliant_copy?sslmode=disable PROFILE_CHAT_ID=8bb0a875-ba30-4867-b31f-7d5043e5da2f go test -tags profilechatload -run TestProfileChatLoad -count=1 -v ./internal/grpc/services/`
- **NEVER** write to localhost:5434 (control-plane postgres, real data) or
  5433. Never run migrations against them.

## Already done (SETTLED — do not redo)

- Migration `internal/db/migrations/postgres/20261004010819_bound_chat_open_reads.sql`
  (goose NO TRANSACTION, CONCURRENTLY, no Down section — this project never
  writes down migrations). Creates:
  - `idx_step_executions_user_facing` ON step_executions (workflow_id, created_at)
    INCLUDE (id, step_id, activity_name, exit_code, success, duration_ms,
    loop_node_id, loop_iteration) WHERE activity_name NOT IN (<9 internal names>)
  - `idx_step_executions_saves` ON step_executions (workflow_id, step_id)
    INCLUDE (loop_node_id, loop_iteration, saved_message_id, id, created_at)
    WHERE saved_message_id IS NOT NULL
  - `idx_chat_updates_snapshot_heads` ON chat_updates (chat_id, entity_id,
    sequence_number DESC) WHERE update_type NOT IN (1, 4, 19)
  - drops `idx_chat_updates_chat_entity_seq` (438 MB, 0 scans in dev)
  - `idx_tool_calls_chat_live` ON tool_calls (chat_id) WHERE status IN (1, 2, 6)
- Proto: `WorkflowExecutionView` enum (BASIC=0 default, FULL=1) and
  `GetWorkflowExecutionsRequest.view = 2`. `make proto-generate` already run;
  Go type is `reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC/FULL`.
- `web/node_modules` installed.

## Measured query shapes on reliant_copy (SETTLED)

- BASIC steps: `workflows w JOIN step_executions se ON se.workflow_id = w.id
  WHERE w.chat_id = $1 AND se.activity_name NOT IN ('WorkflowStatus','WorkflowError','Cleanup','FetchThreadResult','FailStep','SaveMessage','CallLLM','Approval','ExecuteTools')`
  → ~6ms using idx_step_executions_user_facing. The NOT IN list MUST be a
  literal in the SQL (a bound array param cannot prove the partial index
  predicate at plan time). Pin it to `workflowmodel.InternalActivities`
  (internal/workflow/model/internal_activities.go) with a test.
- -save siblings of those visible steps, joined on (workflow_id,
  step_id = visible.step_id || '-save', loop_node_id IS NOT DISTINCT FROM,
  loop_iteration IS NOT DISTINCT FROM, saved_message_id IS NOT NULL) → <1ms
  via idx_step_executions_saves.
- Snapshot heads: recursive skip-scan over idx_chat_updates_snapshot_heads
  (one probe per distinct entity_id) ~100ms vs DISTINCT ON ~326ms+ (3–4s on
  the live, less-cached DB). **Trap (verified)**: a pure per-entity_id skip
  scan LOSES 198 workflow_status (type 5) rows, because THREAD (3) updates
  share entity_id (= workflow id) with workflow_status rows and are re-keyed
  by `data->>'thread'`, and QUESTION (18) updates are re-keyed by stripping a
  timestamp suffix. So: types 3 and 18 must be read separately (they are few:
  656 + 0 rows for this chat) and deduped with the existing CASE keys; the
  skip scan must exclude 3 and 18 too. Then union. Parity must be checked
  against the existing DISTINCT ON (minus tool calls) — the prototype is in
  /tmp/paginate-heads.sql.

## Consumers (SETTLED)

- `steps` readers that need FULL: WorkflowViewer / ActivityLog /
  NodeDetailsPanel / useExecutionStatus (desktop viewer + MobileWorkflowScreen),
  and `reliant workflow status` (cmd/reliant/commands/workflow_supervise.go
  summarizeSteps). `workflow follow` (execfollow) reads only root status —
  BASIC is fine.
- `steps` readers fine with BASIC: activityIndicators.ts getActivitySteps
  (InterleavedTimeline), everything else (ChatHeader/useThreads,
  useBackgroundWork, ToolExecution*, SpawnToolRenderer) reads only the tree.
- Tool-call status on the client: `chatStore.toolCallStates` is only cleared at
  chat init; it is fed by tool_call updates (snapshot and live). Cards resolve
  status as live state → block.toolCallStatus (durable) → inferred. Snapshot
  never clears it. A reconnect with a cursor (chatSinceSeq>0) replays the
  missed range from chat_updates instead of re-snapshotting.
