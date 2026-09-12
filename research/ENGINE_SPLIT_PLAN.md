# Teasing the coding product out of the workflow engine

**Status:** partly shipped — see the ledger below. Grounded in the verified
research docs in this directory: `DAEMONLESS.md`, `TOOL_GATING.md`,
`CODING_LEAKAGE.md`, and the permissions set (`PERMISSIONS_ASBUILT.md`,
`PERMISSIONS_INTERACTIVE.md`, `PERMISSIONS_PRIOR_ART.md`).

Every claim below carries a FILE:LINE citation from those docs. Where an earlier
document disagreed, the citation wins.

> Three companion investigations — trigger semantics, node primitives, and
> tenancy — were run before this branch existed and their findings are quoted
> inline here rather than filed as separate documents. Where this plan cites
> them, the citation is what survived.

### Shipped since this plan was written

| Item | Outcome |
|---|---|
| PR 1: delete the daemon preflight gate | **Cancelled — the gate is live.** Temporal dispatches activities by STRING NAME, so a call-graph "no callers" result means nothing. It is registered and invoked, and caused a real incident. Do not delete it. |
| PR 2: `fetch`/`websearch` off the daemon | Merged (#250). Bigger than expected: `RequiresDaemon` treats any daemon-located tool in a filter as proof the whole workflow needs one, and both carried `TagDefault` — so `tag:default` alone fired the preflight gate. |
| PR 3: loaded-tools store lifetime | Merged (#251). Scoped to `(chat, thread)`; fails closed; `Clear` wired into cleanup. |
| PR 4: `tools:` authoritative | Merged (#252, absorbed into #253). Enforced at load AND execute, covering MCP. |
| PR 5: deny boundaries | **Superseded.** Became "remove the readonly tier" (#253) after the permission research — see `PERMISSIONS_REDESIGN.md`. |
| Security: plaintext git tokens | Merged (control-plane #248). |

The remaining items — the run container, the generic `action` node, triggers,
`await_external` — are unchanged and still the plan.

---

## The thesis

The engine is far more general than it looks. The graph model, flow control, the
CEL layer, the activity-input registry and the `Input` type oneof are already
domain-neutral. What makes it "a coding product" is a small number of concrete
couplings, and they are separable in an order that keeps every step shippable.

Three findings set the whole sequence:

1. **A run cannot exist without a chat and a thread.** `workflows.chat_id`,
   `.thread` are `NOT NULL` (`init_schema.sql:92-94`), and six generic tables
   carry real FKs into `chats`. This blocks triggers, blocks daemon-less
   headless runs, and blocks tenancy — all three at once. It is the first PR
   because everything else waits on it.
2. **`tools:` is a hint, not a boundary.** The workflow's filter is expanded
   once and discarded (`call_llm.go:993`, `:1831`); `load_tool` consults only
   the permission ladder (`load_tool.go:101-117`), and loaded tools are appended
   *after* filter expansion (`call_llm.go:1833-1842`), overriding even an
   explicit `!write`.
3. **Nothing structural requires a daemon.** All four start paths reach
   `ExecuteWorkflow` with no daemon resolution, and the hermetic e2e harness
   already runs the story suite with `DaemonRouter: nil`
   (`e2e/stories/harness_test.go:222`). The requirement lives in 10 tools and 2
   node types, not in the engine.

The engine is closer to general-purpose than the prior split doc assumed. The
work is mostly subtraction.

---

## PR series

Ordered so each PR ships independently and none is a big-bang. Phase 1 is
pure bug-fix and cleanup with no schema change; phase 2 is the structural
migration; phase 3 is the new surface.

### Phase 1 — subtraction and correctness (no schema change)

**PR 1: Delete the dead daemon preflight gate.**
`handlers/preflight_daemon.go` defines an activity that hard-fails with "this
workflow requires a daemon" and has **zero inbound callers** (verified via
`code_context`). Delete it so nobody wires it back and re-imposes the
requirement we are about to remove. Pure deletion.

**PR 2: Move `fetch` and `websearch` to `ToolRunsAnywhere`.**
`registry.go:467-468`. Both are pure `net/http` plus HTML parsing — no `os`,
`exec`, or `filepath`. The registry's own comment at `:126-128` names
"network-only tools like fetch/websearch" as the *example* of `ToolRunsAnywhere`,
so the current marking contradicts the documented intent. Two lines.

*Product decision to name explicitly in the PR:* egress IP moves from the
user's machine to the server. The daemon marking was a deliberate policy choice,
so this needs a yes, not just a correctness argument. `code_context` stays on the
daemon — it `os.Open`s files (`code_context_source.go:159`) and spawns language
servers (`code_context.go:752`).

**PR 3: Fix the loaded-tools store lifetime bugs.**
These are live correctness bugs, independent of any redesign
(`loaded_tools_store.go`):

- `Clear()` has no production caller — every caller in the tree is a test. Tools
  therefore leak across runs of the same chat (a `plan` run inherits `write`
  from a previous `auto` run) and across the spawn boundary in both directions.
- `GetPermission` returns `PermissionOrchestrator` when unset (`:149-157`), so a
  worker restart **fails open**: any `execute_tools` between restart and the next
  `call_llm` sees orchestrator.
- Permission is last-writer-wins per chat, so concurrent background spawns
  overwrite each other's level. The parent-cap logic at `call_llm.go:809` is
  computed correctly but stored in a slot parent and child share.

Fix: key the store by `(chatID, thread)` rather than chatID alone, default
`GetPermission` to the *least* privilege rather than the most, and clear on run
completion. Each is small; together they are one coherent PR with tests that
fail before and pass after.

**PR 4: Make `tools:` authoritative.**
Persist the workflow's expanded filter alongside the permission, and have
`load_tool` intersect against it. Concretely: add an allow-set field to the
store, write it where `SetPermission` is written (`call_llm.go:816-818`), and
check it in `loadTool` (`load_tool.go:101`) before the ladder check. Also
intersect at the append site (`call_llm.go:1833-1842`) so a loaded tool cannot
outrank an explicit exclusion.

Keep the permission ladder — it is orthogonal and it works. The rule becomes:
**a tool must pass both the filter and the ladder.**

Two sub-decisions worth making deliberately:

- **MCP currently bypasses the ladder entirely** (`load_tool.go:146`), so a
  `permission: readonly` agent can load any connected mutating MCP tool. MCP
  servers are scoped per *project* with no workflow-level say. The filter should
  cover MCP too — probably via `tag:mcp` and glob patterns, which the grammar
  already supports.
- **Spawn inheritance:** `buildSpawnChildInputs` (`workflow.go:2580-2598`)
  passes only mode, unattended, and `parent_permission`. The parent's tool list
  never constrains the child. `parent_permission` is the one constraint that
  genuinely works today — extend the same pattern to carry the allow-set.

**PR 5: Add a `deny` that means it.**
The one thing the current grammar cannot express is "this agent may never touch
the network / the filesystem / spawn," enforced below the tool layer. Note the
existing documented caveat (`permissions.go:101-127`): the shell family is
readonly-tier on purpose, so `readonly` never meant "cannot write" — it meant
"is not handed write tools." A hard boundary has to be enforced at the sandbox,
not the registry. This PR adds the *declaration*; enforcement lands with the
executor work.

### Phase 2 — the structural migration

**PR 6: Introduce a run container; make chat and thread optional.**
The load-bearing one. Today `chats` plays three roles at once: coding product
object, run container, and — surprisingly — the engine's identity carrier, since
activities re-derive `user_id` from the `chats` row rather than from the
credential (`call_llm.go:198-205`).

Split those roles:

- A generic run-container row owning `id`, owner/space, thread (**nullable**),
  status.
- The coding-specific `project_id` + `worktree_id` move to a coding-owned table
  keyed by that container.
- The six FKs (`threads`, `messages`, `tool_calls`, `agent_messages`,
  `user_updates`, `background_processes`) repoint at the container.
- `workflows.chat_id` — `NOT NULL` with **no FK constraint**, so the coupling is
  by convention and by every join, harder to find than a declared one — becomes
  a real, nullable FK to the container.

The high-volume tables (`messages`, `threads`, `tool_calls`) do not move; only
the parent pointer is reinterpreted. That is what makes this affordable.

`call_llm`, `compact` and `drain_agent_messages` currently hard-fail with
"thread is required" (`call_llm.go:192`, `compact.go:112`,
`drain_agent_messages.go:70`). `CallLLMArgs.messages` (tag 10) already exists for
ad-hoc LLM calls without a thread — that is the seam to widen rather than a new
concept to invent.

**PR 7: Rename `daemon:` to a runtime/executor selector.**
`DaemonSelectorProto` (`workflow_v2.proto:103-120`) is already a generic executor
predicate — id, name, type, labels. Only the *name* is coding-flavored. Rename
the field on `Workflow` (tag 12) and `Node` (tag 7), keep semantics identical,
reserve the old tags. Cheap, high-visibility, and it stops the vocabulary
hardening further.

Worth noting for the trigger work: unset does **not** mean "no daemon" — it means
default resolution, which prefers a `local` daemon
(`daemon_router_nats.go:130`). A headless run needs an explicit "none" that is
distinct from unset.

**PR 8: Demote `create_worktree` from a node type to a tool/action.**
It is a git concept occupying one of twelve core oneof arms, and it does far more
than its name suggests: reads the chat, refuses without `ProjectID`, enumerates
nested repos, fans out per-repo daemon RPCs, and rolls back partially
(`worktree.go:86-260`). It is used by **exactly one** builtin
(`parallel-compete.yaml:149`). Reserve arm tag 15.

**PR 9: Clean the CEL `workflow` root.**
`model.WorkflowContext` (`model/context.go:12-21`) exposes `branch` and
`worktree_path` among 8 fields. Better than expected — it does *not* leak
chat/project/repo — so this is a small, contained rename/move. The other five
CEL roots are already clean; do not churn them.

### Phase 3 — the new surface

**PR 10: The generic `action` node.** See "New APIs" below — this is the
extensibility unlock and everything in a general engine depends on it.

**PR 11: `trigger` as a first-class datum.** Then the trigger sources.

**PR 12: `await_external`.** The long-running-job primitive.

---

## API refactors, splits, and new APIs

### The headline finding

**`WorkflowService` cannot run a workflow.** Its RPCs are CRUD and authoring
only — List/Save/Get/Delete/Validate/Import/Export/SetVisibility/Copy/BuilderChat
/CreateWorkflowDraft/AssociateChatWithWorkflowDraft (`workflow.proto:19-57`).
Every actual run starts through `ChatService.SendMessage` or `CreateChat`.

That is the API-level statement of the seed-message problem: **the only way to
start a workflow is to talk to it.** And the CLI already calls a
`/api/v1/workflows/run` endpoint that is implemented nowhere — zero hits across
the whole worktree. The ghost endpoint is the API this design is missing.

### Split: `ChatService` → conversation + run control

`SendMessage` is one verb meaning two things, disambiguated by run status, not by
anything about the message (`chat_send.go:459+`): `paused` → save + resume + wake;
`active` → save + wake; otherwise → new `ExecuteWorkflow`.

Split the verb:

| New RPC | Owner | Meaning |
|---|---|---|
| `RunService.StartRun(workflow, trigger, inputs)` | engine | Create a run. No message required. |
| `RunService.SignalRun(run_id, event)` | engine | Deliver an event to a live run. Never starts one. |
| `RunService.GetRun` / `ListRuns` / `CancelRun` / `PauseRun` | engine | Lifecycle, currently scattered. |
| `ChatService.SendMessage` | coding | Thin shim over both, preserving today's behavior. |

The mechanism for `SignalRun` already exists and is good: durable
`SaveMessageToThread` plus the deliberately content-free `thread_wake` Temporal
signal (`threadwake/threadwake.go:36`), whose package doc explains that carrying
no body keeps the DB the single source of truth and makes a dropped signal
harmless. Generalize the envelope; keep the design.

Note the existing HITL paths are already event-shaped in the same way:
`ask_question` blocks on a dynamic signal channel `"signal.question."+questionID`
(`question_flow.go:97`), and `ReplyToQuestion` signals it (`question.go:179`).
That is architecturally the trigger mechanism, addressing a node inside a run
rather than a run that does not exist yet.

### Split: coding-domain services leave the engine surface

Already cleanly separable, since nothing generic references them:
`ProjectService`, `RepoService`, `WorktreeService`, `PackageCommandsService`.
`FileSystemService`, `TerminalService` and `BackgroundService` are candidates to
collapse into one generic `Invoke(runtime, tool, params)` — a single surface for
both LLM tool execution and direct UI calls. The generic daemon envelope already
exists (`DaemonCommandRequest`: string type + JSON payload), so `Invoke`
formalizes what is already the wire shape rather than inventing one.

### New: `TriggerService`

```
CreateTrigger(workflow_ref, kind, config, input_mapping) -> trigger_id, ingress_url?
ListTriggers / GetTrigger / DeleteTrigger
SetTriggerEnabled(trigger_id, enabled)
```

Design decisions, with the reasoning:

- **Triggers are a top-level `triggers:` block, not node types.** n8n makes them
  nodes and pays for it: two different predicates for "is this a trigger" that
  can disagree (UI checks `group`, engine checks method presence plus a
  hardcoded `STARTING_NODE_TYPES` list), and error handling and sub-workflow
  invocation awkwardly modeled *through* the trigger abstraction. We do not need
  that.
- **`trigger` becomes a CEL root distinct from `inputs`.** The seat is already
  reserved: `WorkflowInput.Inputs` is documented `// Configuration inputs (NOT
  trigger data)`, and an unpopulated `trigger` root already exists in the v3 docs
  layer (`v3/reference/cel_reference.go:80`). Populate it. Then
  `{{ trigger.body.foo }}` reads naturally beside `{{ inputs.model }}` and the
  two never get confused.
- **Copy n8n's one genuinely elegant move:** the trigger's payload lands in the
  first frame using the *same* data type as node-to-node flow
  (`webhook-helpers.ts:333`). That uniformity is what makes triggers ordinary.
- **Split stateless from stateful, as n8n's own code does**
  (`active-workflow-manager.ts:522-537`): webhooks are stateless rows any
  instance can serve; schedules and pollers are stateful and need leader
  election. Get this boundary wrong and leader election lands on the wrong half.
  **Temporal removes most of this pain** — schedules are native, so n8n's
  `multi-main-setup.ee.ts` / `@OnLeaderTakeover` machinery largely evaporates.
- **Decide multiple-triggers-per-workflow deliberately.** n8n allows it and its
  manual-run disambiguation is a `__getStartNode` hack carrying a
  `// TODO: Identify later differently` and a special case to skip chat triggers.

The seed message then stops being special: it is a `chat.message` trigger whose
payload maps onto a `message` input — **and that input type already exists** in
the `Input` oneof, alongside `attachments`. The unification is nearly free.

### New: the generic `action` node — the extensibility unlock

The 12-arm `oneof args` (`workflow_v2.proto:270-283`) is a hard ceiling: you
cannot add an arm per integration. One new arm removes the ceiling permanently:

```yaml
- id: send_slack
  type: action
  uses: slack/chat.postMessage@v1
  with: { channel: "#eng", text: "{{ nodes.summarize.response_text }}" }
```

`RunArgs` is the closest existing thing and the natural template. Build the
declarative action spec as the **only** style, from day one — that is the real
n8n lesson: they built the declarative builder and **only 14 of 307 nodes use
it**, because the long tail was written the expensive way first (Slack is 11,709
lines; a declarative node is ~350 and nearly pure data). If there is a code
escape hatch, the tail goes through the hatch and we inherit their problem.

A pure-data, per-resource spec is also exactly the shape an LLM can generate from
an OpenAPI document — which turns "how many integrations do we ship" into "how
fast can a user get the one they need."

**Steal `usableAsTool`.** The best idea in the n8n codebase: any integration
becomes an agent tool with a boolean, because the parameter schema was already
machine-readable. For an agent-native engine, one declarative spec being
simultaneously a workflow node *and* an agent tool is the whole point. Build that
duality deliberately rather than discovering it later.

### New: `CredentialService`

A first-class credential object, sibling to the action spec, with `extends`
inheritance and `authenticate` living on the credential so every action gets auth
free. That is what actually makes integrations cheap. Do **not** copy n8n's
encryption: one instance-wide AES-256-CBC key with no tenant isolation.

Related and independent of all of this: `git_credentials.access_token` is stored
**plaintext** in control-plane's DB, while its neighbour `reliant_api_keys` uses
SHA-256 plus AES with rotation-healing (`reliant_api_key.go:76-77`). Remediation
is a migration plus an existing helper. That is a today problem.

### New: `await_external` — the missing async primitive

The biggest genuine engine gap for the capabilities you named. `approval` and
`ask_question` pause for **humans** only. A phone call, a video render, a batch
inference job are all "start work elsewhere, come back in minutes-to-hours." One
primitive covers all of them:

```yaml
- id: render
  type: await_external
  uses: video/generate@v1
  with: { prompt: "..." }
  timeout: 30m
```

Without it, every such capability becomes a blocking activity fighting Temporal
timeouts. **Build this before any media integration.**

### Model capability APIs

Input is genuinely multimodal today — `ContentPart` includes `ImageURLContent`
and `BinaryContent{Path, MIMEType, Data}`. **Output is text plus tool calls,
full stop.** The `Driver` interface (`internal/llm/types.go:114-129`) has two
methods, both shaped `(prompts, messages, tools) → text`, and no `DriverEvent`
variant could carry produced media. `ModelCapabilities.SupportedFileTypes`
(`models/types.go:474`) even names audio and video in its doc comment while
`models.yaml` only declares `[image, pdf, text]` — the vocabulary anticipates it,
nothing ships it. `ModelCapabilities` also describes *input* modality only and
needs output flags.

**Sequencing recommendation:** ship image/voice/video generation as **tools
returning asset handles** first. Zero engine change, and it tells you what the
ergonomics need to be before committing to a node type and a driver-interface
change.

**Design asset handles before the first media feature.** n8n retrofitted
filesystem/S3 storage over an inline-base64 default and only survived because the
accessors were centralized. Today's `BinaryContent{..., Data}` tempts us toward
inline. Media must be handles from day one.

### Also needed for a programmable engine

- `http_request` node, a **deterministic** `switch` (today's `router` is
  LLM-based), `wait`/timer, and try/catch error handling.
- **Per-node retry config.** Retries are hardcoded `MaximumAttempts: 5`
  (`step_executor.go:859`). This must be configurable before anyone builds a real
  integration on it.
- A transform/code node. CEL is good for conditions and is not a data-mapping
  language.
- Sub-workflow `outputs` are untyped `map<string,string>` of CEL expressions,
  which is why `structured-agent.yaml` is full of defensive
  `has(...) && ... != null` guards. Typed outputs would remove a whole class of
  workflow-authoring pain.

---

## Tenancy: one identity plane, two product surfaces

I recommend **against** the prior doc's dual account system, for now.

The parity test — *if reliant-coding can do it, any-company.io can do it the same
way* — is the right rule. But parity comes from the API surface being the only
door, not from a second identity plane. And the split is further along than the
doc credits: **control-plane already owns the only `users` and `organizations`
tables** in the workspace; reliant holds opaque `user_id` strings across 23
tables with no user model of its own. That is most of the boundary, already
built.

What is actually missing is narrower: reliant knows *users* but never *orgs*, so
it has no scoping primitive for a tenant that is not a single human. Add that —
a flat space/tenant container owning daemons, runs, threads, workflows; the
audience for a token; the dimension for metering. Backfill 1:1 from distinct
`user_id`, dual-accept today's JWTs, no client changes on day one.

Hold the "no members, no nesting, no billing on spaces" constitution for v1.
Members on spaces is how you rebuild orgs one level down.

**The counter-argument, honestly:** splitting identity later is harder than
splitting it early, because once coding's product objects reference engine users
directly you cannot cleanly introduce a boundary. My answer is that the boundary
you need is `chats` (PR 6), not identity — and if PR 6 lands and coding's product
objects stay on the coding side of it, a later identity split stays cheap.

**Do not abstract Temporal.** 121 files import it directly, there is no port to
swap, and durability is precisely what n8n lacks: its engine is an in-process
stack interpreter, queues whole executions via Bull-on-Redis, and loses in-flight
work on a crash — `ExecutionRecoveryService` is forensic reconstruction so the UI
shows a coherent failure, not resumption. What is worth copying from n8n is
everything *above* the engine.

---

## Corrections to the earlier split doc

Several load-bearing "verified facts" in the prior document are wrong:

| Claim | Reality |
|---|---|
| Temporal imported by 64 files | **121** |
| ~38 tables with `user_id` | **23** (of 46) |
| Daemon PATs grant the full user surface | **False.** A `kind` column splits daemon from api with two mutually-rejecting validators (`auth/pat.go:56-68`) |
| No markup anywhere (`actual == billed`) | **False.** `actual_cost_usd_nanos` and `billed_cost_usd_nanos` are separate columns with live overage and refund paths (`canonical_metering.go:151,170`). No markup *multiplier*, but the seam exists |
| No rate limiting in the engine | **Partial.** The MCP server ships a 120rpm/burst-30 limiter (`mcpserver/limits.go:23-29`); only the main RPC surface is unlimited |

Its structural instincts are largely sound. Its specifics need re-checking before
anything relies on them.
