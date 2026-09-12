# Coding-specific concepts baked into the generic workflow API

Research-only inventory. Branch `engine`. Module `github.com/reliant-labs/reliant`.
Every citation is FILE:LINE at time of writing.

Depth ratings:
- **cosmetic** — rename / alias, no wire or data change
- **schema** — proto tag or Go type change; needs reserve + regen, no data touched
- **migration** — requires a DB migration and/or backfill of existing rows

---

## Ranked leak table

| # | Concept | Where | Depth | Removal cost |
|---|---|---|---|---|
| 1 | **Thread is structurally required for every run** | `internal/db/migrations/postgres/20260211000002_init_schema.sql:94` (`workflows.thread TEXT NOT NULL`); `runtime_context.go:11` | **migration** | Highest. Every workflow row must name a thread. A trigger-driven run with no conversation cannot be represented. |
| 2 | **`chat_id` is the run's tenancy + identity root** | `init_schema.sql:92` (`workflows.chat_id TEXT NOT NULL`); `runtime_context.go:10`; identity re-derived from the chat row at `call_llm.go:198-205` | **migration** | Highest. Chat is where user, project and repo are all read from. Needs a generic `run_context` / owner concept. |
| 3 | **`create_worktree` as a first-class node type** | proto `workflow_v2.proto:277` (tag 15), `:621-648`; activity `runtime/activities/handlers/worktree.go:45-260` | **schema** | Moderate. Only ONE builtin uses it (`builtin/parallel-compete.yaml:149`). Reserve tag 15. |
| 4 | **`project` / `ProjectPath` in sub-workflow args + runtime** | proto `:207-211` (`ProjectConfig`), `:714-718` (`SubWorkflowArgs.project` tag 5); `runtime_context.go:68-69` | **schema** | Moderate. Mostly pass-through; rename to `working_directory` / `workspace`. |
| 5 | **`daemon:` selector on Workflow + Node** | proto `:103-120`, `Node.daemon` tag 7 (`:262`), `Workflow.daemon` tag 12 (`:1375`); `runtime_context.go:71-73, 82-88` | **cosmetic→schema** | Low–moderate. The *shape* is generic (executor selector); the *name* is not. |
| 6 | **`save_message` on EVERY node + as a node type** | `Node.save_message` tag 6 (`:259`); `SaveMessageConfig` `:130-140`; `save_message_node` tag 14 | **schema** | Moderate. Conversation vocabulary in the universal node header. |
| 7 | **`transition_to` (chat hands off to another workflow)** | proto `:1385` (tag 14); impl `handlers/graduate.go:43-110` | **schema** | Low. Self-contained; genuinely a *session* concept, not coding. |
| 8 | **`resume_node`** | proto `:1381` (tag 13); `runtime/workflow.go:800, 4014`; `validation/structural.go:227` | **cosmetic** | Low. Generic resumption, but keyed per-chat. |
| 9 | **`skills` on CallLLMArgs** | proto `:388-401` (tag 14) | **schema** | Low. Agent/coding-persona concept. |
| 10 | **`WorkflowContext.Branch` / `WorktreePath` in the CEL env** | `internal/workflow/model/context.go:12-21` | **cosmetic** | Low. Two git fields on the `workflow` CEL root. |
| 11 | **`presets` (agent personas)** | proto `:213-219`, `:1367`, `:711-713`, `:950`; `internal/preset/preset.go`; table `init_schema.sql:248-260` | **cosmetic** | Low. Actually generic (tagged param bundles). |
| 12 | **`RunArgs.work_dir` "defaults to project root"** | proto `:678-682` (tag 3) | **cosmetic** | Comment-only. |
| 13 | **No trigger / webhook / cron concept exists** | (absence) | **schema** | This is the *gap*, not a leak. Every run today starts from a chat. |

---

## Per-question detail

### 1. `create_worktree`

`CreateWorktreeArgs` is oneof arm tag **15** (`workflow_v2.proto:277`), declared
`category: "git"`, `icon: "GitBranch"` (`:626-633`), with fields `name`(1),
`branch`(2), `base_branch`(3), `copy_files`(4), `force`(5).

The activity (`handlers/worktree.go:86-260`) does far more than git: it reads the
chat (`:99`), refuses if `chat.ProjectID == ""` (`:104`), loads the project,
enumerates nested repos via `ListReposByProject`, self-heals through
`repopkg.AdoptFromDaemon`, fans out one daemon `worktree.create` RPC per repo
under a shared workspace UUID, persists a `Worktree` row, and rolls back on
partial failure. It hard-requires a daemon router (`:87-88`).

**Usage: exactly one builtin** — `builtin/parallel-compete.yaml:149`.

It could absolutely be a tool instead: it is already implemented as daemon RPC
calls plus DB writes, which is the definition of a tool. The obstacles to removal
are (a) it returns structured outputs consumed via `nodes.<id>.{id,name,path,
branch,base_branch,repo_id,status}` (`:623-624`), and (b) `analysis/analyzer.go:541`
groups it with `Join`/`SaveMessage`/`Compact` as a non-LLM node for graph analysis.
Removing the arm breaks that one YAML, the analyzer case, and the CEL registry
name mapping (`cel/registry.go:116`). Reserve tag 15.

### 2. The `daemon:` selector

`DaemonSelectorProto` (`:103-112`) = `id`, `name`, `type` (`"local"|"cloud"|"any"`),
`labels map<string,string>`. `CelDaemonSelector` (`:115-120`) wraps it as either a
literal or a CEL `expr` string. Mirrored in Go at `runtime_context.go:82-88`.

The *shape* is already a fully generic executor-selection predicate — id, name,
class, label match. Nothing in it is coding-specific. What is coding-specific is
the **name**: "daemon" means "the reliant agent process with filesystem access."
A general engine would call this `runtime`, `executor` or `environment`. This is
the cheapest high-visibility win: rename the message and the two fields
(`Node.daemon` tag 7, `Workflow.daemon` tag 12), keep the semantics identical.
Note `worktree.go:87` treats the absence of a daemon router as a hard error, so
"no executor" is not currently a valid state for some node types.

### 3. `project` and `working_directory`

- **Node args:** `SubWorkflowArgs.project` tag 5, type `ProjectConfig`
  (`:714-718`), whose sole field is `path` (`:210`) — "Empty/nil means inherit
  from parent workflow." `RunArgs.work_dir` tag 3 (`:679`) — "Defaults to project
  root."
- **CEL environment:** not a `project` root, but `model.WorkflowContext`
  (`model/context.go:12-21`) exposes `Path`, `Branch`, `WorktreePath` under the
  `workflow` root.
- **Activity inputs:** `schema/activity_inputs.go` is purely a reflection-based
  type registry (260 lines) — it contains **no** project concept at all. Clean.
- **ExecContext:** there is no type named `ExecContext`. The real per-activity
  context is `types.ActivityInput` (`activities/types/activity_input.go:16-19`) =
  `{Runtime RuntimeContext, Node *reliantv1.Node}`, and `RuntimeContext`
  (`types/runtime_context.go:8-80`) carries `ProjectPath string` at `:68-69`.

**Verdict: `project` is mostly pass-through, not structural.** `ProjectPath` is a
string the runtime forwards; nothing in the graph executor branches on it. The
structural dependency is not `project` — it is `ChatID`, from which the worktree
activity and `call_llm` *look up* the project. Renaming `ProjectConfig.path` →
`working_directory` is a cosmetic change; severing chat→project is not.

### 4. The CEL environment's roots

`internal/workflow/cel/types.go:31-61` defines exactly six:

| Root | Type | Contents |
|---|---|---|
| `inputs` | dynamic `map[string]any` | user-declared workflow inputs |
| `nodes` | dynamic | previous node outputs, `nodes.<id>.<field>` |
| `workflow` | **typed** `model.WorkflowContext` | see below |
| `iter` | typed `model.IterContext` | `iteration`, `index`, `item`, `key` (`types.go:7-22`) |
| `output` | dynamic | current activity output, for `save_message` |
| `outputs` | dynamic | sub-workflow outputs, for loop `while` |

**The only coding-specific root is `workflow`.** Its full field list
(`model/context.go:12-21`): `id`, `name`, `path`, `branch`, `mode`, `run_id`,
`session_id`, `worktree_path`. Of these, `branch` and `worktree_path` are git;
`path` is filesystem. It does **not** expose chat or project or repo fields —
better than expected. `iter` (`types.go:7-22`) is entirely generic.

So an expression can reach: any input, any prior node output, those 8 workflow
fields, 4 iter fields, and the two dynamic output maps. Trimming `branch` and
`worktree_path` is cosmetic — no wire format, just a struct with json tags.

### 5. `transition_to` and `resume_node`

- **`transition_to`** (tag 14, `:1379-1385`) → `TransitionChatOnCompletion`
  (`handlers/graduate.go:43-78`) and `loadTransitionTarget` (`:84-110`). It loads
  the completed root workflow's definition, reads `GetTransitionTo()`, then
  **mutates the chat row's `workflow_name`** — only if the chat's *currently
  active* workflow is still the one that completed (idempotency guard, `:59-60`).
  `EmitTransitionMessage` (`:117+`) posts a system message via
  `SaveMessageToThread`. Validated at `validation/structural.go:240` and
  `validation/cross_workflow.go:35`.

  This is a **session** concept, not a coding one — "when this pipeline finishes,
  the ongoing session hands off to that workflow." A general engine would keep it
  but rename chat→session. The implementation is small and self-contained.

- **`resume_node`** (tag 13, `:1376-1381`) is genuinely generic — "which node to
  re-enter when a run resumes an interrupted predecessor." Referenced at
  `runtime/workflow.go:800, 4014`, `yaml/parser.go:103, 438`,
  `validation/structural.go:227`. The only leak is the doc-comment phrase "for the
  same chat" — resumption is keyed by chat. Cosmetic once chat is generalized.

### 6. `save_message` / thread coupling — the deepest leak

Three separate couplings:

1. `Node.save_message` tag **6** — on the universal node header (`:259`),
   `SaveMessageConfig` at `:130-140` (`condition`, `role`, `content`,
   `tool_calls`, `tool_results`).
2. `save_message_node` — oneof arm tag 14, its own node type.
3. `SubWorkflowArgs.thread` tag 6 → `ThreadConfig` (`:149-161`): `mode`
   (`inherit|new|fork`), `memo`, `inject` → `InjectConfig` (`:164-169`).

**Is a thread structurally required? Yes — at the schema level.**
`workflows.thread TEXT NOT NULL` (`init_schema.sql:94`). Every workflow execution
row must name a thread; there is no nullable path. This is the single hardest
thing on this list to unwind and the one that most directly blocks a
cron-triggered pipeline.

**Can an individual node execute without a thread?** It depends on the node:
- `call_llm` **hard-fails**: `return nil, fmt.Errorf("thread is required")`
  (`handlers/call_llm.go:192`).
- `compact` hard-fails identically (`handlers/compact.go:112`).
- `drain_agent_messages` hard-fails (`:70`).
- Structural nodes (`join`, `router`, `loop`) and `run` do not read the thread.

So the graph executor tolerates a threadless node, but the *row* cannot be written
without one, and the two most-used node types refuse. For a cron data pipeline
today you would have to synthesize a dummy chat + thread. Interestingly the
runtime already has partial vocabulary for threadlessness —
`runtime/registry.go:1241` and `loop_executor.go:116` both comment that `""` is
"a real answer, not a placeholder" for an error carrying no thread — and
`CallLLMArgs.messages` tag 10 exists specifically for "ad-hoc LLM calls without a
thread" (`:377`). That is the seam to widen.

### 7. Presets

A preset (`internal/preset/preset.go:37-70`) is a **reusable bundle of workflow
parameter values**: `{name, description, tag, params map[string]any, source}`.
Matching rule: the preset's `tag` must equal the workflow's or a group's tag, AND
every param must exist in the target's inputs (partial presets allowed, unknown
params rejected).

Where they live — three places:
- **Files:** `.reliant/presets/*.yaml` (project) and embedded builtins.
- **DB:** `presets` table (`init_schema.sql:248-260`): `id, user_id, project_id,
  name, slug, description, tag, params, timestamps`, unique on
  `(user_id, project_id, slug)`. Plus `selected_presets TEXT` on the workflows-
  adjacent table (`:85`) and `project_presets_json` on `project_configs`
  (`20260303100000_add_project_content_columns.sql:3`).
- **Proto surface:** `PresetsConfig` (`:213-219`), `Workflow.presets` tag 8,
  `SubWorkflowArgs.presets` tag 4, `PresetInputConfig` (`:1101-1106`) as Input
  oneof arm tag 23, `GroupInputConfig.presets` (`:1096`).

**Presets are generic**, not a coding concept — "tagged, validated partial param
bundles" applies to any parameterized workflow. The coding flavor is only in the
default tag being `"agent"` (`:215`) and in `Preset.Params["skills"]` carrying
recommended skills (`preset.go:63-69`). Leave the mechanism; it is a genuine
general-purpose feature.

### 8. Node args that leak coding (with tag numbers)

**`CallLLMArgs`** (`:309-403`) — `category: "agentic"`:
| Tag | Field | Leak |
|---|---|---|
| 14 | `skills` `CelStringList` | **Yes** — namespaced skill paths (`"forge/db"`, `"code-review"`), seeded as fake tool-interactions. Agent/coding persona concept. |
| 12 | `tools_config` → `ToolsConfig` (`:292-302`: `filter`(1), `spawn`(2), `permission`(3)) | Borderline. `spawn` entries are `spawn:workflow(presets)` specs; `permission` is `readonly|mutating`. Generic-ish. |
| 13 | `compaction_threshold` | Generic (LLM context management). |
| 1-5, 9, 10 | model, temperature, max_tokens, thinking_level, system_prompt, response_tool, messages | Generic LLM. |
| — | reserved 6, 7, 8, 11 | already reserved (superseded by `tools_config`). |

**`ExecuteToolsArgs`** — generic tool dispatch; the *tools* are coding tools but
the node is not.

**`RunArgs`** (`:658-688`): `command`(1), `env`(2), `work_dir`(3), `log_file`(4).
Only leak is the doc comment on tag 3, "Defaults to project root." Otherwise a
clean generic shell-exec node. Note `is_structural: true`.

**`SubWorkflowArgs`** (`:695-725`):
| Tag | Field | Leak |
|---|---|---|
| 5 | `project` → `ProjectConfig` | **Yes** — working-directory override named after a coding concept. |
| 6 | `thread` → `ThreadConfig` | **Yes** — conversation concept. |
| 4 | `presets` | Generic. |
| 1,2,3,7 | ref, inline, args, passthrough | Clean and generic. |
| — | `display_name: "Agent"`, `category: "agentic"`, `icon: "Bot"` (`:697-703`) | Cosmetic metadata leak — the generic sub-workflow node is *presented* as an agent. |

**`CreateWorktreeArgs`** — entirely a leak, see Q1. Tags 1-5, arm tag 15.

Tags available to reserve on removal: `Node.args` **15**; `SubWorkflowArgs` **5**
and **6**; `CallLLMArgs` **14**; `Workflow` **14** (`transition_to`).

### 9. Already generic — do NOT touch

These are cleanly general-purpose. PRs should route around them:

- **The graph model itself.** `Workflow.{name, nodes, edges, description, inputs,
  outputs, entry, api_version}` (tags 1-6, 9, 10). `Edge`, `Node.{id, type,
  condition, timeout, outcome}` (tags 1, 2, 3, 5, 8). `Node.outcome`'s
  declared-never-inferred design (`:240-257`) is well reasoned.
- **Flow control:** `LoopArgs` (tag 22) sequential + parallel, `JoinArgs` (23),
  `RouterArgs` (24). Pure orchestration.
- **The CEL layer.** `wfcel` namespaces, `EdgeEvalContext`, `CELEvalContext`
  interface, `iterContextActivationValue` (`cel/types.go:7-22`) — all generic.
  `model.IterContext` is clean.
- **`schema/activity_inputs.go`** (all 260 lines). A reflection/protoreflect-based
  activity type registry with zero domain knowledge. Model for the rest.
- **`types.ActivityInput`** envelope shape (`activity_input.go:16-19`) — `{Runtime,
  Node}` with protojson oneof handling. The envelope is right; only
  `RuntimeContext`'s *fields* need work.
- **The Input type system** (`:931-952`) — the 14-arm oneof (string, number,
  integer, boolean, enum, model, message, attachments, tools, array, object, any,
  group, preset). `model`/`tools`/`attachments`/`preset` are AI-shaped but not
  *coding*-shaped; keep.
- **The preset mechanism** (Q7).
- **`RunArgs`** minus the one doc comment — this is the closest thing to an
  n8n-style generic action node that already exists, and is the natural template
  for whatever replaces `create_worktree`.
- **Validation and YAML round-trip** (`validation/structural.go`,
  `yaml/parser.go`) — mechanical, follows the proto.
- **Temporal determinism boundary**, `NodePath`, loop-scoped identity
  (`runtime_context.go:41-57`) — carefully designed, orthogonal to this work.

### The missing piece

There is **no trigger, webhook, or cron concept anywhere** in the proto or
runtime. Every run originates from a chat: `workflows.chat_id NOT NULL`
(`init_schema.sql:92`) with `thread NOT NULL` (`:94`), and six generic tables
(threads, messages, tool_calls, agent_messages, user_updates,
background_processes) carry real FKs into `chats`. Adding triggers is therefore
not an additive feature — it requires first making chat/thread optional on a run,
which is leak #1 and #2 and the natural first PR in the series.
