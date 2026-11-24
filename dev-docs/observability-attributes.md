# Observability attributes: agent and workflow spans

The one reference for every span name and attribute that Reliant's agent and
workflow telemetry emits, and the rules a new one has to follow. It describes
`origin/main` at `33be58850` plus two open PRs, and marks what only they add:

| Marker | PR | Adds |
|---|---|---|
| (#3286, pending merge) | [#3286](https://github.com/reliant-labs/reliant/pull/3286) | the `integration.call` span and every `reliant.integration.*` attribute |
| (#3289, pending merge) | [#3289](https://github.com/reliant-labs/reliant/pull/3289) | the `trigger.fire` span, every `reliant.trigger.*` attribute, and `reliant.trigger.event_id` on the run's workflow span |

Two other PRs this reference covers have already merged and are described as
current: #3283 (a trigger-launched run starts its own trace, linked to the
firing span) and #3285 (`run.exec`). When #3286 and #3289 merge, delete the
markers.

Where to look in code: `internal/telemetry/ai_attrs.go` (gen_ai and `forge.ai.*`
names), `ai_spans.go` (node, agent turn, tool call), `run_scope.go` (the scope
and the `reliant.chat.id` family), `run_exec_attrs.go` and `run_exec_span.go`,
`ai_error_type.go` (the `error.type` vocabulary of the AI spans),
`gateway_attrs.go` (the LLM gateway's call id on the `chat` span),
`internal/llm/genai_span.go` and `genai_error_type.go` (the provider call), and
`internal/workflow/runtime/run_telemetry.go` (the run's own span). #3286 adds
`integration_attrs.go` and `integration_spans.go`; #3289 adds `trigger_attrs.go`
and `internal/triggers/fire_span.go`.

The four AI spans of a turn (`workflow.node`, `invoke_agent`, `chat`,
`execute_tool`) are opened by forge's `pkg/observe/genai` starters (`StartNode`,
`StartAgent`, `StartChat`, `StartTool`); Reliant's `StartNodeSpan`,
`StartAgentTurn`, `llm.StartGenAISpan` and `StartToolCall` are thin wrappers that
put the run scope into forge's scope (`genai.WithScope`) and add Reliant's own
attributes. Their instrumentation scope is forge's
(`github.com/reliant-labs/forge/pkg/observe/genai`). Every `gen_ai.*` and
`forge.ai.*` constant in `ai_attrs.go` is an alias of forge's, so the names here
and the ones forge's AI dashboards read cannot drift. The span tree of a scripted
turn is pinned by `internal/llm/genai_span_tree_test.go` against
`internal/llm/testdata/genai_span_tree.golden.json`: names, kinds, parents,
attribute keys, events, links and statuses. A change there is a change to this
contract.

**Sampling.** The tracer provider `observability.Init` installs (forge's
`observe.Setup`) wraps the `OTEL_TRACES_SAMPLER` sampler in forge's
`genai.KeepSampler`: a span that starts with `gen_ai.operation.name` or any
`forge.ai.*` attribute is always recorded and sampled, because token and cost
totals are sums over them. Every other span is the base sampler's
(`internal/observability/keep_sampler_test.go`).

The spans go to Reliant's own ClickStack through `internal/observability` and
only staff can query them today. Nothing here is exported to tenants yet; the
[last section](#what-tenant-export-will-rely-on) says what that future export
depends on, so the rules below are written to it.

## The span tree

A workflow run is one trace. Every span below the run is opened worker-side, in
an activity, never in workflow code: workflow code is replayed, and a span
opened there would be emitted again, or lost, on every replay.

```
RunWorkflow:DynamicWorkflow          the run (forge's temporalotel interceptor)
│  gen_ai.operation.name=invoke_workflow; links → trigger.fire, if a trigger launched it
└ workflow.node <node id>            one execution of a node (Internal)
  ├ invoke_agent <agent>             one agent turn (Internal)
  │ ├ chat <model>                   one provider call (Client)
  │ └ execute_tool <tool>            one tool call the turn asked for (Internal)
  │   └ integration.call <id>.<op>   an agent's integration tool (Client)   (#3286, pending merge)
  ├ run.exec <node id>               a run node's command (Internal)
  └ integration.call <id>.<op>       an action node's call (Client)         (#3286, pending merge)

trigger.fire <event kind>            a separate root trace (Internal)       (#3289, pending merge)
```

An agent turn's tool calls run in the next activity (`execute_tools`), after the
turn's own activity has ended, possibly on another worker. The turn's span
context travels there in the workflow's data (`TraceParent`), so each
`execute_tool` is a child of the turn that asked for it and keeps a *link* to the
`execute_tools` node's span. A tool call with no turn recorded (a call made
through `invoke_tool`) is opened under whatever span the context holds.

`trigger.fire` is not part of the run's trace. A run a stored trigger launched
(schedule, webhook, integration, workflow event: `TriggerEventKind.Unattended()`)
starts a trace of its own, and its `RunWorkflow` span carries a span link to the
`trigger.fire` span that fired it. The reason is multi-tenancy: one GitHub
delivery or one run event fans out to the triggers of several orgs. Parenting each
launched run under the span that fired it (what the Temporal tracing interceptor
did before #3283) put one trace across several orgs. A person's own start (chat
start, manual run, CLI run) is *attended* and stays in the trace of the request
that made it. See [Trigger-launched runs](#trigger-launched-runs-and-the-link).

Spans that other libraries open inside this tree carry no Reliant attributes:
Temporal's `StartWorkflow` / `RunActivity` spans, and `otelhttp`'s `HTTP POST`
spans, which are children of `chat <model>` (retries included) and of the gateway
round trip. They are infrastructure, and they have no `org.id`
([audit gaps](#known-gaps)).

## What every span in the tree carries: the run scope

A span opened inside a run reads the run's identity from a `RunScope` on its
context. The workflow decides it (which run, which workflow) and hands it to each
activity in the runtime context; the worker's run-scope interceptor
(`internal/observability/run_scope.go`) adds the chat's user and org from a cache
of chat owners. `RunScope.SpanAttributes()` writes it, and `GenAIAttributes()`
writes the same plus the gen_ai names for the same facts. Empty fields are left
out.

| Attribute | Type | Meaning |
|---|---|---|
| `org.id` | string | The org the work is for. Every span carries it ([the rule](#the-rules)). |
| `user.id` | string | The chat's owner, an opaque id. |
| `reliant.chat.id` | string | The chat. Also on log records. |
| `thread.id` | string | The agent thread. Also on log records. |
| `workflow.id` | string | The Temporal workflow id. Also on log records. |
| `forge.ai.workflow.name` | string | The workflow definition: `agent`, or a project workflow's file name without extension. |
| `forge.ai.workflow.ref` | string | The reference the run started with (`builtin://agent`, a project workflow's path). |
| `forge.ai.workflow.trigger` | string | What started the run: a trigger's kind, `manual` for a person, `spawn` for a sub-agent. |
| `forge.ai.run.id` | string | The run. Stable across continue-as-new and a resume. A spawned sub-agent is a run of its own. The run explorer groups by this, not by trace id. |
| `forge.ai.root_run_id` | string | The top of a spawn chain. Equals `forge.ai.run.id` on a top-level run. |
| `forge.ai.parent_run_id` | string | The run that spawned this one; set on a spawned run's spans only. |
| `forge.ai.node.id` | string | The workflow node the work runs in. |
| `forge.ai.node.type` | string | The node's type (`call_llm`, `execute_tools`, `run`, ...). |
| `forge.ai.node.iteration` | int | The enclosing loop's iteration, 0 outside a loop. Set whenever a node is. |

`GenAIAttributes()` adds:

| Attribute | Type | Meaning |
|---|---|---|
| `gen_ai.conversation.id` | string | The chat id. It is the join key for per-chat trace reads. |
| `gen_ai.agent.id` | string | The agent: the thread whose turns it takes. |
| `gen_ai.agent.name` | string | The agent's preset, else its workflow's name. |

The run span and the node span use `SpanAttributes()` plus
`gen_ai.conversation.id`, not `GenAIAttributes()`: neither is an agent, so
neither names `gen_ai.agent.*` (details in each section). New spans use
`GenAIAttributes()`.

## Namespaces

Four namespaces, and a name belongs to the first that fits.

**`gen_ai.*`** is the OpenTelemetry GenAI semantic convention, used as written.
`gen_ai.conversation.id` is the chat id. Reliant invents nothing under this
prefix. Where the convention has a name, use it.

**`forge.ai.*`** is what the convention does not cover yet: the workflow, its
nodes, the run, the tool's location and the cost. These are forge's names:
reliant's constants are aliases of forge's `pkg/observe/genai` ones, and forge's
built-in AI dashboards read them. Reuse them; never duplicate one under a Reliant name (no
`reliant.workflow.*` for what `forge.ai.workflow.*` already says). Reliant adds no
new `forge.ai.*` name on its own: that namespace is forge's to extend.

**`reliant.*`** is Reliant's own, one sub-namespace per concern:

| Prefix | Owner file | What it names |
|---|---|---|
| `reliant.chat.*` | `run_scope.go` | The chat. Today only `reliant.chat.id`. |
| `reliant.run.*` | `run_exec_attrs.go` | A run node's command (`run.exec`). |
| `reliant.gateway.*` | `gateway_attrs.go` | A model call routed through Reliant's LLM gateway. Today only `reliant.gateway.call_id`. |
| `reliant.integration.*` | `integration_attrs.go` (#3286) | An integration call. |
| `reliant.trigger.*` | `trigger_attrs.go` (#3289) | A trigger firing, and the join from a run back to it. |

`reliant.cost_usd` and `reliant.model_id` and `reliant.tool_calls` on the `chat`
span are older, flat `reliant.*` names, kept as they are. forge reads
`reliant.cost_usd` (`genai.AttrCostUSDReliant`) but never emits it, so Reliant
adds it itself beside `forge.ai.cost.usd`.

**Other OpenTelemetry conventions** are used when one fits and the value is safe:
`error.type`, `process.exit.code`, `http.response.status_code`, `server.address`,
`user.id`, `org.id`. A name that is not Reliant's (`org.id`, `user.id`,
`thread.id`, `workflow.id`) was chosen to match the product-event contract, so
the same key joins spans, logs and product analytics.

Everything above is a public contract once released: never rename an attribute
in place, and never change what a value means. Add the new name beside the old
one, and keep emitting both while anything reads the old one (as
`forge.ai.cost.usd` and `reliant.cost_usd` are).

## The rules

These are the agreed rules every span and attribute follows. They exist because
the same spans are meant to be exported to an org's own destination later, where
a mistake is a disclosure. Every span Reliant opens in the tree follows them;
[Known gaps](#known-gaps) lists the spans it does not open, and the few cases
that still miss `org.id`.

1. **`org.id` on every span.** Tenant export is fail-closed on it: a span with no
   `org.id` is dropped from every tenant destination. A new span creation site
   stamps it, from the run scope, or, when it runs outside any run, from the
   thing that owns the work (`trigger.fire` takes it from the trigger row; a
   listing in `inputoptions` takes it from its caller).
2. **Opaque ids only.** Ids of chats, runs, nodes, triggers, events, connections,
   orgs and users. Never an email, a login, a name, a display title, or a
   repository or branch name. `forge.ai.workflow.name` is the one name allowed:
   it is a workflow's file name, which a dashboard groups by.
3. **No bodies, no command text, no params, no error text.** A span names what
   happened and how it ended; it never carries what was sent or received. No
   prompt or completion, no tool arguments or results, no shell command, no
   request or response body, no URL (a *host* is allowed: `server.address`), no
   headers (the provider's rate-limit headers are the one exception, as numbers),
   and no exception message. When a value would be derived from content, a
   hash, a length or a closed-vocabulary class is the most that goes on a span:
   `reliant.run.command.sha256` (first 16 bytes, hex), `reliant.run.command.bytes`,
   `reliant.run.command.program` (an allowlisted program name),
   `reliant.trigger.dedupe_key_hash`.
4. **The status description is a code, never text.** A failed span's
   `Status{Error, description}` description is a short machine code (`timeout`,
   `http_404`, `machine_unavailable (<transport code>)`, `failed`, `error`),
   the same value as `error.type` or the outcome. Do not call `RecordError`: it
   records the exception's message as an event, and a provider or a machine can
   put anything in one. Every span in the tree follows this; the AI spans take
   their codes from [one vocabulary](#errortype-on-the-ai-spans). The error's
   text is not lost: the code that returned it logs it to the staff logs, where
   the trace and run ids join it to the span. A cancellation is the caller
   hanging up, not a failure: it is `error.type=canceled` with *no* error status.
5. **A closed enum, not free text, for a reason.** When a span needs to say *why*,
   it names one value of a closed vocabulary that lives beside the attribute as
   constants. The model case is `reliant.trigger.reason`: the trigger_events row
   has an `outcome_detail` (free text that can carry an error, a filter
   expression, a workflow or model name), and the span carries a one-word class of
   it (`filter_no_match`, `capacity_waiting`, ...) instead. A detail the
   vocabulary does not know is `other`, never its text. The detail stays on the
   row the span's `reliant.trigger.event_id` names. Add a value beside the others;
   never rename one.
6. **Failure to run is the span's error; a verdict is not.** A tool that ran and
   reported an error is `error.type=tool_error` with the status untouched. A
   command that ran and exited nonzero is `error.type=nonzero_exit`, status
   untouched. A provider that answered with a 404 is an `integration.call` with
   an error status, because the *call* failed. The line is "did the step do what
   it was asked, whatever the answer was": only a failure to do it sets the error
   status.
7. **Keep viewer-safe facts out of `reliant.*` alone.** The analytics design
   (`design/agent-analytics.md`, still a draft) has a shared chat's viewer read
   stubbed spans: kind, name, timing, status, tool name, model and token counts,
   with every `reliant.*` attribute dropped. A fact a viewer is meant to see
   belongs under `gen_ai.*` or `forge.ai.*`, not only under `reliant.*`.

## `error.type` on the AI spans

`workflow.node`, `invoke_agent`, `execute_tool` and `chat` end a failure with one
value of a closed vocabulary (`internal/telemetry/ai_error_type.go`), as
`error.type` and as the status description alike. They never call `RecordError`
and never describe a status with an error's text. A value outside the vocabulary
is written as `error`.

| Value | Means | Set on |
|---|---|---|
| `canceled` | The caller hung up (stop, a newer turn). No error status. | all four |
| `timeout` | A deadline ran out. | all four |
| `tool_error` | The tool ran and reported an error. Status untouched. | `execute_tool` |
| `context_overflow` | The request exceeded the model's context window. | `chat`, `invoke_agent` |
| `credit_exhausted` | The provider's quota, credit or billing is exhausted. | `chat`, `invoke_agent` |
| `prompt_refused` | The provider refused the prompt under its usage policy. | `chat`, `invoke_agent` |
| `stream_stalled` | The stream sent only keepalives past its content-stall deadline. | `chat`, `invoke_agent` |
| `stream_ended_early` | The stream closed before its terminal event. | `chat`, `invoke_agent` |
| `malformed_output` | The model's response could not be read. | `chat`, `invoke_agent` |
| `empty_input` | The request had nothing to send. | `chat`, `invoke_agent` |
| `no_provider` | No provider could serve the model. | `chat`, `invoke_agent` |
| `http_<status>` | The provider answered with an HTTP status of 400-599 (`http_429`, `http_529`). | `chat`, `invoke_agent` |
| `error` | Any other failure. | all four |

The provider classes are decided by `llm.ErrorType`, most specific first (a
context overflow answered with a 400 is `context_overflow`, not `http_400`). The
node span classes only by the error's type (`canceled`, `timeout`, `error`): its
`call_llm` node's turn and calls below it carry the provider class. A failed
provider call is logged at WARN (`LLM provider call failed`, with the error, its
class, the provider and the model) on the call's context; a failed activity, and
with it its node and turn, is logged by the Temporal worker.

## The run: `RunWorkflow:DynamicWorkflow`

Opened by forge's temporalotel interceptor as the workflow starts, on every
execution, replays included (only the last is exported). Reliant does not open
it; it names it. `startRunTelemetry` (`internal/workflow/runtime/run_telemetry.go`)
sets attributes on it, which is replay-safe because an attribute is not a command.

| | |
|---|---|
| Name | `RunWorkflow:DynamicWorkflow`, from Temporal; the workflow's own name is `forge.ai.workflow.name`. |
| Kind | Set by the interceptor, not by Reliant. |
| Parent | None for a trigger-launched run (a new root). A person's start is a child of the request that made it. |
| Links | One, to the `trigger.fire` span that launched it, for a trigger-launched run. None otherwise. |

Attributes: `gen_ai.operation.name` = `invoke_workflow`, the run scope's
`SpanAttributes()` with the fields the workflow knows (`reliant.chat.id`,
`thread.id`, `workflow.id`, `forge.ai.workflow.*`, `forge.ai.run.id`,
`forge.ai.root_run_id`), `gen_ai.conversation.id`,
`reliant.trigger.event_id` (#3289, pending merge) when a trigger launched it, and
`org.id`.

`org.id` is set once the run's root definition load returns. Workflow code
cannot read the database, so the worker's `ActivityLoadWorkflow` reports the
org of the run's chat in its result (`org_id`), from the same chat-owner lookup
that stamps every other span of the run, and `nameRunOrg` sets it on the span.
The result is in the run's history, so a replay sets the same value; a run whose
history predates the field, or whose chat has no org, names none. A run that
fails before its definition loads has no `org.id`. The span has no `user.id`.

Status: the interceptor's. Reliant sets none.

## `workflow.node <node id>`

One execution of one workflow node. It is opened by the run-scope activity
interceptor for any activity whose input names a node, and ended with the
activity's result.

| | |
|---|---|
| Name | `workflow.node <forge.ai.node.id>` |
| Kind | Internal |
| Parent | The run span; in a spawned sub-agent, the sub-agent's. |
| Attributes | The run scope's `SpanAttributes()`, and `gen_ai.conversation.id` (the chat, beside `reliant.chat.id`) so a per-chat read keyed on the conversation finds the node itself. No `gen_ai.agent.*`: a node is not an agent. |
| Status | Error when the activity fails, described by its [class](#errortype-on-the-ai-spans) (`timeout`, `error`); a cancellation is `error.type=canceled`, no status. |

## `invoke_agent <agent>`

One agent turn. Its LLM calls are its children (opened under its context) and, by
`TraceParent`, so are its tool calls. Opened by `telemetry.StartAgentTurn` in
`call_llm`.

| | |
|---|---|
| Name | `invoke_agent <gen_ai.agent.name>`, or `invoke_agent` when the agent has no name. |
| Kind | Internal |
| Parent | `workflow.node` of the `call_llm` node. |
| Attributes | `gen_ai.operation.name`=`invoke_agent` and `GenAIAttributes()`. |
| Status | Error on a failed turn, described by its provider [class](#errortype-on-the-ai-spans) (`llm.ErrorType`); a cancellation is `error.type=canceled`, no status. |

## `chat <model>`

One provider call (`internal/llm/genai_span.go`), opened by the base driver. The
driver's HTTP spans are its children.

| | |
|---|---|
| Name | `chat <requested model>` |
| Kind | Client |
| Parent | `invoke_agent`. |
| Status | Error on a provider failure, described by its [class](#errortype-on-the-ai-spans) (`context_overflow`, `http_529`, ...), which is also `error.type`; a cancellation is `error.type=canceled`, no status. |

| Attribute | Type | Meaning |
|---|---|---|
| `gen_ai.operation.name` | string | `chat` |
| `gen_ai.provider.name` | string | The driver family: `anthropic`, `openai`, `codex`, ... |
| `gen_ai.request.model` | string | The model asked for. |
| `gen_ai.request.stream` | bool | Whether the call streamed. Always set: forge's `StartChat` records only `true`, and Reliant adds `false`. |
| `reliant.model_id` | string | Reliant's own model id, when it has one. |
| `GenAIAttributes()` | | The run scope, as above. |
| `gen_ai.response.model` | string | The model that served it (the requested one when the provider did not say). |
| `gen_ai.response.id` | string | The provider's request id. |
| `reliant.gateway.call_id` | string | The LLM gateway's id for the call: LiteLLM's `x-litellm-call-id`, which control-plane's wallet ledger stores as the call's `provider_request_id`. Set only by the `reliant` driver, on every call the gateway answered, failed and streamed ones included; after retries it is the last answered attempt's. Never set on any other provider's call. It is the exact join to the gateway's billed cost; `gen_ai.response.id` is the upstream provider's id, a different value (`dev-docs/design/agent-analytics.md` §3.2, §4). |
| `gen_ai.response.finish_reasons` | string[] | One value. |
| `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | int | Token counts. |
| `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_creation.input_tokens`, `gen_ai.usage.reasoning.output_tokens` | int | Set only when non-zero. |
| `forge.ai.cost.usd`, `reliant.cost_usd` | float | The cost the provider or gateway reported, never an estimate. Both names are emitted. |
| `reliant.tool_calls` | int | How many tool calls the response asked for. |
| `gen_ai.retries` | int | Backoff waits, when there were any. |

Span events: `gen_ai.first_token` (once per call, retries included: the span's
start to it is the time to first token) and `gen_ai.retry_wait` with `attempt`,
`http.response.status_code`, `reason` (a short code like `http_529`) and
`delay_ms`. A retry with no HTTP status (`reason=connection_error`) has no
`http.response.status_code`; before forge's `genai.RetryWait` it said `0`, and
readers treat the two alike.

## `execute_tool <tool>`

One tool call the turn asked for.

| | |
|---|---|
| Name | `execute_tool <gen_ai.tool.name>` |
| Kind | Internal |
| Parent | The `invoke_agent` of the turn that asked, when its traceparent names a span of this trace; otherwise the current span, with a link to the turn. A link to the `execute_tools` node's span when it is parented by the turn. |
| Status | Error only for a failure to run the tool at all, described by its [class](#errortype-on-the-ai-spans). A tool that ran and reported an error is `error.type=tool_error`, status untouched. A cancellation is `error.type=canceled`, no status. |

| Attribute | Type | Meaning |
|---|---|---|
| `gen_ai.operation.name` | string | `execute_tool` |
| `gen_ai.tool.name` | string | The tool. |
| `gen_ai.tool.type` | string | `function` |
| `gen_ai.tool.call.id` | string | The provider's call id, when there is one. |
| `forge.ai.tool.location` | string | `daemon` (the user's machine, through the gateway) or `local` (the worker). |
| `GenAIAttributes()` | | The run scope. |

The tool's arguments and result are never attributes.

## `run.exec <node id>`

A run node's command, from its send to its verdict (#3285, merged). One per
attempt.

| | |
|---|---|
| Name | `run.exec <forge.ai.node.id>` |
| Kind | Internal |
| Parent | `workflow.node` of the run node. The gateway's round trip to the machine is its child. |
| Attributes | `GenAIAttributes()` (so `org.id` and `gen_ai.conversation.id`), then the table below. |

| Attribute | Type | Meaning |
|---|---|---|
| `reliant.run.command.sha256` | string | First 16 bytes, hex, of the SHA-256 of the command as the step sent it (expressions evaluated). The same command hashes the same on every run. |
| `reliant.run.command.bytes` | int | The command's length in bytes. |
| `reliant.run.command.program` | string | Base name of the command's first word (`bash`, `rc.sh`). Left out unless that word is a plain path, so a variable assignment, an expansion or a quoted word never produces one. |
| `reliant.run.attempt` | int | The activity attempt, 1 first. |
| `reliant.run.credentials` | string | The node's credentials mode: `inherit` or `none`. |
| `reliant.run.worktree` | bool | The command ran in a branch workspace's checkout. |
| `reliant.run.outcome` | string | How it ended; values below. |
| `reliant.run.duration_ms` | int | The span's own wall time. |
| `reliant.run.interrupted` | bool | The command was interrupted. |
| `reliant.run.truncated` | bool | The machine cut the output to its cap. |
| `reliant.run.reattached` | bool | The verdict came from reattaching to a command already sent: by a retry to its previous attempt's, or by this attempt after its transport dropped. |
| `reliant.run.transport.code` | string | The transport failure's code, when one ended the command. |
| `process.exit.code` | int | The exit status, only when the command returned one (a transport failure's synthetic value is not one). |
| `error.type` | string | Equal to the outcome, for every outcome but `ok`. |

`reliant.run.outcome` values: `ok`, `nonzero_exit`, `timeout`,
`machine_unavailable`, `transport_error`, `released` (the attempt's worker
stopped while the command ran; the retry reattaches), `canceled`,
`retry_refused` (a retry that could not reattach, and refused to run the command
again), `error`.

Status: Error only when the command could not be run at all: `machine_unavailable`,
`transport_error`, `retry_refused`, `error`; described by the outcome, plus
` (<transport code>)` when there is one. `nonzero_exit` and `timeout` are the
command's verdict and leave the status alone. The error itself is not recorded.

## `integration.call <integration>.<operation>` (#3286, pending merge)

One call of an integration action: `github.pr.merge`, `http.request`. The span
sits at the runner (`internal/integrations/httpaction/span.go`), so an action
node, an agent's integration tool, a workflow check and a picker listing its
options all get it from one place. A hosted call goes around the runner and opens
its own at the action activity.

| | |
|---|---|
| Name | `integration.call <manifest id>.<action id>` |
| Kind | Client |
| Parent | `workflow.node` (an action node) or `execute_tool` (an agent's integration tool). A call made outside any run (an input-options listing) is a child of whatever span its caller holds, such as the request's. |
| Attributes | `GenAIAttributes()`, then the table below. |
| Status | Error for every outcome but `ok` and `canceled`, described by the error code, or the outcome when there is no code. `canceled` is `error.type=canceled`, no status. No `RecordError`: error text can carry what the provider echoed back. |

Outside a run, the scope is what the caller put in it: the user, and the org when
the caller knows one. A hosted call resolves the org of its run when the worker's
scope names none.

| Attribute | Type | Meaning |
|---|---|---|
| `reliant.integration.id` | string | The manifest id (`github`, `http`). |
| `reliant.integration.operation` | string | The action id (`pr.merge`, `request`). |
| `reliant.integration.uses` | string | The reference both come from (`github/pr.merge@1`); absent for a manifest with no version. |
| `reliant.integration.outcome` | string | `ok`, `error` (the provider answered with a failure), `credential_error` (no credential could be had, nothing sent), `refused` (a policy or grant refused it before it was sent), `failed` (could not be made or completed: invalid params, a timeout, a network failure), `canceled`. |
| `reliant.integration.error_code` | string | The failure's code: a credential error's code, `http_404`, `provider_error` (a failure reported in a 2xx, or with no status), `timeout`, `request_failed`. Also written as `error.type`. |
| `reliant.integration.retryable` | bool | A failed call's retry verdict, from the action's error rules or the 429/5xx default. |
| `reliant.integration.connection.id` | string | The saved connection the call used: an id, never the credential. |
| `reliant.integration.pages` | int | How many responses it read (a paginated walk or a fan-out). |
| `reliant.integration.truncated` | bool | The walk stopped at its page limit with more to read. |
| `reliant.integration.hosted` | bool | A `hosted_use` call, made by the controller with a granted credential. |
| `http.response.status_code` | int | The last response's status. |
| `server.address` | string | The host the last response came from. A host, never a URL. |

The provider's rate limit, from the last response's standard headers
(`X-RateLimit-*` or `RateLimit-*`, and `Retry-After`). Each attribute is set only
when the provider sent it. A hosted call carries none: the controller returns no
response headers.

| Attribute | Type | Meaning |
|---|---|---|
| `reliant.integration.rate_limit.limit` | int | The window's limit. |
| `reliant.integration.rate_limit.remaining` | int | What remains. |
| `reliant.integration.rate_limit.used` | int | What is used. |
| `reliant.integration.rate_limit.resource` | string | Which budget (GitHub's `core`, `graphql`, `search`); only a short token is kept. |
| `reliant.integration.rate_limit.reset` | int | When it resets, unix seconds. A relative reset is converted. |
| `reliant.integration.rate_limit.retry_after` | int | Seconds the provider asked to wait. |

No params, no body, no URL, no other header is ever an attribute.

## `trigger.fire <event kind>` (#3289, pending merge)

One firing attempt of a trigger. Every attempt that writes or settles a
`trigger_events` row opens one, so a trigger that stopped producing runs can be
read on a timeline: launched, skipped, failed or still pending, and why. A skip
used to leave no span at all. It covers a schedule's fire, an inbound event's
fire, an event settled at intake, a workflow event's dispatch, and the redriver's
and capacity queue's settles of events nobody is firing.

| | |
|---|---|
| Name | `trigger.fire <event kind>`: `schedule`, `webhook`, `integration` or `workflow_event`. |
| Kind | Internal |
| Parent | None. It is opened with a new root and **is the root of a trace of its own**. |
| Links | One, to the span that caused the attempt (the fire activity, a webhook request, a run event's dispatch), when there is one. |
| Status | Error when the event was settled `failed` (description `failed`), or when the attempt ended in an error before recording any outcome (description `error`, or `deadline_exceeded`), which is retried. A skip or a wait is not an error. A cancellation is `error.type=canceled`, no status. The description is a code, never the error. |

A firing runs outside any run scope, so its `org.id` and `user.id` are those of the
**trigger** row, set at the end of the attempt. A trigger that could not be read
(deleted, or not loaded) leaves the span without them; see
[Known gaps](#known-gaps).

| Attribute | Type | Meaning |
|---|---|---|
| `reliant.trigger.event_kind` | string | The kind of the `trigger_events` row. The span is named by it. |
| `reliant.trigger.id` | string | The trigger. |
| `reliant.trigger.kind` | string | The trigger's kind. |
| `reliant.trigger.workflow_ref` | string | The workflow the trigger runs. |
| `reliant.trigger.activation_id`, `reliant.trigger.activation_name` | string | When the trigger activates a workflow's declared trigger: the activation row and the declared trigger's name. |
| `reliant.trigger.event_id` | string | The `trigger_events` row it wrote or settled. A run the firing launched carries it on its workflow span, so the two join by attribute as well as by link. |
| `reliant.trigger.dedupe_key_hash` | string | First 16 hex digits of the SHA-256 of the event's dedupe key: enough to join, never the key. |
| `reliant.trigger.origin_run_id` | string | The chat of the run whose outcome, or whose write, caused the event, when Reliant caused it and it is the owner's to see. |
| `reliant.trigger.outcome` | string | `launched`, `skipped`, `failed`, `pending`. Unset when the attempt recorded none. |
| `reliant.trigger.reason` | string | Why, as one value of the closed enum below. |
| `reliant.trigger.resumed` | bool | The attempt continued an event already launched (its chat committed, its start possibly not). Set only when true. |
| `org.id`, `user.id` | string | From the trigger row. |
| `gen_ai.conversation.id`, `forge.ai.run.id` | string | Only when launched: the chat and the run it started. |

`reliant.trigger.reason` is **not** `outcome_detail`. Values (a detail that matches
none is `other`):

| Value | Means |
|---|---|
| `trigger_deleted`, `trigger_paused` | The trigger is gone or disabled (an org policy, a creator who left, no org account, a deleted project may all pause it). |
| `creator_left_org`, `no_org_account` | The creator left the org; no org account serves the run. |
| `filter_no_match`, `filter_error` | The trigger's `filter` rejected the event or failed to evaluate. |
| `origin_filter_rejected`, `origin_filter_error` | The same for `origin_filter`. |
| `loop_guard` | A loop guard refused a run caused by Reliant. |
| `needs_setup`, `definition_broken`, `config_invalid`, `daemon_missing` | The workflow or its target is not runnable yet. |
| `capacity_waiting`, `capacity_superseded`, `capacity_policy_removed`, `expired` | The capacity queue held, replaced, or released the event. |
| `pool_waiting` | Waiting for a pooled machine. |
| `already_launched` | A duplicate: the event was already launched. |
| `other` | Anything the vocabulary does not know. |

## Trigger-launched runs and the link

How the pieces join, for anyone reading a firing's outcome off a trace:

1. The `trigger.fire` span of an attempt is the current span while the launch
   happens inside it.
2. `launch.Launch` records its caller's span as `Spec.FiringTraceParent` before it
   opens its own span. A caller that launches away from its firing (a redrive of a
   parked event) names it explicitly.
3. For an unattended kind, the Temporal start is issued from a context with an
   empty span context and no baggage, so the interceptor opens a new root, and the
   trigger passed to the workflow carries the firing's traceparent
   (`TriggerInfo.TraceParent`, telemetry only, never in the CEL value).
4. `startRunTelemetry` turns it into a span link on the run's span (merged,
   #3283), and, with #3289, sets `reliant.trigger.event_id` on it.

A malformed traceparent is no link. An attended launch is unchanged.

## How to add an attribute or a span

1. **Pick the namespace by the table above.** `gen_ai.*` if the convention has it;
   `forge.ai.*` only if forge declares it; otherwise `reliant.<concern>.*`. Do not
   restate in `reliant.*` what `forge.ai.*` or `gen_ai.*` already says.
2. **Put the constants in a new file of their own**, named for the concern:
   `internal/telemetry/<concern>_attrs.go` (the span name, the attribute names and
   the closed vocabularies of its values), with the span helper in
   `<concern>_spans.go`. Do not add to the `const` block in `ai_attrs.go`: every
   span family that grows there conflicts with the next one. `ai_attrs.go` holds
   only names shared by every span. `run_exec_attrs.go` is the pattern.
3. **Open the span from the scope.** Start it with
   `RunScopeFromContext(ctx).GenAIAttributes()`, never `SpanAttributes()` alone,
   so it carries `org.id` and `gen_ai.conversation.id`. Outside a run, put the
   org on the context (`telemetry.WithRunScope`) or on the span from the thing
   that owns the work.
4. **Apply the [rules](#the-rules).** In particular: a value derived from content
   is a hash, a length or a closed class; a failure's description is a code; no
   `RecordError`.
5. **Test it.** A test that opens the span and asserts the full attribute set,
   that `org.id` is present, and that a representative secret (a command, a body,
   an error message) is in none of the attributes or the status description.
   `run_exec_span_test.go` is the pattern.
6. **List it here**, in the table of its span, in the same PR, with its type and
   meaning. A name that is in code and not here, or the reverse, is a defect. A
   PR that adds one while another PR is open against the same table marks it
   `(#NNNN, pending merge)` until it merges.
7. **Additive only.** The names are a public contract once released (semver, major
   1). New names beside old; never rename or repurpose.

Code outside `internal/telemetry` does not spell an attribute name. It uses the
constant.

## What tenant export will rely on

Phase 1 is staff-only: spans reach Reliant's own ClickStack and nothing leaves
Reliant. The design for exporting a tenant's own spans to the org's forge obs
project, or to an endpoint the org configures, is in
[`design/agent-analytics.md`](design/agent-analytics.md), section 6, and is
awaiting the owner's decision. What it will rely on, and so what this reference
must keep true:

- **`org.id` on every span.** The router sends a span to the destination of the
  org the span names. It is **fail-closed**: a span with no `org.id` is dropped
  from every tenant destination and counted (`spans_dropped_no_org`); it is never
  delivered to a default. Staff keep their own copy.
- **Every trace holds one org.** Since #3283 a launched run is its own trace, so
  a trace never mixes orgs across a trigger fan-out. Routing is by *span*
  `org.id`, which stays safe regardless, but a tree with another org's spans
  removed is a tree with missing parents.
- **Opaque ids and no content.** What is exported is what is on the span; the rules
  above are the privacy statement. No prompt, argument, body, command or error
  text is ever on one, so tenant export needs no scrubbing pass to make that
  true. Whether content may ever be exported, for example behind an org-admin
  opt-in, is an open owner decision; the default and this reference are that it
  is not.
- **`gen_ai.conversation.id` and `org.id` together** are the key per-chat reads
  use: a read filters on both, and a span with no `org.id` is treated as not the
  chat's.
- **Org coverage is audited**, with a test that opens each kind of span and asserts
  `org.id`: `TestGenAISpanTree_EveryAISpanCarriesOrgID`
  (`internal/llm/genai_span_tree_test.go`) for the node, agent turn, model call
  and tool call, failed and cancelled ones included, and
  `internal/workflow/runtime/run_telemetry_test.go` for the run span. The gaps
  below are what such an audit finds today.

## Known gaps

Where the code today does not yet meet the rules. They are listed so a reader
does not mistake the reference for a guarantee. None is changed by this document.

- **Spans other libraries open have no `org.id`**: Temporal's `StartWorkflow`,
  `StartActivity` and `RunActivity`, and `otelhttp`'s `HTTP POST` (under `chat`
  and the gateway round trip). Reliant does not open them, so no creation site
  can stamp them. Dropping them is not harmless: `RunActivity` is the parent of
  every `workflow.node`, so a tenant tree without it has every node orphaned
  from its run. The recommended treatment, for the export work to build: a span
  processor on the exporting process stamps `org.id` at `OnStart` from the
  context the span is started under (the run scope, else W3C baggage the
  starter put there), never from anything ambient, and the fail-closed filter
  drops what it still cannot name. `HTTP POST` starts under a context that
  already holds the run scope. `RunActivity` does not yet: the tracing
  interceptor opens it before the run-scope interceptor binds the scope, so the
  scope has to be bound first (an outer interceptor that only binds it), or
  the org carried in baggage from the workflow. `StartWorkflow` starts in the
  request that launched the run, whose org the launcher knows.
- **A few Reliant spans can still lack `org.id`**: a run span whose run fails
  before its definition loads, or whose history predates `org_id` in that
  load's result; a `trigger.fire` span whose trigger could not be read; an
  `integration.call` outside a run whose caller knew no org. The fail-closed
  filter drops them.
- **`toolexec.ToolRequest.Environment` never reaches a daemon**; only the shell
  tool's `env` input does. Not a telemetry defect, but it bounds what a run node
  can be given to make a command observable.
- **Gmail executors** are not covered by the rate-limit attributes on
  `integration.call`; `go:` executors that make `Do` sub-requests (the GitHub
  `pr.merge` GraphQL call) are.
