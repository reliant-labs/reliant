# C2 — StartChat migration: settled design

Companion to `TRIGGERS.md`. This is the spec for agent C2; the decisions below
are made, so do not re-litigate them.

## Wire

- **Rename** `ChatService.CreateChat` → `StartChat`, and
  `CreateChatRequest`/`CreateChatResponse` → `StartChatRequest`/`StartChatResponse`.
  We never keep backwards compatibility pre-launch, so delete the old
  RPC. Keep the existing field numbers.
- **`StartChatRequest`** gains `optional string chat_id = 17`.
  - Set: start the existing PENDING chat (a branch's first send). `project_id`
    and `worktree_id` must then be empty or equal to the chat's, and `workflow`
    may change only because the chat is still pending (this absorbs today's
    "workflow switching while pending" branch in SendMessage).
  - Unset: create a new chat (today's CreateChat).
- **Delete the dead request fields** `temperature` (8) and `max_tokens` (9):
  nothing on the server reads them. Mark them `reserved` with a comment, the
  same as their neighbours.
  - `mode` (14) is also unread by CreateChat today. Wire it the way
    `RunService.StartRun` intends: when set, it becomes `workflow_params.mode`.
    That makes the field real instead of decorative.
- **`StartChatResponse`**: chat, workflow_id, run_id. Delete `draft_id`: no
  server code sets it on this response.
- **Not in this change:** don't apply the same `temperature`/`max_tokens`
  cleanup to `SendMessageRequest`. Leave it alone.

## Server semantics

1. **Launcher records the event.**
   - In the same transaction as the chat rows, insert a `trigger_events` row
     via `CreateTriggerEvent`: kind `ev.Kind`, dedupe `ev.DedupeKey`, outcome
     `launched`, `chat_id` set, `trigger_id` from `ev.TriggerID` when non-empty.
   - `created=false` means this (kind, dedupe) already launched. Then:
     - Load it with `GetTriggerEventByDedupe` and read its chat id.
     - If that chat's root workflow is still `pending`, a previous attempt
       committed but never started Temporal. Finish that attempt: start
       Temporal and flip to active (below), then return success with the
       existing ids.
     - Otherwise return `*AlreadyLaunchedError{ChatID, EventID}`.
2. **Chat-start events.**
   - kind `chat.start`, `DedupeKey = chat id`, `OccurredAt = now`.
   - Payload: `{workflow, worktree_id, message_count, attachment_count}`. Do NOT
     copy message text into the payload; the messages table already holds it.
   - For a new chat, generate the chat id in the handler and put it in both
     `Spec.NewChatID` and the dedupe key, so the event row and chat row agree
     by construction.
3. **Temporal start.**
   - Uses `WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING`. A retry attaches to the
     execution it already started instead of killing it.
   - After `ExecuteWorkflow` succeeds, CAS the root workflow `pending → active`
     (`CompareAndSwapWorkflowStatus`).
   - Today a root workflow stays `pending` until the run's own WorkflowStatus
     activity flips it, which leaves a window. Closing it at launch makes
     "pending" mean exactly "this chat has never started", and SendMessage
     relies on that.
   - The workflow's own later write to active is idempotent. Confirm in
     `internal/workflow/runtime/activities/handlers/workflow_status.go`
     (~:255, ~:336) that an already-active row is fine.
4. **Starting a pending chat** (`Spec.ChatID` set, i.e. a branch):
   - Load it and check the owner.
   - Require the root workflow to be `pending`; otherwise return
     `ErrNotPending`, which maps to FailedPrecondition "chat has already
     started; use SendMessage".
   - Apply a workflow switch if requested: update the chat and root workflow
     names, as SendMessage's pending branch does today.
   - Save the seed messages to the existing root thread (it was forked by
     BranchChat). Record the event and start Temporal.
   - Reuse the existing chat's worktree, presets and active daemon.
   - **Check whether forked threads need any execution-context difference**
     from a fresh chat. SendMessage's fresh-start path used `ThreadModeNew`
     plus `targetThread = workflowID` even for branches, and that is the
     behavior being replaced. Keep it unless you find a reason not to, and
     document what you find.
   - No greenfield probe for a branch (it's not a first turn of new work).
   - Title generation only if the chat has no title.
5. **SendMessage.**
   - Remove the pending fresh-start path and the "chat has no workflow id"
     legacy branch.
   - A chat whose root workflow is `pending` → `FailedPrecondition`:
     "chat has not started; call StartChat".
   - Remove the workflow-switch branch (StartChat owns it). If
     `req.Msg.Workflow` differs from the chat's workflow on a started chat,
     return FailedPrecondition with the existing message ("cannot change
     workflow after chat has started - use Branch…").
   - Everything else (active / paused / failed / completed / cancelled /
     ghost) is unchanged.
6. **`RunService.StartRun`.**
   - Without a session: `StartChat` semantics via the launcher.
   - With a session: if that session is pending, start it via the launcher;
     otherwise `SendMessage` (a continuation), as today.
7. **ExecContext.UserJWT.**
   - Today CreateChat never sets it, while SendMessage does. Make the launcher
     accept an optional `UserJWT` in the Spec.
   - Interactive callers pass `auth.GetUserJWT(userID)` (via an exported Spec
     field the handler fills); scheduled fires pass none.
   - This lets a cloud-daemon chat's first run resolve the control-plane
     daemon the same way its later runs do.

## Chat proto bug: populate `workflow_state` / `workflow_stop_reason`

`Chat.workflow_state` and `workflow_stop_reason` are never set, so the web's
`isWorkflowPaused` is always false.

- Source them from the chat's ROOT workflow row (`workflows.id = chats.workflow_id`).
- Prefer adding `root_workflow_state` and `root_workflow_stop_reason` columns
  to the `chats_with_activity` view in a new migration. That way every chat
  read path gets them in one query instead of N+1 lookups.
- Watch the frozen `c.*` expansion trap documented in
  `20260928193449_restore_chats_active_daemon_id.sql`: DROP and CREATE the
  view, keeping its existing definition. Then:
  - `scripts/generate-schema-sql.sh` (the repo convention for regenerating schema.sql)
  - `make sqlc`
  - map in the chat store and `core.Chat`, and set both fields in `chatToProto`
- Ensure `BranchChat`'s response carries PENDING. The web needs it to choose
  StartChat over SendMessage.

## Web

- **chat-grpc / client.**
  - `chatGrpc.create` → `chatGrpc.start`, with an optional `chat_id`.
  - Remove `temperature` and `max_tokens` from the create options.
- **chatStore.**
  - `createChat` → `startChat`.
  - Add the branched-chat first-send path: `ChatContainer.handleSendMessage`
    currently calls `sendMessage` whenever `currentChat` exists. Make it call
    `startChat({chatId})` when the chat's `workflowState === PENDING`, and
    `sendMessage` otherwise.
  - Keep the optimistic user message and the activity flip for both paths.
- **Other callers:** `NewChatView`, `MobileNewChat`, `WorkflowBuilderChat` (first
  message), and any other `create(` caller.
- **Tests:** update the vitest tests that mock createChat (6 files: see
  `rg -l createChat web/src --glob '*.test.*'`). Add a test that a PENDING
  chat's first send calls start and an ACTIVE chat's send calls sendMessage.
- **Regenerate:** `make proto-generate` writes `web/src/gen`. There are no
  `node_modules` in this worktree. If you need to type-check or run vitest,
  `npm ci` in `web/` is allowed. Report if it is too slow.

## Other callers

- **CLI** `reliant workflow run` (`cmd/reliant/commands/workflow.go:~340`) →
  StartChat.
- **Harnesses:**
  - `e2e/stories/harness_test.go:294-332` (CreateChat/TryCreateChat helpers →
    StartChat; the story files call the helper)
  - `internal/workflow/runtime/replaytest/harness_gen_test.go:~374-395`
    (build tag `replayfixtures`; vet with the tag)
- **Auth timeout table:** `internal/grpc/interceptors/auth.go:356` → the
  StartChat key.
- **Go tests:** the 8 files constructing `CreateChatRequest` (list in
  TRIGGERS.md), plus branch tests that later send.
- **Docs:**
  - `rg -n 'CreateChat' --glob '!gen/**' --glob '!**/node_modules/**'` across
    docs/, research/ and README-ish files; update those that describe the
    current API (leave historical research notes alone).
  - Regenerate any reference docs whose generator covers RPCs.

## Tests that must exist (each fails before, passes after)

- **StartChat, new chat:** a `chat.start` event row with `dedupe_key = chat id`,
  outcome launched; ExecuteWorkflow with `USE_EXISTING`; root workflow active
  after return.
- **StartChat, pending branched chat:** messages land on the forked thread, the
  run starts, one event row.
- **StartChat on an already-started chat** → FailedPrecondition. No second
  event, no second ExecuteWorkflow.
- **Resumability:** simulate "event + chat committed, Temporal start failed"
  (fake starter errors once). Retrying StartChat with the same `chat_id`
  starts it, with exactly one event row.
- **SendMessage on a pending chat** → FailedPrecondition. On active/paused/
  completed it behaves as before (existing tests).
- **GetChat / ListChats** return `workflow_state` / `workflow_stop_reason` for
  pending, active and paused chats.
- **Web:** the pending→start / active→send routing test above.
