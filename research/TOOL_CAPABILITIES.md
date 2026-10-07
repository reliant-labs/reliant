# Tool capabilities: one resolver, enforced where tools run, built from durable inputs

Status: AS BUILT (stream C, branch `feat/tool-capabilities`). This replaces the
brief that opened the work; §1–§2 are kept as the record of what was wrong,
§3 is the design that shipped, §4 answers every open question the brief left.

Related:
- research/TOOL_GATING.md, research/PERMISSIONS_ASBUILT.md and
  research/PERMISSIONS_REDESIGN.md: earlier analysis. #274 and #264 fixed the
  acquisition half.
- research/DAEMONLESS_RUNS.md and research/NO_MACHINE_CHATS.md: no-machine is
  one input to the resolver.

## 1. How tools were restricted before this change (main f7f5c57f)

| Layer | Source | Menu (`call_llm`) | `load_tool` | Execution (`execute_tools`) | State lived in |
|---|---|---|---|---|---|
| No machine | `chats.no_machine` row | ✅ `withoutMachineTools` | ✅ narrowed access | ✅ `execute_tools.go:582` | chat row (durable) |
| Workflow `tools:` (preloaded) / `loadable_tools:` | workflow node | ✅ `ResolveToolAccess` | ✅ `CanLoadTool` | ❌ deliberately absent | `LoadedToolsStore.access`, in memory |
| Permission tier | workflow `permission` | ❌ (not applied to the menu) | ✅ | ✅ | `LoadedToolsStore.permissions`, in memory |
| load_tool grants | load_tool calls | ✅ intersected with CanLoadTool | — | — | `LoadedToolsStore.tools`, in memory |
| Spawn preset list | tool-call input envelope | — | — | ❌ dead: spawn never reaches the activity | Temporal history |
| Skills for the `skill` tool | project config | ✅ | — | ✅ | `LoadedToolsStore.skills`, in memory |
| MCP discovery list | daemon | ✅ | ✅ verify + search | — | `LoadedToolsStore.availableMCP`, in memory |

`LoadedToolsStore` was a process-global map keyed by `Scope(chatID, thread)`,
while every activity runs on the shared, non-sticky Temporal task queue.

## 2. The defects

1. **Declared tools were only steering.** A model calling a registry tool it
   was not offered (named from history, or hallucinated) still ran it. The
   earlier execution check had been removed because it enforced the preloaded
   bundle only, which refused legitimately loaded tools.
2. **Per-scope state was in memory on a shared queue.** A restart (every
   deploy) or a second worker replica emptied it between call_llm and
   execute_tools: CanLoadTool failed OPEN, GetPermission fell back to
   `mutating` (so an orchestrator's start_run/activate_trigger was refused),
   loaded tools vanished, and the `skill` tool lost the project's skills.
3. `LoadToolMetadata.LoadedTools` was emitted and never consumed.
4. Found while building this (not in the brief):
   - **Spawn preset validation was dead code.** spawn calls are split out and
     dispatched workflow-side (`executeToolsWithSpawnSupport`), so the
     activity's `AvailablePresets` check at `execute_tools.go:346` never saw
     one. A model could spawn any preset that exists.
   - **The tier never gated the menu**: a `mutating` node whose
     `preloaded_tools` named `start_run` was handed it and refused at
     execution, contradicting the ToolsConfig.permission proto comment.
   - **`spawn` was never tier-checked anywhere it mattered**, so
     `MinimumPermissionForTool("spawn") == orchestrator` was a dead letter:
     the builtin agent runs at `mutating` and spawns. See §3.6.

## 3. As built

**One capability set per agent turn, computed by one resolver from durable
inputs, recorded in Temporal history by call_llm, threaded by the workflow to
the execute_tools that runs that turn's calls, and checked at the menu, at
load_tool, and at execution. There is no in-memory per-scope state left.**

### 3.1 The resolver — `internal/llm/tools/capabilities.go`

`ResolveCapabilities(CapabilityInputs) *Capabilities` is pure: no I/O, no
globals beyond the static registry and integration catalog.

Inputs (`CapabilityInputs`):

| Input | Where call_llm gets it | Durable because |
|---|---|---|
| `Access` (preloaded + loadable, expanded) | node `tools_config`, `ResolveToolAccess`, then `withoutMachineTools` for a no-machine chat (stream A's `request_machine` preload lands here; §4b) | it is the node declaration |
| `Permission` | node `permission`, capped to `rtx.ParentPermission` | node + RuntimeContext |
| `NoMachine` | `chats.no_machine` | chat row |
| `Grants` | `rtx.ToolGrants` | workflow state (§3.4) |
| `MCPTools` | connected MCP names, only when the node's filters can reach MCP | re-discovered every turn |
| `MailboxReachable`, `CanSpawnChildren`, `SpawnPresets` | spawn depth + node `spawn:` | node + RuntimeContext |
| `OwnChildren`, `InheritedChildren` | `spawnHistory` (#500): `ListSpawnChildren` (a spawn row that started a child) and `ListInheritedSpawnChildren` (a branch's ancestors' spawns before the fork point). Best-effort: a read error withholds the grants for that turn only (§4b) | spawn tool_calls rows, read every turn |
| `ResponseTool` | node `response_tool` | node |
| `UsableIntegrations` (integration id → bool) | stream B's `usableIntegrations` (handlers/integration_access.go): which connection-gated integrations the declaration reaches the run's owner can use. Fails closed (§4b) | asked of the credential source every turn |
| `Unattended` | `rtx.Unattended`, which the StepExecutor sets on every call_llm from `runtime.IsUnattended(workflowInputs)` (§3.9) | a workflow input: injected by the launcher, carried by continue-as-new, propagated to every sub-workflow, loop body and spawn |

Rules. Every run-level rule lives in ONE classifier, `exclusion(name)`, which
the menu, load_tool, its advertised list (`Deferred`), its search
(`Searchable`) and execution-time refusals (`Explain`) all consult, so they
cannot disagree:

| Exclusion | When |
|---|---|
| unknown | neither a registry tool nor an MCP tool |
| not connected | an MCP tool that is not connected |
| needs machine | needs the user's machine (`NeedsMachine`, every MCP tool) on a no-machine run |
| has machine | exists only for a run without one (`OnlyWithoutMachine` — `request_machine`) on a run that has one |
| unusable integration | a connection-gated integration the owner cannot use (`WithheldIntegrations`) |
| unattended | nobody is attending the run, the tool is one `UnattendedWithholding` names, and the declaration did not name it (§3.9) |
| tier | above the run's tier |

- A name is **reachable** when no exclusion applies.
- **Offered** = reachable preloaded names ∪ reachable grants the declaration
  may load ∪ the structural tools ∪ the response tool. The structural tools:
  - `load_tool` when the declaration names any loadable reach;
  - `spawn_status` when the thread has sub-agents, its own or inherited;
  - `spawn_send` when there is a counterpart (a parent) or own sub-agents;
  - `spawn_stop` when this agent may spawn or has own sub-agents;
  - `spawn` when the node declares spawn presets.

  A branch's inherited sub-agents grant `spawn_status` alone: the original
  conversation owns them.
- **CanLoad(name)** = offered, or reachable and declared loadable — and never
  a no-machine-only tool, which is handed over directly rather than loaded.
- **Explain(name)** derives the refusal reason from the set alone (no
  machine, has a machine, unknown, not connected, unusable integration with
  its reason, unattended with its category's reason, tier, "load it first",
  "not declared").

Output (`Capabilities`, proto `reliant.v1.ToolCapabilities`): `offered`,
`loadable_all`, `loadable`, `permission`, `no_machine`, `mcp_tools`,
`spawn_presets`, `withheld_integrations` (integration id → reason, ≤ one entry
per integration), `bound_params` (§3.8), `unattended` and `unattended_opt_in`
(§3.9). Names only, sorted — except a bound parameter's value when the
workflow bound it, which is already in history as call_llm's own input.

### 3.2 call_llm — builds the menu FROM the set and records it

`getAvailableTools` resolves the set, instantiates exactly `Offered` (registry
factories, connected MCP tools, the structural tools), call_llm appends the
spawn and response tools, applies bindings, and then **re-records `Offered`
as the names actually in the request's tool array** — so "offered" means
literally what the model was sent, even if a factory declined to build a tool.
From the same array it records `bound_params`: what the bound instances hid
from the schema (§3.8).
The set goes out as `CallLLMOutput.capabilities` (field 19) and so into
history. load_tool's "Additional tools available" list is `caps.Deferred()`.

### 3.3 The runtime threads it to execute_tools

`StepExecutor.startAction` (execute_tools) finds the call_llm that produced the
batch — the node `tool_calls` names (`nodes.X.tool_calls`), else whichever
call_llm output in scope carries those tool-call ids — and copies its set onto
`ExecuteToolsArgs.capabilities` (field 6), the same way it already enriches the
node with that call_llm's response-tool info. A recorded set always carries a
tier, so an output whose `capabilities.permission` is empty was never resolved
(schema normalization fills absent sub-messages with zero values).

`executeToolsWithSpawnSupport` applies the set to the two tool families that
run workflow-side: a `spawn` that was not offered or names a preset outside
`spawn_presets`, and an `ask_user` that was not offered, are moved into the
regular ExecuteTools batch instead of being dispatched, so the activity refuses
them like any other call (one refusal path, one FAILED row).

### 3.4 execute_tools — one check, then load_tool reads the same set

For each call, in order:
1. **not in `offered` → refused** (`capabilityRefusal`): a tool_result error
   carrying `Explain(name)` — the tier, no machine, withheld, "load it first
   with load_tool", "not declared", or "does not exist" — recorded FAILED
   with that text as the row's `error_message`. A `spawn` that reached the
   activity is always refused here (§3.3), with the preset reason. With no
   recorded set the only check is the base tier.
2. the chat-row no-machine refusal, unchanged. It is redundant on any turn
   that recorded a set (machine tools are never offered there) and is kept on
   purpose: it reads the durable row on every path, and it is the boundary a
   no-machine run actually depends on.
3. otherwise it runs. `load_tool` and `skill` read what they need from the
   context: `tools.WithCapabilities(ctx, caps)`; the project's skills are read
   from the project config row when the call is a `skill` call
   (`tools.WithSkills`), replacing the store.

load_tool grants nothing to a store: it returns `loaded_tools` metadata, and
execute_tools unions every load_tool result's metadata into
`ExecuteToolsOutput.granted_tools` (field 6).

### 3.5 Grants live in workflow state

`ChildWorkflowTracker.toolGrants` (thread → names) is the execution-wide,
per-thread record. `handleActivityCompletion` merges an ExecuteTools result's
`granted_tools` into the thread's entry (also through the workflow-assembled
spawn/ask_user path, which now carries the field). `buildRuntimeContext` hands
the thread's grants to every call_llm as `rtx.ToolGrants`. Continue-as-new
carries the map as `ResumeInput.ToolGrants`; relaunched spawns keep their
thread and therefore their grants. Updated only from recorded activity
results, so replay re-derives it exactly.

**Durably, each grant is on its load_tool result's row.** execute_tools
writes what a result granted (`grantedByResult`, the same rule that feeds
`granted_tools`) to `tool_call_results.granted_tools`, in the transaction that
writes the result content the model reads. That row is what survives an
execution's death:
- **The coarse fresh restart** (history-limit death, ghost, a reset that
  cannot replay) starts a new execution with nothing of the old one's memory.
  `ResumeInputFromDurableState` — the one builder SendMessage and ghost
  recovery use, beside the checkpoint and `ResumableSpawnsFromDurableState` —
  rebuilds `ResumeInput.ToolGrants` per thread from the chat's result rows
  (`ToolGrantsFromDurableState`), and `DynamicWorkflow` seeds them as it seeds
  a continue-as-new's. A relaunched sub-agent gets its own thread's grants.
- **A retried ExecuteTools** answers a call that already finished from its
  row (`checkPriorTerminalResult`) instead of running it again; the replayed
  result now carries the recorded grants, so the retry reports them to the
  workflow rather than dropping them.

Why the result rows and not the position checkpoint: a grant is a fact about
one load_tool call, written once with the result that announced it, so the
restarted menu agrees with the history the model resumes from by
construction. The checkpoint is a snapshot written at the root's node-entry
and loop-iteration boundaries only. It would lag every grant made since the
last boundary, miss every sub-agent's grants while the root is parked waiting
on them (the fan-out that tends to precede a history-limit death), and with
concurrent writers it would race. No workflow command changed: the rebuild
runs in the API server before the new execution starts, so no
`workflow.GetVersion` is involved.

The rows are per chat, not per run: the rebuild includes a grant from an
earlier run of the chat that completed, which an in-memory record would have
dropped at that run's fresh start. Nothing durable marks a fresh start, and
the grant cannot widen anything — the resolver offers a grant only where the
run's own declaration could load it, under its tier and unattended rule —
while the model's history already shows that load.

### 3.6 Tiers and spawn

The tier is part of the set (§4.5), and the resolver now applies it to the
menu. `spawn` is granted by the node's `spawn:` declaration (and depth), NOT
by the tier: it never was tier-checked in practice, and the builtin agent
spawns at `mutating`. `MinimumPermissionForTool` no longer lists `spawn`;
`agent`, `start_run`, `control_run`, `send_to_run` and `activate_trigger`
stay orchestrator-only.

### 3.7 Removed

`LoadedToolsStore` (all of it: grants, access, tier, skills, MCP list),
`DeferredToolNames`, `cleanup.go`'s Clear, the `AvailablePresets` /
`SpawnWorkflow` tool-call fields and the call_llm envelope stamping (the
decoder still unwraps envelopes in in-flight histories), and
`ExpandToolFilterWithSpawn` from call_llm (its `spawn:` configs parsed out of
`preloaded_tools` were never used).

**Why not persist the store in Postgres instead:** it would be a second source
of truth beside workflow history, with a DB read on every tool call and a
cleanup story. The call_llm → execute_tools edge is exactly where this data
already flows, and history is already the durable per-turn record.

**Not a goal:** making a machine-attached chat a security boundary. The shell is
granted at every tier, so this stays steering there. For no-machine runs it is
a real boundary, because the server has no route to the machine.

### 3.8 Bound parameters take effect at execution

A bound parameter (`tools_config.tools` in the workflow, or the run owner's
`tool.bindings.<tool>` setting — `internal/toolbindings`) is removed from the
schema the model is offered. Until this change, that was the only thing it
did. Only the tool instance call_llm built to produce the schema was bound,
and execute_tools ran every call on a fresh tool from the factory (on the
worker, or on the daemon), so:
- the bound value never reached the tool;
- a model that sent the hidden parameter anyway had its own value honored.

A workflow that pinned `http__request`'s `url` pinned nothing.

**The flow now.**
1. **call_llm records what it hid.** `toolbindings.Recorded` reads the final
   tool array: each configured parameter whose binding took effect on its
   bound instance (`BindableTool.Bindings()`). The result goes into the set as
   `bound_params` (tool → parameter → `BoundParam`).
   - Not recorded: a binding that failed, because Apply left that parameter
     open; and a tool's own declared default, because the executor's fresh
     tool declares and applies the same default itself.
   - MCP tools and the schema-only spawn/ask_user tools cannot be bound
     (`ErrBindingsUnsupported`), so nothing for them is ever recorded.
2. **The runtime carries it.** The set travels into execute_tools unchanged
   (§3.3).
3. **execute_tools merges before dispatch.** `boundToolInput` in
   handlers/tool_bindings.go runs `tools.ApplyBindings` on the model's input.
   The result goes only into the `ToolRequest`, so every executor receives a
   complete input:
   - `RemoteExecutor.executeOnServer` → `LocalToolExecutor`;
   - `executeOnDaemon` → the daemon's own `LocalToolExecutor`.

   Each builds an unbound tool whose full schema includes the bound
   parameters. **Nothing is asked of the daemon, so a daemon of any version
   applies bindings.** The tool_calls row and the transcript keep the
   model's own input.

**Carried, or re-resolved at execution?** Both, split by where the value
already lives:
- **Workflow-scope values are carried in the set.** They are part of the
  evaluated node, which is already in history as call_llm's activity input.
  Carrying them again exposes nothing new. Re-resolving them would need
  call_llm's node at execute_tools, which it is never handed — the
  second-resolution-path problem §4.1 already rejected.
- **A global setting's parameter is recorded by name only**
  (`BoundParam.global`). execute_tools re-reads the setting
  (`toolbindings.LoadGlobal`, one query, only when the call's tool has such a
  parameter). A binding can carry a secret: the natural place to put an
  `Authorization` header is `http__request`'s `headers`. Settings are not
  otherwise in workflow history, which is retained, shown in the Temporal UI,
  and checked into `replaytest/fixtures`.

  If the re-read setting no longer binds a recorded parameter, or cannot be
  read, the call is refused rather than run open. The model was not shown the
  parameter, so its call has no value there. A changed value is applied: it
  is still the owner's pin. Sealing values with the vault instead was
  rejected: it adds a vault dependency to both activities for data that is
  plaintext in its own store anyway.

Payload: only bound tools appear, and a turn with no bindings adds nothing.
Workflow values are the same bytes call_llm's input already carries; the
claim-check codec covers anything large.

**A model value for a bound parameter is refused, not overridden.**
`checkBoundKeys` is the one rule for every path:
- a value different from the bound one fails the call with a
  `*BoundParamSetError`, recorded FAILED, naming the parameters and never
  their bound values;
- a model that repeats the bound value (compared as JSON, so 7 and 7.0 match)
  has the redundant key dropped and runs.

The rule is shared by execute_tools (`ApplyBindings`), a bound ToolWrapper's
own `Run`/`RequiresPermission`, and `integrationTool.Run`.

Silent override was the previous documented rule, and it is the wrong one.
The model never saw the parameter, so a value for it is a hallucination or an
injected instruction, and the rest of the call was written around that value:
`body` composed for the host the model named, posted to the one the workflow
pinned. Running it silently executes a call nobody wrote, then reports it to
the model as the call it made. A refusal costs one turn, tells the model
exactly what to drop, and leaves an injected redirect visible as a FAILED row.

**Expression bindings** (`{"expr": ...}`, writable through a setting) still
have no evaluator anywhere. At execution they fail the call, exactly as they
did in a bound tool's own Run.

**Replay and deploy:**
- `bound_params` is a new payload field, and the merge happens inside the
  activity, so no workflow command changes.
- A batch whose call_llm ran before the deploy carries no `bound_params` and
  runs unbound, as before. The next call_llm records them.

**Not tool-parameter bindings:**
- `internal/mcpserver`'s `Bindings` are OAuth client → connector-grant
  bindings (`connectorgrant.ClientBinding`). That server exposes daemon
  commands from its own catalog to third-party MCP clients, never offers a
  schema with parameters bound away, and so does not have this defect.
- `invoke_tool` nodes run a tool with parameters the workflow author wrote,
  with no model and no capability set, so nothing is bound there.

### 3.9 Who is attending: unattended runs are withheld what outlives them

**The problem** (research/WORKFLOW_EDITOR_UX_REVIEW.md §2 Q2). Runs are
started by schedules, webhooks, GitHub, Slack, Gmail, Twilio and other
workflows' runs, and they carry text nobody vetted: an issue body, an email.
The builtin agent preloads by tag and declares `loadable_tools: ["*"]`, so
such a run could load `edit_workflow` and rewrite a workflow that has live
activations. That is a prompt injection that persists itself, because
activations resolve the workflow by name at fire time. It could also post to
Slack or send email under the user's name.

**The input.** `CapabilityInputs.Unattended` is `runtime.IsUnattended` of the
run, which is the definition the rest of the product already uses:
- **The launcher sets it** for every event kind
  `core.TriggerEventKind.Unattended` reports (schedule, webhook, integration,
  workflow_event). `Launch` now forces it from the kind, so a launch path
  that forgets `Spec.Unattended` fails closed.
- **A person's turn is attended, even in an automation's chat.** Each reply
  starts a new root run, and only the launcher writes `unattended` (it is a
  `RuntimeInjectedInput`, refused from clients; #504's `__launch_run` marks
  the same boundary).
- **Propagation is monotone** (`propagateUnattended`). Sub-workflows, loop
  bodies and spawned sub-agents inherit it, and a node can turn it on for a
  phase but never off. The StepExecutor copies it onto every call_llm's
  `RuntimeContext.Unattended`, which is in history as the activity's input.

The set records it as `unattended`. Execution, load_tool and `Explain` need
nothing else.

**What is withheld.** `UnattendedWithholding` (tools/unattended.go) is one
list of categories, each a predicate plus a refusal reason:

| Category | Tools | Reason |
|---|---|---|
| Workflow authoring | `create_workflow`, `edit_workflow`, `write_workflow`, `write_scenario`, `edit_scenario`, `delete_scenario` | A workflow is standing work. A draft is one write away from complete. Scenarios are a workflow's tests, so rewriting or deleting one hides a change. |
| Trigger activation | `activate_trigger` | It creates standing work that keeps starting unattended runs. |
| Other runs | `start_run`, `send_to_run`, `control_run` | start_run's run is attended (`agent.start_run`), so it would get everything withheld here. send_to_run instructs a run that may hold these tools. control_run stops or resumes the user's own work. |
| Mutating integration actions | every exposed action whose manifest says `mutates: true` (`MutatingIntegrationAction`): today GitHub 5, Slack 4, Gmail 1, Twilio 1 and `http__request` | It puts unvetted text in front of other people under the user's name. Derived from the manifest, so a new integration classifies itself. Read-only actions stay. |

Deliberately **not** withheld:
- **Reads:** `list_workflows`, `get_workflow`, `list_scenarios`,
  `view_scenario`, `list_triggers`, `list_runs`, `get_run`,
  `search_integrations`, `get_integration_schema`, `get_schema`.
- **`run_scenario`:** it runs the workflow against mocked activities only
  (scenario/runner), so nothing real executes.
- **`spawn` and the spawn management tools:** the children inherit the
  restriction.
- **Plans, tasks, `worktree`, `metadata_writer` and the file and shell
  tools:** none of them is reliant standing work. The filesystem is §3.7's
  non-boundary: on a run with a machine, the shell can still write
  `.reliant/workflows/*.yaml` (synced as project workflows) or a preset file,
  so there this is steering, like the tier. On a no-machine run it is a real
  boundary.
- **`invoke_tool` nodes:** the author writes the call, so there is no model
  to steer.

`TestUnattendedWithholding_EveryWritingWorkflowOrRunsToolIsClassified` fails
when a non-read-only tool tagged `workflow` or `runs` is added without either
being withheld or being listed as safe.

**The rule.** The exclusion is `unattended` (§3.1): not offered, not loadable,
not advertised (`Deferred`), not searchable, and refused at execution.
- The refusal text (`Explain`, and load_tool's `LoadRefusal`) is the
  category's reason ("unattended runs can't create or change workflows or
  their test scenarios") plus how to opt in.
- execute_tools records it as the FAILED row's `error_message`.
- A load_tool grant from an earlier turn is intersected with the exclusion
  like any other grant.

**The opt-in: name the tool.** A step that names the tool exactly in
`preloaded_tools` or `loadable_tools` keeps it unattended. A tag, a glob or
`"*"` does not count, and a name its own list excludes (`!edit_workflow`)
names nothing.
- `ToolAccess.Named` carries the exact names out of `ResolveToolAccess`.
- The resolver records the withheld ones the step named as
  `unattended_opt_in`, so execution and load_tool agree with the menu.
- The tier still applies: naming `activate_trigger` on a mutating node does
  not reach it.
- A spawned sub-agent's opt-in is its own preset's declaration, by the same
  rule.

Why naming, and not a dedicated flag:
- **What the attack exploits** is the convenience paths (`tag:integration`,
  `tag:workflow`, `"*"`) that sweep a tool in with nobody deciding. A name is
  a reviewable decision that the step does that work. A Slack-triggered
  workflow that posts its answer names `slack__message_post`. A migration
  that creates drafts names `create_workflow` (migrate.yaml already does).
- **A flag would be a second statement that has to agree with the first.**
  A tool named but not flagged would stay withheld, which surprises the
  author. A tool flagged but not named would do nothing.
- **The cost:** an author who named a tool for an interactive chat, and
  later attached a trigger, has opted that trigger in. The name is in the
  YAML, so that review surface already exists.

**Considered and declined, for simplicity.** The UX review proposed three
levels (draft authoring allowed, publish and activate behind an approval card,
all withheld unattended), a per-project policy setting and a per-chat toggle.
The user chose the simpler rule: unattended runs get none of these tools
unless the step names them, and attended chats are unchanged. If the levels
come back, they layer on as more resolver inputs, because the most
restrictive rule wins:
- **A project policy** would be one more input, read from the project config
  row, that can withhold even a named tool.
- **Approval** needs a per-call gate first. None exists:
  `Tool.RequiresPermission` has no caller (PERMISSIONS_ASBUILT.md §Q5), so
  `mutates: true` asks nobody today. The natural seam is workflow-side,
  beside the spawn/ask_user split in `executeToolsWithSpawnSupport`, reusing
  `executeApprovalSignalFlow`'s approval card.

**Replay and deploy.**
- `RuntimeContext.Unattended` and the two set fields are new payload fields,
  and no command changes.
- A batch whose call_llm ran before the deploy runs the legacy policy (§4.1)
  for that one batch. The run's next call_llm resolves with the input.

## 4. Answers to the open questions

1. **Replay safety.**
   - New fields on activity inputs/outputs and on the continue-as-new input
     are replay-safe: Temporal compares command type and order, not payloads.
   - The only command-sequence change is §3.3's re-routing of a refused
     spawn/ask_user into the ExecuteTools batch. It is gated on DATA, not on a
     version: it fires only when the recorded call_llm output carries a set,
     which no pre-deploy history does, so every pre-deploy history replays
     identically. `TestReplayFixtures` replays all six checked-in histories
     unchanged. No `workflow.GetVersion` (project policy:
     replaytest/fixtures/README.md). Residual risk: during a rolling deploy
     with more than one worker replica, an OLD worker could process a
     workflow task whose call_llm output a NEW worker produced and dispatch a
     spawn the new code would refuse; prod runs one replica today.
   - **Runs started before the deploy** (an execute_tools whose upstream
     call_llm recorded no set): that one batch runs the legacy policy — the
     chat-row no-machine refusal still applies, there is no offered check, the
     tier is the base tier (what a restart already produced), and load_tool
     may reach anything the chat row allows. The next call_llm records a set
     and enforcement is complete from then on. "Resolve now from the chat row
     and node" was not possible for the node half: execute_tools is never
     handed the call_llm node's declaration, and resolving it there would be a
     second resolution path beside call_llm's — the drift this design exists
     to remove. The window is at most one batch per in-flight run.
2. **Where grants live:** `ChildWorkflowTracker.toolGrants`, per thread,
   carried across continue-as-new in `ResumeInput.ToolGrants`, and durably on
   each load_tool result's row (`tool_call_results.granted_tools`), from which
   a coarse fresh restart from the position checkpoint rebuilds them (§3.5).
   A reset-and-replay re-derives them from history. A new run starts empty,
   as the old store's run-end Clear did. Rows written before the column
   existed carry no grants, so a restart of a run that loaded tools before the
   deploy rebuilds without them, and the model reloads.
3. **Payload:** names only. A typical builtin agent set is ~30 offered names
   with `loadable_all` (no loadable list) and no MCP list — under 1 KB, carried
   twice per turn (call_llm output, execute_tools input). MCP names travel only
   when the node's filters can reach MCP. MCP **descriptions** do not travel,
   so load_tool's keyword search matches MCP tools by name (which includes the
   server name) rather than by description.
4. **Interactions.**
   - `ExpandToolFilterWithSpawn`: spawn is granted only by `tools_config.spawn`;
     `spawn:` entries inside `preloaded_tools` were parsed and dropped before
     and are ignored now; call_llm no longer calls it.
   - Response tools: appended by call_llm, so they are in `offered`, and
     execute_tools' inline response-tool path runs after the offered check.
   - Bindings change parameters, never names, so they do not touch the
     offered names. The set does carry what they bound (`bound_params`), so
     execution applies the values. That was fixed after this landed (§3.8);
     before it, a binding was hidden from the model and never applied.
   - MCP late binding (`toolexec/mcp_binder.go`): the set records the MCP names
     connected at call_llm time; execution still binds the runtime late. A
     server that disappears in between fails in the executor as before.
5. **Tier inside the set:** yes. The resolver withholds tools above the tier
   from the menu and from load_tool, so execute_tools has ONE capability check
   (offered), plus the deliberately-kept chat-row no-machine refusal. The tier
   is still carried so load_tool and `Explain` can name it.

## 4b. Streams A and B, folded into the resolver (as built)

B (#494) and A (#493) merged first, each against the old store. This PR moves
both rules into the resolver and deletes their per-site checks.

- **B — owner-aware integrations.**
  - `usableIntegrations` (handlers/integration_access.go) asks
    `ToolsFactory.UsableIntegrations` which of the connection-gated
    integrations the (no-machine-narrowed) declaration reaches the owner can
    use. B's 5s timeout and its short-circuit are kept: a declaration that
    reaches no gated tool (`tools.ReachedGatedIntegrations`) never asks.
  - The answer is `CapabilityInputs.UsableIntegrations`, an allow-list, so it
    **fails closed** by construction: the resolver withholds every gated
    integration the declaration reaches that is not in it — no source, an
    unknown owner, a source error and a caller that never asked all mean
    "withheld".
  - What is withheld is recorded per integration
    (`withheld_integrations`: id → "it needs a GitHub connection, and the run's
    owner has none it can use (connect one in Settings → Integrations)"), not
    per tool. This document's earlier §4b planned a per-tool `Withheld` map; per tool it
    would have carried every action of every unconnected integration (29
    names today) with a repeated reason, twice per turn. Per integration it is
    at most four entries. The per-tool `Withheld` input had no other producer
    and was removed.
  - **B's workaround is gone.** `withoutUnusableIntegrations` turned a
    `LoadableAll` reach into an explicit list of the registry minus the
    withheld tools (plus the MCP names) whenever anything was withheld,
    because the store could not filter an unrestricted scope per name. The
    resolver filters per name, so `"*"` stays `loadable_all` and the
    expansion, `registryToolNames` and `withoutUnusableIntegrations` were
    deleted.
  - `githubReadable` (handlers/no_machine_repo.go) reads the set:
    `Offers(github__repo_get_content)`, or `Offers(load_tool)` and
    `CanLoad(github__repo_get_content)` — which already reflects whether the
    owner has GitHub.
- **A — `request_machine`.**
  - "Only without a machine" is the resolver's `has machine` exclusion: on a
    run with a machine, `request_machine` is neither offered, loadable,
    advertised nor searchable however it is named — preloaded, a glob,
    `loadable_tools: ["*"]`. On any run it is never loadable (it is handed
    over, so a grant of it can never exist to outlive the no-machine state).
  - The four scattered `OnlyWithoutMachine` checks were removed:
    `ExpandToolFilter`, `load_tool`, `DeferredToolNames` (deleted with the
    store) and `SearchTools`. `OnlyWithoutMachine` stays as the classifier
    (machine.go), like `NeedsMachine`.
  - `withoutMachineTools` keeps its API and still preloads `request_machine`
    for a no-machine node given any tools. It no longer expands `"*"` into an
    explicit list of non-machine tools — the same store-era workaround as
    B's; the resolver excludes machine tools per name — and `noMachineMenu`
    (which re-added `request_machine` to a name list the resolver no longer
    uses) was deleted.
  - `noMachineNotes(caps, repos)` reads `caps.Offers(request_machine)` and
    `githubReadable(caps)`. call_llm records `Offered` before building the
    system prompts, so the notes describe exactly the tool array sent.
  - A's DB constraint (`chats_no_machine_has_no_daemon`) and the
    `UpdateChatActiveDaemon` change are untouched;
    `TestCallLLM_ConnectingAMachineGivesTheNextTurnTheFullToolSet` passes.
- **D (#496)** touched only `preflight_daemon.go` and `db/core/trigger.go`; no
  overlap.
- **#500 — spawn management tools follow spawn history.**
  - #500 merged against the old store. It granted spawn_status, spawn_send
    and spawn_stop by appending them to the tool list after the menu was
    built, from `spawnHistory` (own children / children inherited by a
    branch, read from the DB).
  - Here they are resolver inputs instead (`OwnChildren`,
    `InheritedChildren`), and the rules are structural rows (§3.1). That is
    what puts them in the recorded set. Had they been appended past
    `RecordOffered`, the model would have been handed spawn_status and every
    call to it refused as not offered. That would land on the turn right
    after a fan-out, exactly when #500 meant it to work.
    `TestCapabilities_SpawnHistoryGrantsAreAcceptedAtExecution` pins this.
  - **Deliberately dropped:** #500 also kept the old store's habit of making
    load_tool, spawn_status, spawn_send and spawn_stop loadable under any
    explicit `loadable_tools` list. With the grants structural and read from
    durable state every turn, every case where one of them has something to
    act on is already covered by a structural row. An always-loadable set
    would only add them to load_tool's advertised list (`Deferred`) on nodes
    where they can do nothing: spawn_stop for an agent that cannot spawn,
    spawn_send with nobody to send to.
  - The cost is a `spawnHistory` read error. It withholds the grants for that
    one turn, and the next turn reads again. A node that declares them
    loadable can still load them.

## 5. Tests that pin this

| Behaviour | Test |
|---|---|
| A tool not offered is refused at execution, recorded FAILED | `TestExecuteTools_RefusesToolNotOfferedThisTurn`, `TestExecuteToolsActivity_CapabilityEnforcement` (handlers) |
| A tool loaded in turn N is offered and accepted in turn N+1 (and refused on turn N) | `TestCapabilities_LoadInTurnNIsAcceptedInTurnN1` (handlers, real call_llm + real load_tool), `TestToolCapabilitiesLoop/TestToolGrants_ThreadedFromExecuteToolsIntoNextCallLLM` (runtime, full DynamicWorkflow) |
| Grants and tier survive a worker restart between call_llm and execute_tools | `TestCapabilities_SurviveAFreshWorkerBetweenCallLLMAndExecuteTools` (handlers: fresh activity instances, set round-tripped through protojson) |
| No-machine still refuses machine tools, with and without a recorded set | `TestExecuteTools_NoMachineRunRefusesMachineToolsWithoutTrippingTheBreaker`, `TestCallLLM_NoMachineRunIsOfferedOnlyToolsThatRunWithoutAMachine` |
| Orchestrator tools and spawn with no shared memory | `TestCapabilities_OrchestratorToolRunsWithNoSharedMemory` (handlers), `TestToolCapabilitiesLoop/TestSpawnNotOffered_IsRefusedByTheActivityNotDispatched`, `TestWithCapabilitiesApplied_RoutesWhatTheSetDoesNotAllow` (runtime) |
| The runtime finds the producing call_llm's set | `TestUpstreamToolCapabilities_FindsTheProducingCallLLM`, `TestToolCapabilitiesLoop/TestNoRecordedSet_HandsExecuteToolsNone` |
| Grants cross continue-as-new | `TestContinueAsNew_CarriesToolGrants`, `TestToolCapabilitiesLoop/TestContinueAsNewSuccessor_SeedsGrantsIntoItsFirstCallLLM` (runtime) |
| A tool loaded in turn N survives the run dying and the coarse restart from its checkpoint: turn N+1 on a fresh worker is offered it and its call is accepted | `TestLoadedToolSurvivesACoarseRestartFromTheCheckpoint` (activities: production `RegisterAll` on a real DB, scripted model, full DynamicWorkflow twice) |
| A restarted fan-out hands the root and its relaunched sub-agent each their own grants | `TestFreshRestart_EachThreadKeepsItsOwnGrants`, `TestToolGrantsFromDurableState` (runtime), `TestListToolGrants` (db) |
| The grant is recorded with its result, and a retried ExecuteTools reports it | `TestExecuteTools_LoadToolGrantIsRecordedAndSurvivesARetry` (handlers) |
| The resolver, including B's fail-closed integrations and A's only-without-a-machine rule | `internal/llm/tools/capabilities_test.go` (`TestResolveCapabilities_UnusableIntegrationsAreWithheld`, `TestResolveCapabilities_RequestMachineOnlyWithoutAMachine`), `TestUnusableIntegrationsAreWithheldByTheResolver` (handlers) |
| Spawn management tools granted by spawn history (#500) are in the recorded set and accepted at execution; a branch's inherited sub-agents grant spawn_status only | `TestCapabilities_SpawnHistoryGrantsAreAcceptedAtExecution` (handlers), `TestResolveCapabilities_SpawnManagementToolsFollowTheThreadsSubAgents`, #500's `TestCallLLMActivity_SpawnManagementToolsOfferedOnceThreadHasSpawned` and `TestCallLLMActivity_SpawnToolsOfferedOnlyWhenReachable` |
| A bound value is applied at execution, a model value for a bound parameter is refused (repeating the bound value is not), an unbound parameter is untouched — on the daemon path and the local path | `TestBoundParameters_TakeEffectOnTheDaemonPath`, `TestBoundParameters_TakeEffectOnTheLocalPath`, `TestUnboundTool_RunsWithTheModelsInputUnchanged` (handlers: real call_llm → protojson → real execute_tools on the real RemoteExecutor; the daemon side decodes with its own fresh tool) |
| A global setting's bound value stays out of history, is re-read at execution, and a removed setting refuses the call | `TestGlobalBoundParameters_AreReadAtExecutionNotCarriedInHistory` (handlers), `TestRecorded_RecordsWhatTookEffectAndKeepsSettingValuesOut` (toolbindings), `TestCapabilities_ExecutionBindings` |
| The rule itself, and a bound tool's own Run agrees with it | `TestApplyBindings_MergesBoundValuesAndRefusesAModelOverride`, `TestBinding_ModelValueForABoundParamIsRefused`, `TestGenerateImage_ModelBindingStillLocks`, `TestGenerateVideo_BoundModelLocksAndIsHiddenFromSchema` |
| Bound params cross the workflow into execute_tools | `TestToolCapabilitiesLoop/TestBoundParams_ReachExecuteTools` (runtime), `TestCapabilities_ProtoRoundTrip` |
| Replay of pre-change histories | `TestReplayFixtures` (replaytest), unchanged fixtures |
| A webhook-fired run is not offered `edit_workflow` or a mutating integration action, cannot load it, and is refused (FAILED, with the reason) if it calls it; a person's turn in that chat is offered both and runs them; a named tool survives unattended; a sub-agent's turn is withheld the same | `TestUnattendedRun_IsNotOfferedLoadedOrRunAuthoringTools`, `TestPersonsTurnInAnAutomationsChat_IsOfferedAuthoringTools`, `TestUnattendedRun_ExplicitlyDeclaredToolStillRuns`, `TestUnattendedSubAgent_IsWithheldAuthoringTools` (handlers: real call_llm → protojson → real load_tool / execute_tools) |
| The runtime hands every call_llm the fact, a spawned sub-agent's included; a person's turn and a person-started chat are attended | `TestUnattendedCapabilities` (runtime, full DynamicWorkflow with a real spawn) |
| The launcher makes every unattended event kind's run unattended even when the caller forgot | `TestLaunchUnattendedFollowsTheEventKind` (launch) |
| The categories, every mutating catalog action withheld and every read-only one kept, the naming rule, the classification drift guard, the set's round trip | `internal/llm/tools/unattended_test.go` |
