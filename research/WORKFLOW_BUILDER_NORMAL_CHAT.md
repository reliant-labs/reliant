# Workflow builder → normal chat (de-magic)

Briefing for the parallel implementation of "the workflow editor's chat is just a
normal chat". Settled facts are marked SETTLED — do not re-derive them.

## Decision (from the user, SETTLED)

1. **No magic chat↔workflow binding.** Workflow/scenario tools REQUIRE an `id`
   (UUID, slug, or name). Nothing resolves "the workflow this chat is editing".
   Do not auto-attach a chat to a draft anywhere — not in `create_workflow`, not
   in the frontend.
2. **The workflow editor's chat is a normal chat.** No `workflow_builder` preset
   forced on it, no localStorage chat persistence keyed by workflow, no
   `associateChatWithWorkflowDraft`, no hidden prompt injection. It is the litmus
   test: if a normal chat can't build workflows well, that's the bug to fix.
3. Pre-launch: no backwards compatibility. Delete old paths; don't shim.

## Shared contract (SETTLED — already landed in proto + regenerated gen)

`proto/reliant/v1/workflow.proto`:
- REMOVED rpcs `BuilderChat`, `AssociateChatWithWorkflowDraft` and messages
  `BuilderChatRequest/Response`, `ToolCallInfo`,
  `AssociateChatWithWorkflowDraftRequest/Response`.
- `builder_chat_id` REMOVED (field numbers reserved) from `Workflow` (13),
  `GetWorkflowResponse` (4), `SaveWorkflowRequest` (6), `SaveWorkflowResponse` (9).
- `CreateWorkflowDraft` stays (the canvas needs a draft row for "New workflow").

`proto/reliant/v1/streaming.proto`:
- NEW `UserUpdateType.USER_UPDATE_TYPE_WORKFLOW_DRAFT_UPDATED = 21`
- NEW `EntityType.ENTITY_TYPE_WORKFLOW_DRAFT = 6`
- Payload contract for that update:
  - `entity_type = ENTITY_TYPE_WORKFLOW_DRAFT`, `entity_id = <draft id>`
  - `data_json = {"draft_id": "<id>", "slug": "<slug>", "version": <int64>}`
  - `project_id`, `chat_id`, `worktree_id` unset (drafts are user-scoped).
- This is how an open workflow editor learns that an agent (in ANY chat) edited
  the draft it shows. It replaces the old "watch my own builder chat's tool
  results and refetch" coupling.

Other agents have unrelated in-flight edits in `workflow.proto` (`Workflow.title = 19`)
and in tools_daemon/catalog/daemon_registry/workflow_v2 protos. Leave them alone.

## Where things are today (SETTLED)

Backend (`reliant/`):
- `internal/llm/tools/workflow_resolve.go` — `resolveWorkflowDraft(ctx, repo, idOrName)`;
  step 3 falls back to `repo.GetWorkflowDraftByChatID(ctx, ctx.ChatID)`. Callers:
  `workflow_editing.go:260,438`, `workflow_discovery.go:285`,
  `scenario_tools.go:95,189,305,471,595,674`.
- `ID` param structs with `json:"id,omitempty"` + "Optional — defaults to the
  workflow this chat is editing": `EditWorkflowParams`, `WriteWorkflowParams`
  (workflow_editing.go), `GetWorkflowParams` (workflow_discovery.go), and
  `ListScenarios/ViewScenario/EditScenario/WriteScenario/DeleteScenario/RunScenario`
  Params (scenario_tools.go). Description strings repeat the chat default at
  workflow_editing.go:218,382; workflow_discovery.go:222,252;
  scenario_tools.go:159,265,432,565,644; workflow_resolve.go:84-91.
- `create_workflow` (workflow_editing.go ~84-175) never touched chat_id. Keep it
  that way; its response must tell the model to pass the returned `id`.
- DB: `workflow_drafts.chat_id` column; queries `GetWorkflowDraftByChatID`,
  `AssociateChatWithDraft` in `internal/db/postgres/queries/workflow_drafts.sql`;
  `ChatID` on `core.WorkflowDraft`; repository plumbing in
  `internal/db/core/workflow_catalog.go`, `internal/db/repository.go`,
  `internal/db/repository_impl.go`, `internal/db/postgres/workflow_catalog_store.go`.
  Schema snapshot `internal/db/postgres/schema.sql`. sqlc via `make sqlc`.
  Migrations: `goose -dir internal/db/migrations/postgres create <name> sql`
  (never hand-write the version). Up-only, no down section.
- gRPC: `internal/grpc/services/workflow.go` — `BuilderChat` (stub, ~1341),
  `AssociateChatWithWorkflowDraft` (~945), `BuilderChatId:` populated at ~270,
  686, 1020, 1091, 1103; `SaveWorkflow` reads `req.Msg.BuilderChatId` (~610-629).
- User-update publishing: `repo.CreateUserUpdate(ctx, &db.UserUpdate{...})`
  (`internal/db/repo.go:1469`); type constants in `internal/db/models.go:380-432`.
  Writes to drafts happen in the tools (create/edit/write workflow, scenario
  writes do not change the draft row) and in `WorkflowService`
  (SaveWorkflow, CreateWorkflowDraft, SetWorkflowStatus, SetWorkflowVisibility,
  CopyWorkflow, Import…).
- Preset `internal/workflow/builtin/presets/workflow_builder.yaml` (system prompt
  says "operate on the workflow this chat is editing — call them with no id").
  Also used by `build-workflow.yaml` (implementer_preset: workflow_builder) —
  keep the preset, fix its prompt.
- Skill text is GENERATED: edit `tools/docgen/assembler/main.go` (lines ~130-160
  and ~355-365), then `make generate-workflow-builder-skill`. Never hand-edit
  `internal/skills/catalog/builtin/workflow-builder/SKILL.md`.
- Tool reference docs generated: `make generate-tools-ref` (+ mintlify reference).

load_tool (`internal/llm/tools/load_tool.go`, `loaded_tools_store.go:305`):
- `query` mode only LISTS matches, and matches tool NAME substrings only —
  "workflow" misses get_schema, get_cel_reference, list_presets, get_preset and
  all six scenario tools. Loading is one tool per call by exact name.
- Builtin agent sets `loadable_tools: ["*"]`; enforcement via
  `store.CanLoadTool(scopeKey, name)` + `PermissionAtLeast`.

Frontend (`reliant/web/src/`):
- `components/workflow/WorkflowBuilderChat.tsx` (909 lines) — the magic chat.
  `WorkflowBuilderChat.modelSelection.ts` (+ tests) — builder-only model/thinking
  plumbing, `WORKFLOW_BUILDER_PRESET`.
- `components/workflow/WorkflowBuilder.tsx` — renders it (~1968), `builderChatId`
  prop, `useIsChatRunning(builderChatId)` exit guard (~290, ~610-640),
  `handleChatWorkflowUpdate` (~1117) applies a refetched workflow to canvas/loop
  stack, `useBuilderTestRun(testRunChatId, nodeIds, builderChatId)` (~893).
- `components/workflow/WorkflowBuilderPage.tsx` — `builderChatId` state, passes
  `builderChatId` into save, `handleChatIdChange`.
- `components/workflow/hooks/useBuilderTestRun.ts` — re-subscribes builderChatId
  after a test run.
- `store/chatStoreHooks.ts` `useActiveBuilderChats` (no callers — delete).
- `api/workflow-grpc.ts` — `builderChat`, `associateChatWithWorkflowDraft`,
  `builderChatId` fields.
- Normal chat UI: `components/Chat/ChatContainer.tsx` (`tabId` = chatId; renders
  an existing chat fully) and `components/Chat/NewChatView.tsx`
  (`onChatCreated` callback; starts via `useChatStore.getState().startChat(...)`).
- User updates arrive in `store/globalUpdatesStore.ts` `handleUpdate` (~317).

## Additional decisions (SETTLED)

- **`load_tool` tag loading.** `load_tool(name="tag:workflow")` loads every tool
  carrying that registry tag in one call, each one still individually gated by
  `CanLoadTool` (loadable_tools) and the permission ladder; the response lists
  what loaded and what was refused and why. `query` search also matches tag
  names (so `query="workflow"` finds all 16, scenario tools included). Skill and
  preset text tell a normal chat to use `load_tool(name="tag:workflow")`.
- **`workflow_drafts.chat_id` is retired expand-then-contract.** This change
  stops every read and write of it (no query selects it into the domain type,
  nothing inserts/updates it, `core.WorkflowDraft.ChatID` is deleted, the
  `GetWorkflowDraftByChatID` / `AssociateChatWithDraft` queries are deleted). The
  column itself is NOT dropped here — the previous release still reads it during
  a rolling deploy. Dropping it is a follow-up migration once this has shipped.
  No down migration, ever.
- **WORKFLOW_DRAFT_UPDATED is published from the repository layer**, on every
  successful draft write (create, upsert, update, update-definition, set-status,
  set-hidden, delete). One choke point, so no writer — tool or RPC — can forget.
  Precedent: `Repo` already emits user updates (`internal/db/repo.go` ~1656, ~1760).
- **Editor chat identity is explicit UI state, not a DB binding.** The editor
  panel's current chat id lives in the workflow route's search params
  (e.g. `?chat=<id>`), so reload/back keeps it, and nothing on the server knows.
- **Telling the agent which workflow is the user's message, visibly.** When a
  chat is started from the editor, the composer is PREFILLED (visible, editable,
  deletable) with a reference to the workflow by slug, e.g.
  "Workflow `swift-fox-a1b2`: ". Nothing hidden is injected.
- `CreateWorkflowDraft` (the canvas's "New workflow") stays — it's manual-editing
  plumbing, not chat plumbing.

## Verification gates

- Go: `go build ./... && go test ./internal/llm/tools/... ./internal/grpc/services/... ./internal/db/...`
  (tests need the reliant postgres on localhost:5433 — `make postgres-up`).
- Web: from `web/`, `npm run typecheck` (= `tsc -b`) and `npx vitest run <paths>`.
  NOT `npx tsc --noEmit -p .` — the root `web/tsconfig.json` is `files: []` plus
  references, so that command checks nothing and always "passes". This
  briefing originally named it, and it let five real type errors (one a runtime
  ReferenceError on YAML Apply) through the first round.
- Every new behavior gets a test that fails before and passes after.
