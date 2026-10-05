# Review: workflow builder → normal chat

Reviewed against `research/WORKFLOW_BUILDER_NORMAL_CHAT.md`. The decisions marked SETTLED there were
taken as given. I checked the implementation against them and did not reopen them. The review was
read-only: no files edited and no git state changed. The working tree also holds other agents' work,
so only the files named in the brief were reviewed.

## Verdict: REQUEST CHANGES

- **Blocker.** The web app does not typecheck, so `npm run build` / `tsc -b` fails. One of the
  errors is also a runtime `ReferenceError` on a user-facing path: applying YAML in the editor.
- **High.** When the chat panel is open on a chat, it takes the single chat-details stream slot back
  from a builder test run. The canvas then stops showing test-run node statuses.
- Backend publish design is correct: exactly one announcement per write, inside the write's
  transaction, with nothing announced on rollback. Moving the writes into `RunTx` has two side
  effects worth fixing (retry ladder on unique violations, and the isolation level).
- `load_tool` tag loading is enforced per tool exactly as single-name loads are. Search-by-tag
  does not expose anything that name search did not already show.

### What I ran

| Check | Result |
|---|---|
| `go build ./...` | ok |
| `go test ./internal/db -run TestWorkflowDraft` | ok (2 tests). The duplicate-slug test takes **2.68s** — see I-3 |
| `go test ./internal/llm/tools -run 'TestLoadTool\|TestResolveWorkflowDraft\|TestWorkflowTools_RequireID\|TestCreateWorkflow_ReturnedID\|TestWriteWorkflow'` | ok |
| `go test ./internal/grpc/services -run TestUserUpdateMatchesProject` | ok |
| `go test ./internal/workflow/scenario/runner` | ok |
| `npx vitest run` (WorkflowEditorChatPanel, useWorkflowDraftSync, globalUpdatesStore.workflowDraft, useBuilderTestRun) | 4 files, 13 tests pass |
| `npx tsc --noEmit -p .` (the briefing's gate) | "passes", but **checks nothing**: the root `web/tsconfig.json` is `files: []` + references |
| `npx tsc --noEmit -p tsconfig.app.json` (what `npm run typecheck` = `tsc -b` checks) | **FAILS, 5 errors, all from this change** |

---

## Critical (must fix)

### C-1. The web app fails typecheck/build, and YAML Apply throws at runtime
`tsc -p tsconfig.app.json`:

```
WorkflowBuilder.tsx(2094): TS2304 Cannot find name 'handleChatWorkflowUpdate'.
WorkflowBuilder.tsx(1149): TS2322 'number | undefined' is not assignable to 'number'.
WorkflowBuilder.tsx(205):  TS6133 'onDraftIdChange' is declared but never read.
store/chatStoreHooks.ts(18): TS6133 'useRef' is declared but never read.
store/chatStoreHooks.ts(20): TS6192 All imports in import declaration are unused.
```

- `web/src/components/workflow/WorkflowBuilder.tsx:2094`: `handleChatWorkflowUpdate` was renamed to
  `applyRemoteWorkflowUpdate` (~L1079), but the `YamlEditorModal.onApply` call site was not updated.
  Vite does not typecheck, so dev serves this file. Clicking **Apply** in the YAML editor runs
  `ImportWorkflow`, which persists the change, then throws inside `YamlEditorModal.handleApply`'s
  `try`. The user sees "Failed to parse YAML: handleChatWorkflowUpdate is not defined" even though
  the import was stored. Fix: call `applyRemoteWorkflowUpdate(w)`.
- `WorkflowBuilder.tsx:1149`: the `version` prop is optional, but `useWorkflowDraftSync` requires a
  `number`. Pass `version ?? 0`, or make the prop required.
- `WorkflowBuilder.tsx:205`: `onDraftIdChange` is no longer used. The deleted panel was its only
  consumer. Remove it from props and from the `WorkflowBuilderPage` call.
- `web/src/store/chatStoreHooks.ts:18,20`: these imports (`useRef`, `useActivityStore`,
  `ChatActivity`) became unused when `useActiveBuilderChats` was deleted.
- **Process:** the gate in the briefing, `npx tsc --noEmit -p .`, cannot fail. Use
  `npm run typecheck` from `web/`.

---

## High

### H-1. The open chat panel takes the stream slot from builder test runs
- `web/src/components/Chat/ChatContainer.tsx:109-114`: ChatContainer re-asserts its own chat
  whenever `chatId` **or `connectionStatus`** changes.
- `web/src/components/workflow/hooks/useBuilderTestRun.ts:27-33`: subscribes the test chat once and
  never re-asserts it.
- `globalUpdatesStore.subscribeToChatDetails` has one slot.

How it plays out: the panel is open on chat A (the default once `?chat=` is set, since
`chatPanelOpen` defaults to `true` at `WorkflowBuilder.tsx:251`). The user clicks Run, and
`useBuilderTestRun` subscribes T. The resubscribe reconnects the stream, so `connectionStatus` goes
`connected → connecting`. ChatContainer(A)'s effect re-runs, `reconcileChatSubscription(A)` sees
`subscribedChatId === T`, and it subscribes A again. T's subscription is now gone and nothing
restores it, so `useNodeExecutionStatus(T)` receives no events and the canvas stops painting
test-run statuses. The old builder chat did not have `connectionStatus` in its subscribe effect, and
`useBuilderTestRun` used to hand the slot back on cleanup. The new panel always wins during the run,
not just after it (the new comment in `useBuilderTestRun` says "afterwards").

Fix options, cheapest first:
1. While a test run is live, don't mount `ExistingChat` (collapse the panel or show a "paused while
   test run streams" stub).
2. Give ChatContainer a passive mode that skips `reconcileChatSubscription`, and use it in the panel.
3. The durable fix is multi-chat subscriptions in the stream.

Add a test that mounts both and asserts the test chat stays subscribed after a status flip. This is
inferred from the code and not yet reproduced in a browser. Confirm with
`grep 'Reconciling chat subscription' frontend_reliant-web.log` during a test run.

---

## Important (should fix)

### I-1. The composer prefill is lost on the default first-open path
- `web/src/components/workflow/WorkflowEditorChatPanel.tsx:151-177`
- `web/src/components/Chat/useChatInputState.ts:29-43, 129-145`

The builder, and therefore the panel (open by default), renders while `WorkflowBuilderPage` is
still loading, so `initialWorkflow` is `undefined` (`WorkflowBuilder.tsx:1955`):

1. On first render, `prefill` is `""`, `ready` becomes `true`, and `NewChatView` → `ChatInput`
   mounts. `useState` reads the project's new-chat draft **once**.
2. When the workflow arrives, `prefill` changes and the effect writes the draft to the store. But
   ChatInput's reload effect is keyed on project/worktree/draftKey and never re-reads it.
3. The composer stays empty, and the agent is never told which workflow is meant. That violates the
   SETTLED "composer is PREFILLED".

Same on `/workflow/new`. Fix: don't render `NewChatWithWorkflowReference` until `workflowSlug` is
known, or `key` `NewChatView` on `prefill`. The existing test (see T-2) cannot catch this.

On the clobber question asked in the brief: **no clobber.** A non-empty draft that isn't an
untouched reference is left alone. Side effect: the panel shares the project-level new-chat draft
with the main app's composer, so text typed in the panel shows up there and the reverse.

### I-2. Draft writes now run SERIALIZABLE, which drops `CreateUserUpdate`'s READ COMMITTED
- `internal/db/repository_impl.go`: 4090-4290, every draft write is wrapped in `r.RunTx(...)`
- `internal/db/repo.go`: `RunTxWithOptions` joins an outer tx and ignores options (~L516); the
  rationale is the comment at ~L33-64

`CreateUserUpdate` deliberately runs at READ COMMITTED: allocating from the single per-user
`update_stream_counters` row under SERIALIZABLE aborts every waiter. Inside the draft write's
default (SERIALIZABLE) tx, that option is silently ignored. So a draft write that overlaps any other
user update for the same user gets 40001 and retries the whole write. That overlap is the normal
case: the agent editing the workflow is in a running chat that emits chat-state and activity
updates. It is correct but slower, and it reintroduces the hazard the repo comment documents.

Fix: run draft-write transactions with `TxOptions{Isolation: IsolationReadCommitted}`. Each is one
row write plus a read-back of its own write, so READ COMMITTED is enough. Also make the deletes
`DELETE … RETURNING *` (see I-6).

### I-3. Unique violations now go through the retry ladder (3s stall, error-level logs)
- `internal/db/repo.go:271`: 23505 is classed as retryable
- `internal/llm/tools/workflow_editing.go:150-161`: no slug pre-check before insert

Before, `CreateWorkflowDraft` ran outside a transaction and a duplicate slug failed at once. Now
the duplicate is retried 7 times with backoff. The duplicate-slug part of
`TestWorkflowDraftWrite_RolledBackEmitsNothing` takes **2.68s** versus 0.76s for its neighbour,
which is that ladder.

Effects:
- An agent calling `create_workflow` with a name that already exists stalls about 3s.
- It then gets `transaction failed after retries: … duplicate key …`, and "Transaction failed after
  all retries" is logged at Error.
- The same applies to a rename onto a taken slug through `UpdateWorkflowDraft`.

Fix: pre-check `WorkflowSlugExists` in `create_workflow` and return a clear message (the better UX
anyway). Optionally map 23505 inside the closure to a non-retryable domain error, using no `%w` so
`errors.As` doesn't find the `PgError`.

### I-4. `workflow_drafts.chat_id` is still read by every query, so the planned DROP is unsafe
- `internal/db/postgres/queries/workflow_drafts.sql`: every `SELECT *` and `RETURNING *`
- e.g. `generated/workflow_drafts.sql.go:150`

sqlc expands `*` when it generates code. Every compiled query in this release therefore names
`chat_id` explicitly (`SELECT id, …, is_hidden, chat_id, created_at …`). The SETTLED plan is to drop
the column in a follow-up "once this has shipped". During that follow-up's rolling deploy, this
release's pods would hit `column "chat_id" does not exist` on every draft read.

For expand-then-contract to work, this release must not mention the column at all. Fix now: replace
`*` with explicit column lists that omit `chat_id`, then `make sqlc`. The domain type and the INSERTs
are already clean.

### I-5. Draft tools resolve a UUID without checking the owner (pre-existing, now the main path)
- `internal/llm/tools/workflow_resolve.go:37-46`

`GetWorkflowDraft(id)` is not scoped to the user, and none of the edit, write or scenario tools
check `draft.UserID`. In this multi-tenant system, any chat that learns another user's draft UUID
can read and edit it. The slug and name lookups are user-scoped.

This existed before. It matters more now because the UUID is the required handle, and
`create_workflow` tells the model to repeat it in every call, so it ends up in transcripts. Fix: in
the UUID branch, return not-found when `draft.UserID != userID`.

### I-6. Status-change echoes can raise a false "updated elsewhere"
- `WorkflowBuilder.tsx:1145-1174`
- `WorkflowBuilderPage.tsx:591-604`
- `hooks/useWorkflowDraftSync.ts:75-90`

`handleSetStatus` does not set `isSaving`. `SetWorkflowDraftStatus` publishes version N+1. If the
push arrives before the RPC response updates `version`:
- With unsaved edits (the "Move to draft" button at L1670 isn't gated on them), the user gets the
  "This workflow was updated elsewhere" toast for their own action. Clicking **Reload** throws away
  their own edits.
- Without unsaved edits, it causes a redundant refetch and canvas rebuild.

It's a race, so it will show up intermittently. Fix: pass `isSaving: isSaving || isChangingStatus`.

---

## Minor / Low

- **L-1. `UpdateWorkflowForkedFrom` bypasses the single publish point**
  (`repository_impl.go:4292`; caller `grpc/services/workflow.go:780`, CopyWorkflow). It bumps
  `version` but publishes nothing, which breaks "every draft write announces itself". Anyone holding
  the post-save version gets an unexplained OCC conflict. Route it through
  `publishWorkflowDraftUpdated`, or stop bumping `version` for this metadata-only write. The raw
  `DELETE` in `accountpurge.go:242` is fine: it purges the whole user.
- **L-2. The open editor never sees a delete.** Delete publishes the last version without bumping
  it, and the client gate is `update.version <= current.version`, so the push is dropped. The
  payload also has no discriminator, so a delete can't be told apart from an update even if it got
  through. Add `"deleted": true` to `data_json` and handle it (e.g. navigate to the Library), or
  document that it is ignored on purpose.
- **L-3. Out-of-order refetches can apply a stale version** (`useWorkflowDraftSync.ts:49-60`).
  `latest.current.version` only changes on the next render. Two pushes in flight can resolve as
  N+2 then N+1 before a re-render, and N+1 then overwrites N+2. There is one uncoalesced GET per
  push. Fix: keep a synchronous `appliedVersionRef`, and single-flight the fetch, re-fetching once
  if more pushes arrived meanwhile. No storm in practice: every user action produces exactly one
  publish (see "What's fine").
- **L-4. `/workflow/new?chat=X` and reload** (`WorkflowBuilderPage.tsx:251-300`,
  `WorkflowPage.tsx:59-68`). A reload creates a new draft but keeps `?chat=X`, whose prefill named
  the old draft. Canvas and chat then refer to different workflows. Replacing the URL with
  `/workflow/<slug>` (keeping search) after `createWorkflowDraft` fixes this and the existing
  "reload creates another draft" bug.
- **L-5. A `?chat=` deep link has global side effects** (`components/runs/loadRunContext.ts:40-57`
  via `useRunRoute`). It switches the app's current project and worktree to the chat's, sets the
  global `activeChatId`, and pushes onto the persisted nav queue. That's acceptable for "the panel
  IS a normal chat", but a pasted `?chat=` for another project's chat silently switches the project
  under the editor.
- **L-6. A tag with no registry tools loads nothing, silently.** `load_tool(name="tag:mcp")` passes
  the `TagDescriptions` check, but no registry tool carries `mcp`, so it reports "0 loaded, 0
  refused" (`load_tool.go:92-111`). Say so in the response, or expand MCP names the way
  `loadable_tools` does.
- **L-7. `WorkflowParseErrorView.tsx:56-60` resubscribes on every render.** `onFixed` is a new
  function each page render, so the draft subscription is torn down and recreated each time.
  Harmless; use a ref.
- **Nits:**
  - Removed proto fields reserve their numbers but not their names (`workflow.proto:94,176,229,249`).
    `reserved "is_valid"` in the same file is the precedent.
  - `generated/docs/tools-reference.md` is a tracked orphan last touched 2026-09-16, not written by
    `make generate-tools-ref`. It still says `id` is optional / "defaults to the workflow this chat
    is editing" (7 places). Regenerate or delete it.
  - `docs/workflows/custom-workflows.mdx:40`: the heading still reads "AI Builder Assistant" above
    text saying it's a normal chat.
  - The `-- NOTE:` added at the top of `workflow_drafts.sql` becomes part of the generated doc
    comment on `CreateWorkflowDraft` / `querier.go`.
  - `publishWorkflowDraftUpdatedByID` re-SELECTs a row that the store already got from
    `RETURNING *` and discarded. Returning it from the store saves a query per write.

---

## Tests

- **T-1. `TestWorkflowTools_RequireID` passes both before and after this change**
  (`internal/llm/tools/workflow_resolve_test.go`, ~L115-155).
  - It sends `{}`, which JSON-schema validation rejects ("missing properties: id") before the
    resolver runs.
  - Before this change, `{}` reached the resolver and failed anyway, because the test's draft is not
    bound to the chat.
  - `if err == nil { … }` also passes silently if `Run` returns an error.

  To pin the change: assert `ParamSchema().Required` contains `id` for each tool; call with
  `{"id":""}` and assert "`id` is required"; and bind a draft to the chat through raw SQL on the
  retained `chat_id` column, then assert it is **not** found.
- **T-2. The `WorkflowEditorChatPanel` prefill test checks the store, not the composer.**
  `NewChatView` is mocked and `workflowSlug` is present from the start, so it cannot catch I-1.
  There is also no test for the no-clobber rule. Add: render with `workflowSlug` undefined, then
  set it, then assert the ChatInput value. Seed a user draft and assert it is kept.
- **T-3. Missing tests:**
  - a status-change echo (I-6)
  - a push from someone else that arrives mid-save with a version above the saved one (should apply)
  - out-of-order fetch resolution (L-3)
  - `tag:<x>` refused by the permission ladder (e.g. `tag:runs` at `mutating` refuses
    `start_run`/`control_run`/`send_to_run`). Every `workflow` tool is base-tier, so the ladder is
    never exercised through tag loading.
  - H-1
- **Good:**
  - `TestWorkflowDraftWrites_EachPublishesOneUpdate` checks count +1, entity type, id, slug, version
    and nil project/chat after each of the 7 writes.
  - `TestWorkflowDraftWrite_RolledBackEmitsNothing` proves a rolled-back write leaves no row.
  - The `useWorkflowDraftSync` mid-save test is not vacuous: the `rerender` happens inside `act`,
    so the pending re-evaluation actually runs.
  - The load_tool tag tests assert loaded/refused counts and store contents.

---

## What's fine (checked)

- **Publish count and transaction.**
  - `CreateWorkflowDraft`, `UpsertWorkflowDraft`, `UpdateWorkflowDraft`,
    `UpdateWorkflowDraftDefinition`, `SetWorkflowDraftStatus`, `SetWorkflowDraftHidden`,
    `DeleteWorkflowDraft` and `DeleteWorkflowDraftBySlug` each publish exactly once, inside the
    write's tx.
  - The `user_updates` insert joins that tx, and the hub notification is registered with
    `runAfterCommit` on the same attempt. A rollback or serialization retry therefore never
    announces anything.
  - `user_id` is the draft owner and `version` comes from the written row. For an upsert that hits
    the slug conflict, the returned row's id is used, which is correct.
- **Delete read-then-delete** is not racy under the current SERIALIZABLE tx: an interleaving
  aborts and retries. It would need `RETURNING` if I-2 moves it to READ COMMITTED. `DeleteWorkflowDraft`
  (by id) has no non-test callers; the RPC uses by-slug, whose not-found case is a silent no-op as
  intended.
- **No bursts.** Every user action produces exactly one publish:
  - `SaveWorkflow`: an upsert *or* a rename-update
  - `ImportWorkflow`: one upsert
  - `SetWorkflowStatus`: only when the status actually changes
  - `SetWorkflowVisibility`: one
  - `create_workflow`, `edit_workflow`, `write_workflow`: one each
  - The one exception is CopyWorkflow's silent bump (L-1).
- **Own-save echo is handled.** Saves and test-run saves set `isSaving`, and a push held over is
  re-judged once `version` lands. The template-copy save switches draft id, so its echo is gated
  too.
- **`load_tool` tag loading** goes through `loadTool` for each name, so `CanLoadTool`, the
  permission ladder and Has/Add apply exactly as for a single load. Search applies
  `CanLoadTool` when `loadable_tools` is restricted. Above-permission tools show as
  "requires X permission", as name search already did.
- **`userUpdateMatchesProject`** exempts `WORKFLOW_DRAFT_UPDATED` and is used at both the live and
  backfill call sites (`streaming.go:287, 1238`). The client applies no project filter of its own.
- **Leftovers.**
  - In Go, nothing reads or writes `chat_id` in the domain or store layer. The queries
    `GetWorkflowDraftByChatID` / `AssociateChatWithDraft` are deleted, and `BuilderChat` /
    `AssociateChatWithWorkflowDraft` and `truncateString` are gone from services.
  - No live model-facing text says `id` can be omitted. Checked: tool descriptions, preset,
    regenerated `SKILL.md`, `docs/reference/tools.mdx`. The orphan file in the nits is the only
    hit.
  - On the web side, `builderChat`, `associateChatWithWorkflowDraft`, `builderChatId`,
    `useActiveBuilderChats`, the localStorage chat persistence and the hidden preset forcing are all
    gone.
- **`create_workflow`** does not touch chat binding, and its response tells the model to pass the
  returned `id`.
