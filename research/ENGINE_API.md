# The engine API surface

**Status:** proposal. Concrete service list, derived from the 29 services and
~250 RPCs that exist today (`proto/reliant/v1/`).

The question this answers: **if reliant-engine shipped as a product with no
coding features at all, what would its API be?** Everything else is
reliant-coding, consuming that API as customer #1.

## Framing: chat is an engine concept, not a coding one

An earlier draft of this document split `ChatService` down the middle — run
control to the engine, "the chat product object" to coding. That was wrong, and
it was wrong in the direction that matters.

**A chat is a workflow you talk to.** Users will build custom workflows and
drive them conversationally; that is a primary way the engine gets used, not a
coding-specific affordance. The frontend SDK — workflow editor plus chat
timeline — is intended to be open-sourced so anyone can embed both in their own
app. A timeline that only works for our product would be worthless for that.

At the same time a workflow stays a primitive in its own right: runnable
headless, on a trigger, in the background, with no conversation anywhere. Those
are two interfaces onto one execution model, not two products.

So the line is not "conversational vs. programmatic". It is **coding vs.
everything else**, and it is a much thinner line than the RPC counts suggest.

The `chats` table makes this concrete. Sixteen columns, of which exactly three
are coding: `project_id`, `worktree_id`, `archived_worktree_name`. The rest —
`id`, `title`, `user_id`, `state`, `workflow_id`, `run_id`, `workflow_name`,
`selected_presets`, `unread`, `last_active`, timestamps, `active_daemon_id` — is
generic session state that any conversational workflow needs.

Same story on the RPCs. `SearchChats`, `ListArchivedChats`, `DismissChat`,
`MarkUnreadChat`, `UpdateChatState` have nothing to do with code; they are
session management, and an embedder building a support console or a research
tool wants every one of them. What they should not be called is *chat* —
`SessionService` or `ConversationService` says what they are without implying an
IDE.

---

## The finding that shapes everything

**`WorkflowService` cannot run a workflow.**

Its 18 RPCs are authoring and CRUD — List/Save/Get/Delete/Validate/Import/
Export/SetVisibility/Copy, plus builder and scenario calls. Every actual run
starts through `ChatService.SendMessage` or `CreateChat`.

So the engine's central verb does not exist as an API. That is the single
biggest gap, and it is why the CLI calls a `/api/v1/workflows/run` endpoint that
is implemented nowhere.

`ChatService` holds that missing verb. It is not half-coding — it is the
engine's conversational surface under a coding-flavored name, with two RPCs of
genuine coding coupling.

---

## Engine services

### 1. RunService — NEW, and the point of the exercise

```protobuf
service RunService {
  rpc StartRun(StartRunRequest) returns (StartRunResponse);
  rpc SignalRun(SignalRunRequest) returns (SignalRunResponse);

  rpc GetRun(GetRunRequest) returns (GetRunResponse);
  rpc ListRuns(ListRunsRequest) returns (ListRunsResponse);

  rpc CancelRun(CancelRunRequest) returns (CancelRunResponse);
  rpc PauseRun(PauseRunRequest) returns (PauseRunResponse);
  rpc ResumeRun(ResumeRunRequest) returns (ResumeRunResponse);
  rpc InterruptRun(InterruptRunRequest) returns (InterruptRunResponse);

  rpc StreamRunEvents(StreamRunEventsRequest) returns (stream RunEvent);
}
```

`StartRun` takes a workflow ref, inputs, an optional trigger payload, and an
optional thread to run in. It does **not** require a message — that requirement
is a handler-layer 400 today, not an engine invariant.

`SignalRun` delivers an event to a live run and never starts one. This splits
`SendMessage`, which currently means both, disambiguated by run status rather
than by anything about the request.

The mechanism already exists and is good: durable `SaveMessageToThread` plus the
deliberately content-free `thread_wake` Temporal signal. Generalize the
envelope, keep the design.

### 2. SessionService — today's ChatService, renamed, minus two RPCs

A session is a durable conversation bound to a workflow. This is what the
embeddable timeline talks to, and it is engine API in full:

```
CreateSession  GetSession  UpdateSession  DeleteSession
ListSessions   SearchSessions  ListArchivedSessions
SendMessage    SendAgentMessage  ListQueuedAgentMessages  CancelQueuedAgentMessage
ListMessages   GetMessage
InterruptThread  PauseSession  ResumeSession  TerminateSession
CompactSession   UpdateWorkflowParams  GetSessionUpdates
GetWorkflowExecutions  GetThreadWorkflowInputs  ListSessionPlans
UpdateSessionState  DismissSession  MarkUnreadSession
```

Everything an embedder needs to build a conversational product: search,
archive, unread, queueing, interrupt, branch. None of it is about code.

Only two of today's 28 RPCs do not come along, and both for the same reason —
they take a worktree:

- `BranchChat` / `ListBranches` — branching a chat means forking the thread
  **and** creating a git worktree. The thread-fork half is engine (`ForkThread`
  below); the worktree half is coding. This is one RPC that needs splitting,
  not a category.
- `SetChatDaemon` — survives as `SetSessionRuntime` once daemon → runtime.

`CreateSessionRequest.project_id` (required today) becomes optional and
generic, and `worktree_id` leaves the engine message entirely.

Absorbs `MessageService` (1 RPC). Adds `ForkThread`, exposing the fork DAG that
already exists in `internal/threads` as an engine primitive rather than only as
a side effect of `BranchChat`.

Conversation state stays engine-side and is not optional: Temporal activities
read and idempotently write it on every step and across retries
(`messages.activity_id`). A product that owned this would be inside the durable
execution loop.

**Naming.** `SessionService` over `ChatService` because the embeddable SDK is
the point — a company putting this in their product should not find "chat"
meaning "our IDE's chat". `ConversationService` reads better in isolation but
collides with `conversation` as the LLM-turn concept. Either way the rename is
mechanical and worth doing while the surface is being restructured.

### 3. WorkflowService — definitions only

Keeps: `ListWorkflows`, `SaveWorkflow`, `GetWorkflow`, `DeleteWorkflow`,
`ValidateWorkflow`, `ImportWorkflow`, `ExportWorkflow`, `CopyWorkflow`,
`SetWorkflowVisibility`.

Loses to coding: `BuilderChat`, `CreateWorkflowDraft`,
`AssociateChatWithWorkflowDraft` — those are IDE authoring UX, not engine.

### 4. ScenarioService — unchanged

`ListScenarios`, `CreateScenario`, `RunScenario`, `DeleteScenario`,
`UploadScenario`, `ExportScenario`. Testing workflows is an engine concern; it
currently shares `workflow.proto` and should get its own file regardless
(one service per file).

### 5. ApprovalService + QuestionService — unchanged

`ListApprovalsByChat` → `ListApprovalsByRun`, `Approve`, `Deny`, `BatchApprove`,
`BatchDeny`; `ResolveQuestion`, `GetPendingQuestion`.

Generic human-in-the-loop. The approval-row + Temporal-signal + timeout flow is
replay-safe and loop-iteration-addressable — worth keeping exactly as built.

### 6. PlanService + TaskService — unchanged

Thread-scoped run state, already generic. 15 RPCs between them.

### 7. PresetService — unchanged

Tagged, validated partial param bundles. Genuinely generic — the only coding
flavor is the default tag `"agent"` and a `skills` param, which are data.

### 8. CatalogService — engine introspection

`ListModels`, `ListModelsByProvider`, `ListAvailableModels`, `ListTools`,
`ListNodes`, `GetCELCompletions`.

This is how an API consumer discovers what the engine can do — what node types
exist, what tools are available, what a CEL expression can reference. For a
programmable engine this is a headline API, not a support service.

### 9. AttachmentService — assets

`UploadAttachment`, `CreateFileReference`, `GetAttachment`, `DeleteAttachment`.

Already the right shape for the media work: handles, not inline bytes.

### 10. StreamingService + ToolCallService

`StreamUserUpdates` (→ run/thread events), `CancelToolCall`,
`ConvertToBackground`.

### 11. RuntimeService — from DaemonRegistryService, renamed and widened

```protobuf
service RuntimeService {
  rpc ListRuntimes / GetRuntime / ResolveRuntime / ResumeRuntime
  rpc Invoke(InvokeRequest) returns (InvokeResponse);
  rpc InvokeStream(InvokeStreamRequest) returns (stream InvokeEvent);
}
```

`Invoke(runtime, tool, params)` is the single generic surface for executing a
tool on a runtime — both LLM tool calls and direct UI calls. The generic daemon
envelope already exists on the wire (`DaemonCommandRequest`: string type + JSON
payload), so this formalizes what is already happening rather than inventing a
protocol.

**This is what `FileSystemService` (13), `TerminalService` (3),
`BackgroundService` (4) and `PackageCommandsService` (8) collapse into** — 28
RPCs replaced by one verb. Those services are product-shaped conveniences over
a generic capability; coding can keep them as thin wrappers if the UI wants
them, but they are not engine API.

"Daemon" → "runtime" throughout: `DaemonSelectorProto` is already a generic
executor predicate (id/name/type/labels). Only the name is coding-flavored.

### 12. RuntimeTokenService + ToolsDaemonService — transport

`CreateToken`/`ListTokens`/`RevokeToken` (+ the managed-mint pair), and
`ConnectDaemon`/`ConnectGateway`/`ReportToolResult`.

The transport half is engine; the *fleet* (pods, images, prewarm, preview proxy)
is coding.

### 13. SystemService — engine half only

`Health`, `Ready`, `Info`, `Version`. The OAuth and DevAuth RPCs are product.

### 14. TriggerService — NEW, after the run-container work

```protobuf
service TriggerService {
  rpc CreateTrigger / ListTriggers / GetTrigger / DeleteTrigger
  rpc SetTriggerEnabled
}
```

Deliberately last. It depends on a run existing without a chat, which is the
migration in `ENGINE_SPLIT_PLAN.md`.

---

## Coding services — leave the engine surface

This is the actual split, and it is narrow — four services and change, all of
them about **a checkout on disk**.

| Service | RPCs | Why |
|---|---:|---|
| ProjectService | 23 | repos, git info, init, clone |
| WorktreeService | 22 | git worktrees, staging, commits, PRs |
| RepoService | 5 | repo discovery |
| PackageCommandsService | 8 | run the project's own build/test/dev commands |
| `BranchChat` / `ListBranches` | 2 | the worktree half; thread-fork half stays |
| Settings: skills, shortcuts, prefs, onboarding, privacy | ~25 | IDE config |
| MCPService | 10 | server *management* is product config (see below) |
| ConnectorService | 8 | third-party client authorization |
| AccountService | 2 | account deletion |

Everything here is git, a working copy, or IDE preferences. That is the whole
coding product at the API layer.

Three notes on the edges:

**`FileSystemService`, `TerminalService`, `BackgroundService` are not coding.**
An embedder building any agent product wants a file tree, a terminal and
background processes — those are runtime capabilities, not code-editing ones.
They collapse into `RuntimeService.Invoke` as engine surface, with thin
product-shaped wrappers if a UI prefers them. Only `PackageCommandsService` is
genuinely coding, because "the project's commands" presumes a project.

**`SettingsService` splits rather than moves.** `UpdateProviderAPIKey` /
`ValidateProviderAPIKey` / `GetProviderStatuses` are LLM gateway concerns and
belong to the engine; the other ~29 are IDE config.

**`MCPService` splits too.** Managing *which servers a project runs* is product
config. But an embedder wants MCP servers in their own product, so the
attach-a-server-to-a-runtime capability is engine; the recommendation list,
install UX and scope-moving are coding.

---

## Counts

| | Services | Approx. RPCs |
|---|---:|---:|
| Engine | ~15 | ~165 |
| Coding | ~8 | ~85 |

The engine is the **larger** half, which is the correction this draft makes.
Chat, sessions, search, archive, unread, queueing, interrupt, the file tree, the
terminal, background processes — an embedder needs all of it, and none of it is
about code.

What actually leaves is git and a working copy.

---

## Sequencing

1. **`RunService`**, with handlers delegating to the existing implementation.
   Additive: the headless verb starts existing, nothing changes for anyone.
2. **Extract the coding RPCs** — `project_id` optional on session create,
   `worktree_id` out of the engine message, `BranchChat` split into
   `ForkThread` (engine) + worktree creation (coding). This is the real work
   and it is small.
3. **Rename `ChatService` → `SessionService`**, daemon → runtime, reserving old
   tags. Mechanical, and best done once the shape is settled.
4. **`RuntimeService.Invoke`**, with FileSystem/Terminal/Background over it.
5. **`TriggerService`**, after runs can exist without a session.

Step 1 is additive and reversible. Step 2 is the actual split and is far
narrower than the RPC counts suggested, because most of what looked like "chat
product" is engine surface with a misleading name.

The SDK question runs alongside: the embeddable frontend (workflow editor +
timeline) is a consumer of exactly the engine services above, which makes it a
good forcing function. If the SDK needs something the engine API cannot supply,
that is a missing engine RPC rather than a reason to reach into coding.

---

## Open questions

1. **One proto package or two?** `reliant.engine.v1` and `reliant.coding.v1` make
   the boundary enforceable (a lint rule can forbid engine importing coding).
   The cost is a large, mechanical rename.
2. **Does `RunService` need `GetRunEvents` as well as a stream?** A polling API
   is friendlier for scripts; the stream is what the UI uses.
3. **Where does workflow storage live?** Definitions are engine artifacts, but
   today they are per-project rows. A tenant-scoped store is implied by the
   run-container work.
4. **Do the four daemon-proxy services stay as coding wrappers, or get deleted?**
   Deleting is cleaner; keeping them avoids rewriting a lot of frontend at once.
