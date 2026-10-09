# Triggers: one door for starting runs

**Status:** in progress on branch `triggers`. This is the working design and the
shared briefing for every agent on this work. Facts marked *settled* were
verified against this checkout. Do not re-derive them.

Prior art this builds on: `ENGINE_SPLIT_PLAN.md` ("New: `TriggerService`",
"Split: `ChatService` → conversation + run control").

---

## The model

Three nouns. Everything else is built from them.

| Noun | What it is | Stored? |
|---|---|---|
| **Trigger** | A standing instruction that starts a run without a human typing: "every weekday at 9am, run workflow W in project P with prompt M". Kinds: `schedule` now; `webhook`, GitHub events and manual buttons later. | Yes, in `triggers`. |
| **Trigger event** | One firing: kind, dedupe key, when it happened, payload, and outcome (`launched` / `skipped` / `failed`) plus the chat it launched. | Yes, in `trigger_events`. |
| **Launcher** | The ONE code path that turns `(event, spec)` into a running session: chat row + root workflow + thread + seed messages + event row + Temporal start. | No. It's code (`internal/launch`). |

An **interactive chat start is a trigger event too**, of kind `chat.start`. It
has no stored definition (it's ad hoc) and its payload is the user's message.
`StartChat` (the renamed `CreateChat`) produces it.

### What is NOT a trigger

`SendMessage` never fires a trigger. It handles a chat that has already started:

| Root run state | `SendMessage` does |
|---|---|
| active | save message, wake thread |
| paused | save, resume, wake |
| failed | reset-and-replay, or coarse resume |
| completed / cancelled | start a new run **in the same chat** (continuation, not a trigger) |
| **pending (never started)** | **rejected**: `FailedPrecondition` "chat has not started; use StartChat" |

The pending case covers branched chats. `BranchChat` creates a chat whose root
workflow is `pending` with a forked thread. Its first send is
`StartChat(chat_id=<branch>, message)`, not `SendMessage`.

### Why the event row matters

`trigger_events` has `UNIQUE (kind, dedupe_key)`.

- For `chat.start` the dedupe key is the chat id. So "a chat is started
  exactly once" is enforced by the database, not by client discipline.
- For `schedule` the dedupe key is the Temporal fire-workflow id, which is
  unique per scheduled time. So a retried fire cannot launch twice.

The event row is the durable record of intent. The Temporal start is the step
that brings the run in line with that intent, and it is safe to retry because
the workflow id is deterministic (it is the chat id).

---

## Settled facts (verified; do not re-derive)

**Start paths today**

- `CreateChat` (`internal/grpc/services/chat_crud.go:30-305`):
  - chat id = root workflow id = root thread id.
  - One transaction creates the chat, the `pending` root workflow, the thread,
    the messages and the `chat_created` user update.
  - It then calls Temporal `ExecuteWorkflow(v2.DynamicWorkflow)` with
    `TERMINATE_EXISTING`, `runs.RecordRun`, and starts `GenerateTitleWorkflow`.
- `SendMessage` (`chat_send.go:415`): the status switch is at ~:525.
  - Pending chats fall through to a fresh start (~:921-1100), including a
    workflow-switch branch (~:938-968) that only works while pending.
  - There is a legacy "chat has no workflow id" branch (~:930-936).
- `BranchChat` (`chat_branch.go:22-306`) creates a `pending` chat with a forked
  thread and starts nothing.
- `RunService.StartRun` (`run.go:146`):
  - Without a session, it goes through `CreateChat` and uses `defaultProjectID`
    (the most recently active project).
  - With a session, it goes through `SendMessage`.
- CLI `reliant workflow run` uses `CreateChat` (`cmd/reliant/commands/workflow.go:340-355`).
- Web callers:
  - `web/src/api/chat-grpc.ts:269` (create), `:416` (send), `:700` (branch)
  - `web/src/components/Chat/ChatContainer.tsx:163,184`
  - `NewChatView.tsx:195`, `Mobile/MobileNewChat.tsx:140`
  - `workflow/WorkflowBuilderChat.tsx:~580,616`
  - `store/chatStore.ts:1100` (createChat), `:1512` (sendMessage), `:3391` (branchChat)
- Tests that construct `CreateChatRequest`: 8 files, including
  `e2e/stories/harness_test.go` and
  `internal/workflow/runtime/replaytest/harness_gen_test.go` (build tag `replayfixtures`).
- `/reliant.v1.ChatService/CreateChat` is also named in the timeout table at
  `internal/grpc/interceptors/auth.go:356`.

**Start-path helpers** (the code to move into `internal/launch`). These are
`ChatService` methods:

- `chat_workflow.go`: `resolveDefaultWorkflow`, `buildWorkflowInputs`,
  `loadWorkflowInputsForBuild`, `validateWorkflowInputs` (reads
  `auth.MustGetUserID(ctx)`), `loadWorkflowForValidation`,
  `loadCreateChatWorkflowForValidation`, `validateCreateChatWorkflowTree`,
  `createDBPresetLoader(Full)`, `loadPresetFromDB`, `buildStateUpdateForActiveWorkflow`
- `chat_helpers.go`: `resolveChatWorktreeID`, `getEffectiveWorkingPath`,
  `normalizeModelInputs`, `extractModelSelectors`, `dbPresetToRuntimePreset`,
  `normalizeWorkflowSlug`
- `chat_greenfield.go`: `greenfieldGuidanceForChat`, `maybeGreenfieldGuidance`
- `workflow.go:326`: `loadProjectWorkflowBySlugFromDB` (also used by `WorkflowService`)

The worker (`internal/serverworker`, `internal/workersetup`) does **not** import
`internal/grpc/services`. Anything a scheduled fire needs must therefore live
outside the services package.

**Chat proto bug**

`Chat.workflow_state` and `Chat.workflow_stop_reason` (`chat.proto` fields 31
and 32) are **never populated**:

- `chatToProto` (`chat_helpers.go:109`) does not set them.
- No Go code assigns them.
- The web still reads them (`ChatInput.tsx:1110`, `isWorkflowPaused`), so the
  web's paused detection is currently always false.

**Spawns**

- A spawn is a goroutine inside the chat's root Temporal execution, not a
  Temporal execution of its own.
- To stop one, send the `cancel_thread` signal to the chat's ROOT workflow
  (`internal/workflow/runtime/workflow.go:2391-2440`) with `{thread, tool_call_id}`.
- `ToolCallService.cancelChildWorkflowForToolCall`
  (`internal/grpc/services/tool_call.go:132-216`) does that signal, then CASes
  the child workflow row from active/paused to cancelled.
- `spawn_send` (`internal/llm/tools/spawn_send.go`) shows the
  parent↔child relationship check (`repo.ListSpawnChildren`) and the injected
  notifier pattern:
  - leaf contract package `internal/workflow/threadwake`
  - implementation `internal/temporal/agent_message_notifier.go`
  - wired at `internal/serverworker/run.go:184` and `internal/serverapi/run.go:230`
- `child_workflow_id` on a tool call is not guaranteed to equal the child
  thread id. Send BOTH the thread and the tool call id.

**Temporal**

- SDK v1.48 has `client.ScheduleClient()`.
- `temporal` CLI 1.4.1 (Server 1.28) is on PATH.
- Tests boot an ephemeral dev server with `testsuite.StartDevServer`; the
  pattern is in `e2e/stories/main_test.go:105-135`.
- Deployed server is `temporalio/auto-setup:1.26.2`, which supports schedules.
- The worker registers `DynamicWorkflow` and `GenerateTitleWorkflow` at
  `internal/workersetup/setup.go:176-187`.

**Identity**

- Activities re-derive the user from the chat row.
- LLM calls use per-user provider keys stored in settings. The Reliant provider
  key is a stored `rlat_` `llm:invoke` token. A run without a user JWT can
  therefore still call the LLM.
- The JWT is needed only for control-plane daemon resolution
  (`internal/toolexec/daemon_router_nats.go:294`) and resume
  (`internal/mcpserver/resumer.go`).
- So scheduled runs that need a **cloud** daemon need the delegated-credential
  follow-up. LLM-only runs and local-daemon runs do not.

**Unattended runs**

`internal/workflow/runtime/unattended.go`:

- `IsUnattended(inputs)` reads the `unattended` input.
- `ask_user` and `ask_question` auto-resolve instead of blocking.

**Repo conventions**

- No down migrations, ever. Create a migration with
  `make migration NAME=<snake_name>`.
- `internal/db/postgres/schema.sql` is generated by
  `scripts/generate-schema-sql.sh` (disposable docker Postgres 16).
- Then run `make sqlc`.
- Protos: `make proto-generate` (remote buf plugins), `make proto-lint`.
- Tool catalog: `make generate-tool-catalog`, `make generate-tools-ref`.
- DB tests skip unless Postgres is reachable. Set
  `DATABASE_URL=postgres://postgres:postgres@localhost:<port>/reliant?sslmode=disable REQUIRE_TEST_DB=1`.
  The per-test DB cloning in `internal/db/testutil.go` is safe across
  concurrent processes.

---

## Contracts

### `internal/launch`: the one door (package name is fixed)

```go
type EventKind string
const (
	EventKindChatStart EventKind = "chat.start"
	EventKindSchedule  EventKind = "schedule"
)

// Event is the uniform envelope every trigger source produces.
type Event struct {
	Kind       EventKind
	TriggerID  string         // stored trigger that fired; "" for ad hoc kinds
	DedupeKey  string         // unique within Kind
	OccurredAt time.Time
	Payload    map[string]any // recorded verbatim on the event row
}

type SeedMessage struct {
	Role         reliantv1.MessageRole
	Content      string
	DisplayStyle *reliantv1.DisplayStyle
}

type Spec struct {
	OwnerUserID string
	ProjectID   string
	WorktreeID  *string // nil → project's main worktree
	ChatID      string  // "" → create a chat; else start this PENDING chat (branch)
	NewChatID   string  // id for a created chat; "" → random. Idempotent sources derive it.
	Title       *string
	Workflow    string  // "" → owner's default workflow
	Presets     map[string]string
	Params      map[string]*structpb.Value
	Mode        *string
	Messages    []SeedMessage
	Attachments []string

	Unattended      bool // inputs.unattended = true; no human will answer
	GenerateTitle   bool // run GenerateTitleWorkflow from the first user message
	GreenfieldProbe bool // probe the working dir and prepend greenfield guidance
}

type Result struct {
	Chat       *db.Chat
	WorkflowID string
	RunID      string
	EventID    string
}

// Launch materializes the session and starts its root run.
func (l *Launcher) Launch(ctx context.Context, ev Event, spec Spec) (*Result, error)
```

Rules:

- **userID is always explicit.** No `auth.*FromContext` reads inside `launch`.
  It runs on the worker, where there is no request context.
- **No `connect` import.** Return domain errors and let handlers map them to
  connect codes.
- **`ErrAlreadyLaunched` is a sentinel.** Launching an already-launched
  `(Kind, DedupeKey)`, or an existing `NewChatID`, returns an error with
  `errors.Is(err, launch.ErrAlreadyLaunched)` that carries the existing chat id.
- **Temporal start uses `WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING`.** The
  workflow id is the chat id, so a retry attaches instead of terminating.
  `SendMessage`'s restart paths keep `TERMINATE_EXISTING`; they are
  continuations, not launches.
- **Launch is resumable.** If the event row and session committed but the
  Temporal start never happened, re-launching with the same dedupe key issues
  the start instead of reporting a duplicate. The event row tells "half
  launched" apart from "pending branch awaiting its start".

### Shared Go types (ALREADY WRITTEN — code against them, do not redesign)

- `internal/db/core/trigger.go`:
  - types: `Trigger`, `ScheduleConfig`, `TriggerEvent`, `TriggerFilters`
  - kind/outcome constants
  - `ErrTriggerNotFound`, `ErrTriggerEventNotFound`
- `internal/launch/contract.go`:
  - `Event`, `Spec`, `SeedMessage`, `Result`
  - errors: `ErrAlreadyLaunched` / `*AlreadyLaunchedError`, `ErrNotPending`,
    `*ValidationError`, `ErrNotFound`
  - Agent C1 adds the `Launcher` implementation beside it.

**Repository methods agent B adds**:

- Exact signatures, on `db.Repository` and `*db.Repo`.
- Consumers declare narrow interfaces with these same signatures, so
  `*db.Repo` satisfies them structurally.

```go
CreateTrigger(ctx context.Context, t *core.Trigger) error
GetTrigger(ctx context.Context, id string) (*core.Trigger, error)            // core.ErrTriggerNotFound
ListTriggers(ctx context.Context, f core.TriggerFilters) ([]*core.Trigger, error)
UpdateTrigger(ctx context.Context, t *core.Trigger) error                    // core.ErrTriggerNotFound
DeleteTrigger(ctx context.Context, id string) error
SetTriggerEnabled(ctx context.Context, id string, enabled bool) error        // core.ErrTriggerNotFound

// Insert; ON CONFLICT (kind, dedupe_key) DO NOTHING. created=false means a row
// for that (kind, dedupe_key) already existed and nothing was written.
CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error) // core.ErrTriggerEventNotFound
UpdateTriggerEventOutcome(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error
ListTriggerEvents(ctx context.Context, triggerID string, limit int) ([]*core.TriggerEvent, error) // newest first
// Latest event for the trigger, optionally filtered by outcome; (nil, nil) when none.
GetLatestTriggerEvent(ctx context.Context, triggerID string, outcome *core.TriggerEventOutcome) (*core.TriggerEvent, error)
```

All of them must honor the ambient transaction from `RunTx`, like every other
repo method, so the launcher can insert the event row in the same transaction
as the chat.

### Database (migration by agent B)

`triggers`:

| Column | Type / notes |
|---|---|
| `id` | text, PK |
| `user_id` | text, not null. The owner, and the identity the run executes as. |
| `project_id` | text, not null, FK `projects` `ON DELETE CASCADE` |
| `worktree_id` | text, nullable, FK `worktrees` `ON DELETE SET NULL` |
| `name` | text, not null. `UNIQUE (user_id, project_id, name)` |
| `kind` | text, not null, `CHECK (kind IN ('schedule'))` |
| `enabled` | bool, not null, default true |
| `workflow` | text, not null |
| `presets` | jsonb, default `{}` |
| `params` | jsonb, default `{}` |
| `message` | text, not null, default `''`. The seed prompt. |
| `config` | jsonb, not null. Kind-specific source config. |
| `created_at`, `updated_at` | timestamptz, not null |

Indexes on `(user_id)` and `(project_id)`.

`trigger_events`:

| Column | Type / notes |
|---|---|
| `id` | text, PK |
| `trigger_id` | text, nullable, FK `triggers` `ON DELETE SET NULL` |
| `user_id` | text, not null |
| `kind` | text, not null, `CHECK (kind IN ('chat.start','schedule'))` |
| `dedupe_key` | text, not null. `UNIQUE (kind, dedupe_key)` |
| `occurred_at` | timestamptz, not null |
| `payload` | jsonb, default `{}` |
| `outcome` | text, not null, `CHECK (outcome IN ('launched','skipped','failed'))` |
| `outcome_detail` | text, default `''` |
| `chat_id` | text, nullable, FK `chats` `ON DELETE SET NULL` |
| `created_at` | timestamptz, not null |

Indexes on `(trigger_id, occurred_at DESC)` and `(chat_id)`.

### Wire (proto by agent B): `proto/reliant/v1/trigger.proto`

`TriggerService` RPCs:

- `CreateTrigger`, `GetTrigger`, `ListTriggers` (optional `project_id`), `UpdateTrigger`
- `DeleteTrigger`, `SetTriggerEnabled`
- `FireTrigger` (run now)
- `ListTriggerEvents` (`trigger_id`, `limit`)

`Trigger`:

- fields: `id`, `name`, `project_id`, `optional worktree_id`, `enabled`,
  `workflow`, `map<string,string> presets`, `map<string, Value> params`,
  `message`
- `oneof source { ScheduleSource schedule = 20; }`. Webhook and GitHub become
  new arms.
- timestamps `created_at` and `updated_at` as RFC3339 strings, matching `chat.proto`
- read-only: `optional next_fire_at`, `optional TriggerEvent last_event`

`ScheduleSource`:

- `repeated string cron` (5-field, union)
- `optional string interval` (Go duration)
- `string timezone` (IANA, default `UTC`)
- `TriggerOverlapPolicy overlap` (`SKIP` default: don't fire while this
  trigger's previous run is active or paused; `ALLOW`)
- `optional string catchup_window` (Go duration; default `10m`, deliberately
  not Temporal's 1 year)

`TriggerEvent`: `id`, `optional trigger_id`, `kind`, `occurred_at`,
`TriggerEventOutcome outcome`, `outcome_detail`, `optional chat_id`,
`google.protobuf.Struct payload`.

### Schedules on Temporal (agent E)

**Write path.** The API stores the `triggers` row; Temporal is converged to it.

- One Temporal Schedule per trigger, with id `trigger-<id>`.
- Paused exactly when the trigger is disabled.
- Its action starts `TriggerFireWorkflow` on the shared task queue with id
  `trigger-fire-<id>` (Temporal appends the scheduled time) and input
  `{trigger_id}`.
- Writes sync the schedule immediately.
- An idempotent `SyncAll` at api-server startup repairs drift.

**Fire path.** Runs on the worker.

1. `TriggerFireWorkflow` runs one activity, `FireTrigger`.
2. The activity loads the trigger. If it is deleted or disabled, it records a
   `skipped` event, unless the fire is manual.
3. Overlap `SKIP`: if the latest `launched` event's chat root is active or
   paused, record `skipped` and stop.
4. It builds a `Spec`:
   - owner = `trigger.user_id`
   - `Unattended`, no title generation, no greenfield probe
   - title `"<name> · <time>"`
   - user message `trigger.message`, plus a hidden system message saying the
     run was started by schedule X for time T and no human is watching
   - `NewChatID = uuid.NewSHA1(<fixed namespace>, fire workflow id)`
5. It builds the `Event`:
   - kind `schedule`
   - dedupe key = fire workflow id
   - occurred at = scheduled time
   - payload `{scheduled_for, trigger_name}`
6. It calls `launch.Launch`:
   - `ErrAlreadyLaunched` → success.
   - Validation failures (missing workflow, unavailable model) → `failed`
     event, non-retryable.

`FireTrigger` (run now) starts the fire workflow directly with
`{trigger_id, manual: true}`. A manual fire skips the enabled and overlap
checks.

---

## Work split

| Agent | Wave | Owns (may edit) | Must not touch |
|---|---|---|---|
| A: `spawn_stop` | 1 | `internal/llm/tools/**`, new leaf `internal/workflow/threadcancel`, `internal/workflow/runtime/workflow.go` (alias the signal only), `internal/workflow/runtime/activities/handlers/call_llm.go` (spawn guidance text / tool reachability only), `internal/temporal/**` or a new small package for the stopper, `internal/grpc/services/tool_call.go`, `ToolsOptions` blocks in `internal/serverworker/run.go` + `internal/serverapi/run.go`, generated tool catalog/docs | chat_*.go, launch, db, proto |
| B: schema + proto | 1 | `proto/reliant/v1/trigger.proto`, `gen/**` + `web/src/gen/**` (via `make proto-generate` only), new migration, `schema.sql`, `internal/db/postgres/{queries/triggers.sql,generated/**,trigger_store.go}`, `internal/db/core/trigger.go`, `internal/db/{repository.go,repository_impl.go,models.go}` | services, tools, launch |
| C1: launch extraction | 1 | new `internal/launch/**`, `internal/grpc/services/{chat.go,chat_crud.go,chat_send.go,chat_workflow.go,chat_helpers.go,chat_greenfield.go,chat_branch.go,workflow.go,run.go}` + their tests | tool_call.go, tools, db, proto, web |
| C2: StartChat migration | 2 | chat.proto/run.proto, regenerate, launch (event recording, pending start, resumability), chat services, `auth.go` timeout key, CLI `workflow run`, web callers, e2e/replay harnesses, view migration for `workflow_state` | triggers pkg, workersetup |
| E: schedule triggers | 2 | `internal/triggers/**`, `internal/grpc/services/trigger.go`, `internal/grpc/server.go` (register), `internal/workersetup/**`, `internal/serverapi/run.go` (startup sync), `cmd/reliant/commands/trigger.go` (+ root registration) | chat*.go, launch, proto/gen, db schema |

Rules for every agent:

- **No git commands** other than read-only ones (`status`, `diff`, `log`).
  Never commit, stash, checkout or reset.
- **Other agents edit this worktree concurrently.** A build error in a file you
  don't own is someone else's in-flight edit. Wait and retry; never "fix" it.
- **Test databases.** Use the dedicated test Postgres containers: `55433` for
  most agents, `55434` for agent B. Don't use 5433 or 5434.
- **Tests come with every behavior.** A new test must fail before the fix and
  pass after.
- **Report actual command output.**

---

## Deliberately later (in the plan, not in this wave)

- **CEL `trigger` root.** Reserved in `v3/reference/cel_reference.go:80`;
  populate it from the event payload.
- **Run origin in the UI.** Hide automation chats from the sidebar unless they
  need attention.
- **Delegated automation credential.** A scoped `rlat_` `daemon:resume` token,
  so unattended runs can wake cloud daemons.
- **Daemon availability service (#558).**
- **Per-run worktree mode** for triggers.
- **Webhook, GitHub and manual/form triggers.**
- **Run-management tools for agents:** `start_run`, `list_runs`, `control_run`,
  `send_to_run`.
- **Tool capability classes and mutation policy** (the #559 successor).
- **Triggers UI.**
