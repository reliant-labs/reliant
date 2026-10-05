# Daemon-less runs: no-machine automations, workflows and chats

**Status:** implemented (stream H). Read with `DAEMONLESS.md` (the engine already
runs without a daemon) and `TOOL_PLACEMENT.md` (where a tool executes).

**Requirement (user, verbatim):** "users should be able to create
automations/workflows that may not have a paired daemon. we need to figure out
how to do that with proper tool call prevention, etc. as well as consider if we
want to allow normal chats without a daemon connected"

The failure mode to eliminate: a run with no machine is OFFERED shell/file
tools, every call comes back "no daemon connected", and after three of those
`DaemonOfflineCircuitBreaker` pauses the run. The model must never be offered a
tool that cannot run.

## 1. How a run knows it has no machine

**Decision: an explicit, durable `no_machine` fact on the CHAT row.** Set once at
launch, never inferred.

| Alternative | Why not |
|---|---|
| "The pinned daemon is absent" (infer at tool time) | Conflates two different situations. A run whose machine is merely asleep or offline must keep today's wake / "Waking <machine>…" / breaker behaviour, because the machine is coming back. A run that has no machine *by design* must never wait for one. Inference cannot tell them apart, and guessing wrong either strands an automation or wakes a laptop. |
| A workflow input (`no_machine`, like `unattended`) | Activities do not see workflow inputs; the flag would have to be threaded through every `RuntimeContext` construction site (ten of them) in workflow code, which is replay-sensitive. It also dies at the run boundary (`start_run`, SendMessage continuations) unless every launcher remembers to forward it. |
| A per-node / per-workflow "server only" annotation | It describes the workflow, not the run. The same builtin `agent` should run with a machine in one chat and without one in an automation. |

The chat row is read by every activity that could reach a machine (`call_llm`,
`execute_tools`, `invoke_tool`, `ExecuteRunStep`, `CreateWorktree`, the preflight
check), so it is one source of truth with no plumbing and **no workflow-code
change** (nothing new for replay to disagree with). Spawned sub-agents and loop
bodies share the chat, so they inherit it for free. `start_run` copies it to the
run it starts, the same way it copies the caller's daemon.

Where the explicit choice is made:

- **Triggers:** `TriggerDefinition.no_machine`. `daemon_id` is optional only
  with it: an empty `daemon_id` on its own is still InvalidArgument, so a
  client that omits the field (or an older one) cannot silently create an
  automation with no machine. The row stores `daemon_id = NULL` and
  `no_machine = true`, and `CHECK (NOT no_machine OR daemon_id IS NULL)` keeps
  the two from disagreeing. (A stricter `no_machine = (daemon_id IS NULL)` was
  tried and dropped: dozens of fixtures across other streams write trigger
  rows with no daemon, and the service layer already refuses that shape.) A
  pinned daemon that was later deleted still FAILS the fire as before; it never
  silently becomes a no-machine run.
  - *Rejected:* "empty `daemon_id` means no machine". One field cannot
    contradict itself, which is attractive, but it turns an omission into a
    mode switch, the thing an explicit choice exists to prevent.
- **Chats:** `StartChatRequest.no_machine`. A separate bool here, because an unset
  `daemon_id` already means "default resolution" for chats. `no_machine` with a
  `daemon_id` is InvalidArgument.

## 2. Tool-call prevention, in layers

The tool-level fact is **`tools.NeedsMachine(name)`**, not `Placement`.
`Placement` answers "which process executes this tool"; `NeedsMachine` answers
"does this tool touch the user's machine at all". They differ:
`view`/`write`/`edit`/`find_replace`/`move_code`/`save_attachment`/
`component_library`/`worktree` are any/server-placed but reach the user's
filesystem through the worker's daemon client. `NeedsMachine` is
`Placement == daemon`, every `mcp__*` tool (user MCP servers are always daemon,
`MCPServerPlacement`), plus that explicit list.

`RequiresDaemon` is left exactly as it was: it runs inside workflow code
(STEP 6.07), so changing its answer would change replay of in-flight runs. The
no-machine analysis is a new function, `runtime.MachineRequirements`.

| Layer | Where | What |
|---|---|---|
| (a) menu | `call_llm` (`getAvailableToolsWithSpawn`) | A no-machine run's tool list, its load_tool reach and its deferred-tool advertisement exclude every `NeedsMachine` tool; MCP servers are never listed (no `mcp.ensure_loaded` is sent). A one-paragraph system note tells the model it has no machine. `@local` models that relay through a machine report unavailable. |
| (b) execution | `execute_tools` / `invoke_tool` (`executeSingleTool`) | A `NeedsMachine` call in a no-machine run is refused before dispatch with an explanation. The text deliberately does NOT contain "no daemon connected", so `DaemonOfflineCircuitBreaker` classifies it as neutral and never pauses. |
| (b') transport | `nomachine` context marker | The activity marks its ctx. `NATSDaemonRouter` refuses to resolve (so it can never wake) a daemon for a marked ctx, `RemoteExecutor` drops the per-request daemon client and refuses daemon dispatch, and the daemon MCP binder binds nothing. A tool that slips past (a) and (b) still cannot reach a machine. |
| (c) authoring | launch + `TriggerService` | See §3. |

Backstops for nodes: `ExecuteRunStep` returns a failed command (exit -1, the
refusal as output), not a Go error, so it does not enter the retry-exhaustion
pause; `CreateWorktree` errors; `PreflightDaemonCheck` returns available without
touching the router.

## 3. Static refusal: learn it at create time, not fire time

`runtime.MachineRequirements(wf, inputs, loader)` returns human-readable reasons
a workflow cannot run without a machine, recursing into inline and `ref:`
sub-workflows:

- **Hard** (the node cannot run at all): a `run` node, a `create_worktree` node,
  an `action` whose integration is daemon-placed (or not statically resolvable),
  an `invoke_tool` of a `NeedsMachine` tool, an explicit workflow/node `daemon:`.
- **Tools** (statically known machine tools): a call_llm literal PRELOADED tool
  list that reaches a machine tool, or a `tools`-typed workflow input whose
  value (the trigger's params, else the schema default) does. `loadable_tools`
  is not counted: it is a ceiling on discovery (`["*"]` means "reach what you
  need"), and a no-machine run narrows it rather than refusing the workflow.
  CEL tool lists are not resolvable statically and are left to layer (a).

Who refuses what:

- **Starting a no-machine run (any launch):** hard reasons → FailedPrecondition.
  Tool lists are not refused: an attended chat with `tag:coding:default` simply
  gets the web/planning/integration subset (layer a).
- **Creating/updating a trigger with no machine:** hard AND tool reasons →
  FailedPrecondition, e.g. *"this workflow needs a machine: input `tools`
  includes shell, view, …. Pick a machine for this automation, or give it only
  tools that run without one (for example tag:web)."* An unattended author made a
  deliberate choice, and a definite contradiction should surface while they are
  at the form, not as a degraded 9 a.m. run nobody watched. So builtin `agent`
  with `tools: ["tag:web"]` is a valid no-machine automation, and the default
  `tag:coding:default` is not.
- **Builder/UI:** `WorkflowListItem.needs_machine` (hard and tool reasons,
  inputs at their defaults) drives whether the Automations form offers "No
  machine (server only)": enabled only when it is empty, and otherwise shown
  disabled with the reason. The trigger write re-checks with the trigger's own
  params, and its FailedPrecondition is shown under the machine picker.

Devil's advocate on refusing tool lists for triggers: filtering alone (layer a)
already prevents the failure mode, so why refuse? Because for an unattended run,
"the agent quietly had fewer tools than you configured" is a silent degradation
nobody sees. Refusal is only for statically DEFINITE contradictions. Anything CEL
still runs, filtered.

## 4. Chats without a machine: recommendation

**Recommend allowing them, as an explicit "No machine" chat, clearly labelled,
with web + integration + planning + workflow tools only, and a "Connect a
machine" upgrade path.** Reasons: it is the natural home for research, writing,
triage and integration work (Slack/GitHub/Gmail actions) that never needed a
checkout, and it lets a user without a daemon (web sign-up, machine not yet
provisioned) get value on minute one instead of hitting "Paused: no machine is
connected".

What is built: the whole server side. `StartChat{no_machine: true}` launches a
no-machine chat, `Chat.no_machine` is returned, all prevention layers apply, and
no wake is attempted on start or send.

What is NOT built (scoped follow-up, the product call is the user's): the chat
composer's machine picker option, the "No machine" label in the chat header, and
the upgrade path (flip a chat to a machine). The upgrade is a deliberate design
question rather than a toggle. A no-machine chat's history contains no file
context, so continuing it on a machine is safe, but the reverse (machine → none)
would strand tool results that referenced files. Recommended shape: allow
none → machine only, through `SetChatDaemon`, clearing `no_machine`.

## 5. Interaction with runs that DO have a machine

Unchanged. `no_machine` defaults to false. Every new branch is gated on it, the
breaker, wake and "Waking <machine>…" paths are untouched for a run with a
daemon, and `RequiresDaemon`/preflight answer exactly as before (pinned by
`TestRequiresDaemonIsUnchangedByTheNoMachineAnalysis`). The breaker story
(`story_05`: three offline results pause, a message resumes) passes unchanged.

## 5a. Tests

| Behaviour | Test | Shown failing first |
|---|---|---|
| A no-machine `call_llm` is offered only server/any tools; MCP never asked | `handlers.TestCallLLM_NoMachineRunIsOfferedOnlyToolsThatRunWithoutAMachine` | yes: offered `shell`, `view`, `edit`, `write`, `code_context`, the MCP tool |
| A machine tool is refused without tripping the breaker | `handlers.TestExecuteTools_NoMachineRunRefusesMachineToolsWithoutTrippingTheBreaker` | yes: reached the executor |
| Transport never resolves or resumes a daemon for a marked ctx | `toolexec.TestNoMachineContextNeverResolvesOrResumesADaemon` (+3) | yes |
| A trigger without a machine on a machine workflow is refused at create | `services.TestCreateTrigger_NoMachineRefusesAWorkflowThatNeedsAMachine` | yes: `daemon_id is required` |
| …and on a server-only workflow is accepted and fires end to end, `DaemonRouter: nil`, `fetch` against httptest | `stories.TestStory13_NoMachineAutomationFiresEndToEnd` | yes: refused at create, and the run died on `unattended` |
| A no-machine chat on the default agent is offered only server tools | `stories.TestStory13_NoMachineChatIsOfferedOnlyServerTools` | yes: with the menu filter disabled it is offered the shell and file tools |
| Machine-backed runs unchanged | `story_05`, `preflight_*`, every existing story | n/a |

## 6. Found and fixed on the way

- **Every trigger fire of a workflow with inputs failed (pre-existing, on
  main).** The launcher injects `inputs.unattended` after its own validation,
  but the runtime validates inputs AGAIN inside the workflow (STEP 5.5) and
  exempts only `RuntimeInjectedInputs`, so `unattended` was rejected as an
  unknown input. Schedule, webhook, integration and workflow-event fires of the
  builtin agent all died before their first node. Every trigger e2e test stubs
  `DynamicWorkflow`, which is why none caught it. Fixed by adding `unattended`
  to `RuntimeInjectedInputs`.
- **Clients could write engine-injected inputs.** `BuildWorkflowInputs`
  copied request params into the run unfiltered, and validation skips
  `RuntimeInjectedInputs` precisely because the engine sets them. So a
  `StartChat` could claim `session_daemon_id`, a forged `__trigger`, and (after
  the fix above) `unattended`. Client params can no longer name an injected key.
- **Concurrent `StartWorker` calls raced on the schema registry.**
  `IsActivityTypeRegistered` followed by `RegisterActivityType` on an unlocked
  global map; two workers in one process (the parallel e2e stories) panicked
  with "activity type already registered". Now an atomic
  `RegisterActivityTypeIfAbsent` under a mutex.

## 7. Deferred

- Chat UI (picker option, label, upgrade path), §4.
- Agent `activate_trigger` / builder rail surfacing `MachineRequirements`
  (stream F/G consume `runtime.MachineRequirements`).
- Presets are not applied when the trigger check evaluates `tools` inputs (params
  and schema defaults are). A preset that narrows tools to web-only does not by
  itself pass the check; setting the trigger's `tools` param does.
- `activate_trigger` (stream F) defaults the activation's daemon to the calling
  chat's. From a no-machine chat that default is empty, so the activation is
  refused ("daemon_id is required … or set no_machine") rather than silently
  pinned anywhere. It should inherit `no_machine` the way `start_run` now does;
  that is a one-field change to F's request plumbing, left to F.
- The Automations form offers "No machine" from the workflow's
  `needs_machine` at its input DEFAULTS. A workflow whose default tools need a
  machine but which the user narrows in the form's inputs (e.g. the builtin
  agent with `tools: [tag:web]`) is offered "No machine" only after saving
  through the API; re-evaluating with the form's live inputs is a follow-up.
