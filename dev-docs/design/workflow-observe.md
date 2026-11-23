# Declaring observability on workflow nodes: attributes, events, metrics

Status: **draft for owner review.** The direction is the owner's (2026-11-22): "I
don't like the CLI based mechanism ... instead i'd imagine it might make more
sense to instrument the yaml workflows themselves or adjust the schema to opt-in
to per node observability", then "make it more generically part of observability
in general, and then users can fill in types or attributes as they want, although
maybe we recommend the word 'decision' ... also non-llm like run nodes and other
nodes can have it. Any node." Everything else here is a design for the owner to
accept or change. Section 12 (the model recording its own observability) ends in
a recommendation that is **not yet decided**.

Code references are `file:line` at **reliant `e254d81ee`** (`origin/main`, paths
relative to the reliant repo unless they start with `control-plane/`). The
pipeline workflows quoted in section 13 are control-plane's, read at its
`origin/main`.

## 1. The proposal in one page

Every workflow node, of any kind, may carry an `observe:` block, and the workflow
itself may carry one that supplies defaults. It has three parts, all optional:

```yaml
- id: plan
  type: compute
  outputs: { ... }
  observe:
    attributes:                       # set on the node's span, key -> template
      trigger: "{{ output.trigger }}"
    events:                           # span events; the author picks the names
      - name: decision                # "decision" is the recommended convention
        attributes:
          name: cut
          value: "{{ output.cut ? 'cut' : 'no_cut' }}"
          reason: "{{ output.hold }}"
    metrics:                          # OTEL metrics, cardinality-capped
      - name: rc_train.ticks
        kind: counter
        attributes: { decision: "{{ output.cut ? 'cut' : 'no_cut' }}" }
```

The runtime evaluates the block **after the node finishes**, worker-side, over the
node's own result, and does three things with what it gets: it stores the
attributes and events durably (a new `node_observations` table), it puts them on
the node's `workflow.node` span as attributes and span events and emits the
metrics, and it returns them in both run read paths so the web run view and
`reliant workflow status` show them. An evaluation error, a size cap, a slow
expression or a failed write never fails, retries or delays the node.

"Decision" is a convention, not a schema element. A decision is an event named
`decision` whose attributes are `name`, `value` and `reason`; the runtime knows
nothing about it, the dashboards and the run view recognise it, and rc-train,
pr-review, pr-fix and hotpath use it in section 13. The GitHub API cost per node
needs no YAML at all: it is already on the `integration.call` spans (#3286). The
same block is also where a model-recorded event lands, if the owner wants the
model to record observability itself (section 12).

Four things in this design go beyond the owner's stated shape, and each can be
cut: an optional `when:` and `each:` on events and metrics (section 3.2), a `node`
namespace in the expression scope (3.3), a rule that **only code-shaped strings
leave the tenant's own storage as telemetry** (8.2), and a new table rather than
a column (5).

## 2. What exists today

**The schema.** A workflow is `reliantv1.Workflow` (`proto/reliant/v1/workflow_v2.proto:2277`,
last field `check = 26` at :2394). A node is `message Node` (:256). Its base
fields are 1, 2, 3, 5, 6, 7, 8 and 9, with the `args` oneof (:317) at 10-28 and
`secret_refs = 40` (:315). No tag from 29 up to 39 is used.
Node kinds are the `NodeType*` constants (`internal/workflow/model/constants.go:7-23`):
`ask_question`, `call_llm`, `execute_tools`, `compact`, `approval`,
`save_message`, `create_worktree`, `run`, `workflow`, `loop`, `join`, `router`,
`invoke_tool`, `action`, `compute`, `git_publish`. An `observe` field on `Node`
covers all of them with no per-kind work in the schema.

**The parser is strict, and that matters for compatibility.**
`wfyaml.ParseWorkflow` (`internal/workflow/yaml/parser.go:32`) rejects any
workflow key it does not know (`:161-162`, `unknownField`) and any node key it
does not know (`nodes.go:212` for structural kinds, `nodes.go:200` for activity
kinds). The error names the build and says the file may have been written for a
newer server (`withVersionSkewHint`, `unknown_field.go:34`, applied at
`parser.go:34`). A node key is recognised in three hand-kept places, and a new
key must be added to all of them:

1. the node switch in `unmarshalNode` (`nodes.go:24`, `case yamlKeyTolerateFailure`
   at :66) and its mirror in `marshalNode` (`nodes.go:926`);
2. `generatedNodeBaseFieldKeys` (`bindings_generated.go:28`), which the generator
   `internal/workflow/yaml/cmd/yamlbindingsgen/main.go:82` fills from a list of
   eight field names written in the generator;
3. the allow-list in `validateActivityNodeTopLevelKeys` (`nodes.go:299-313`),
   **which does not include `tolerate_failure`**. I probed it: `tolerate_failure: true`
   parses on a `run` or `compute` node and is refused as an unknown key on an
   `action`, `call_llm` or `execute_tools` node. That is an existing defect (the
   proto and `docs/reference/nodes.mdx:30` say any node); reported to engineering with this
   PR. It is also exactly how `observe:` would be accepted on "any node" in the proto and
   refused on half the kinds in the YAML, so the implementation PR must test every
   kind.

**The validator** is `internal/workflow/validation`. Its entry is
`staticAnalysisOne` (`validator.go:133`). It builds one typed CEL environment per
*site*, from the same context type the runtime uses there (`cel_sites.go:58`,
`siteNamespaces`). `save_message` is the precedent for a block whose expressions
read a node's result: it uses `PostActivityContext` (`cel/types.go:229`:
`output`, `inputs`, `workflow`, `iter`, plus `trigger` and `failure` derived from
inputs) and is checked by `validateSaveMessage` (`cel_sites.go:515`) with
`output` typed as the node's own result. `load_workflow.go:109` runs this
validation when a run loads its definition, and **an error blocks the run**
(`:110-112`); a warning (`result.go:131`, `AddWarning`) does not.

**The linter** is `internal/workflow/lint`: `Check` (`lint.go:170`), the rule table
(`lint.go:115`), per-node rules in `checkGraph` (`rules.go:21`), a read index over
every expression (`graph.go:98`, `newReadIndex`) and the `lint:allow <rule> -
<reason>` comment (`allow.go:21`). Lint runs only where a definition is written
and reviewed, "never at save or start: a rule added in a later release must not
make a stored automation refuse to fire" (`lint.go:12-14`). A warning never
blocks (`SeverityWarning`).

**The runtime.** A node that dispatches an activity runs inside `ActivityWrapper`
(`internal/workflow/runtime/registry.go:721`). After the activity returns, the
wrapper writes the node's `save_message` from the result (`:1215`), clears
message-only fields (`:1232`), and writes the `step_executions` row best-effort
with a detached context and a 5-second budget (`writeStepExecution` :2032,
`bestEffortWriteTimeout` :2017, `CreateStepExecution` :2055). The run-scope
activity interceptor opens the node's span around all of it
(`internal/observability/run_scope.go:66`, `telemetry.StartNodeSpan`
`internal/telemetry/ai_spans.go:42`), so inside the wrapper
`trace.SpanFromContext(ctx)` **is** the `workflow.node` span.

Two facts shape the design:

- **`save_message` is the precedent for "evaluate after the node, in the worker".**
  The workflow attaches a `types.SaveMessageRequest` to the activity input
  (`step_executor.go:1650`, `activities/types/save_message_request.go:25`), carrying
  the node's config verbatim, the `workflow` and `iter` contexts, and only the
  workflow inputs the expressions reference, because "the full inputs map is
  several KB and would re-bloat the history" (`:31-34`). For a run node it rides
  in the flat step input under `RunStepSaveMessageKey` (`:50`). The request is
  needed explicitly because `execute_tools` "rebuilds its node without
  save_message" (`:26-28`). `observe` must be carried the same way, by the same
  logic.
- **Nodes the workflow completes itself have no activity, no wrapper, no span and
  no row.** `compute` is the case that matters: `startCompute` resolves the
  outputs in workflow code and settles a future (`step_executor.go:1086-1096`; the
  comment on `computeActivityName` says it "is not a registered activity: nothing
  is dispatched"). Its completion is visible only through the structural observer
  (`structural_observer.go`, a no-op "in every production run"). Join, router,
  loop and workflow nodes are alike (`recordStructuralCompleted` is called from
  `step_executor.go:1094`, `loop_executor.go`, `inline_workflow_executor.go` and
  `router_executor.go`). **rc-train's `plan`, pr-review's `lane` and `verdict`,
  and pr-fix's `decide` are all `compute` nodes**, so the main use case is a node
  kind with nowhere to attach a record today.

**What the run read paths return.** `RunService.GetRun` (`internal/handlers/run/rpc_get_run.go:15`)
reads `ListRunNodeExecutions` (`internal/store/step_execution_repo_ext.go:164`, a
recursive CTE over `workflows.parent_id`, 200 rows, output bytes budgeted) and
returns `RunNodeExecution` (`proto/reliant/v1/run.proto:399`, last field 16) in
`GetRunResponse` (:390, last field 3), whose comment promises "Inputs and
arbitrary outputs are never included". The web run view renders it in
`frontends/web/src/components/runs/RunNodes.tsx:40` (`RunNodeList`).
`ChatService.GetWorkflowExecutions` (`internal/handlers/chat/rpc_get_workflow_executions.go:20`)
returns `WorkflowExecution` (`chat.proto:1723`, last field 21) carrying
`StepExecution` (:1680, last field 16); `reliant workflow status` reads it with
the FULL view (`sdk/go/reliant/status.go:129`, `statusOf`; `SummarizeSteps` :251,
`StatusStepGroup` :96). The two paths share no code.

## 3. The `observe:` block

### 3.1 Shape

```yaml
observe:
  attributes:           # map<string, template>: span attributes, one value each
    <key>: "{{ expr }}"
  events:               # list: span events
    - name: <event name>
      when: <CEL bool>            # optional; raw CEL like `condition:`
      each: "{{ list expr }}"     # optional; one event per element, `item` in scope
      attributes:
        <key>: "{{ expr }}"
  metrics:              # list: OTEL instruments
    - name: <metric name>
      kind: counter | histogram | gauge
      value: "{{ number expr }}"  # optional for a counter (default 1)
      unit: <UCUM unit>           # optional
      when: <CEL bool>            # optional
      attributes:
        <key>: "{{ expr }}"
```

Values are templates, `"{{ expr }}"`, the same as `outputs:` and `save_message`.
A pure `{{ expr }}` keeps its native type (I evaluated `{{ int(...) }}` through
`wfcel.EvaluateTemplate`, `cel/evaluate.go:47`, and got an integer back), so a
histogram value is a number and a flag is a bool; mixed text interpolates to a
string. `when:` is raw CEL because `condition:` is.

**Proto storage must not use the `Cel*` wrapper types.** Before a node is
dispatched the runtime resolves every `CelString`-family field in it against the
node-resolution context (`cel/resolver.go:48`, `ResolveCELFields`), where `output`
does not exist yet; `save_message` is exempted by a path rule (`resolver.go:136`).
`observe` is stored as plain `string` fields in a new message so the resolver
never sees it:

```proto
message NodeObserve {
  map<string, string> attributes = 1;
  repeated ObserveEvent events = 2;
  repeated ObserveMetric metrics = 3;
}
message ObserveEvent  { string name = 1; string when = 2; string each = 3; map<string, string> attributes = 4; }
message ObserveMetric { string name = 1; string kind = 2; string value = 3; string unit = 4; string when = 5; map<string, string> attributes = 6; }
// Node:     NodeObserve observe = 30;
// Workflow: NodeObserve observe = 27;
```

The tags are proposals; the PR confirms them against `buf breaking`.

### 3.2 `when:` and `each:`, the two additions

Without `when:`, a workflow-level default cannot say "only on `run` nodes", and a
decision event that should fire only on the no-cut path cannot be written. Without
`each:`, hotpath's sweep, which lands several PRs in one `run` node and prints one
`HOTPATH <repo>#<n> merged|queued|skip|failed <why>` line per PR
(`control-plane/scripts/pipeline/hotpath.sh:68`), cannot record one decision per
PR, because an `events:` list is static. `each:` evaluates a list expression and
emits one event per element, capped at 20, with `item` bound. Both are optional
and small; if the owner wants the narrowest reading of the original shape, drop
them and accept that sweep decisions collapse into one event per run.

### 3.3 What an expression can see

The scope is the one `save_message` uses, plus two additions, for **every** node
kind, so the same expression means the same thing wherever it is evaluated
(section 4):

| Name | Meaning |
|---|---|
| `output` | The node's result, as the workflow would see it as `nodes.<id>` (the map `activityResultMap` builds, `activity_message_save.go:124`; for a `compute` node its resolved outputs; for a `run` node `stdout`, `stderr`, `exit_code`, ...). |
| `inputs`, `workflow`, `iter`, `trigger` | As in `PostActivityContext` (`cel/types.go:229-260`). Only the inputs an expression references are shipped to the worker. |
| `node` (new) | `node.id`, `node.type`, `node.path`, `node.attempt`, `node.duration_ms`. Read-only. |
| `item` (new) | Only inside `each:`. |

There is deliberately **no `nodes.*`**: the worker has no view of sibling outputs
(`runtime/save_message.go:60-63` says why the same is true of `save_message`), and
giving workflow-side nodes `nodes.*` while activity nodes lack it would make the
same block mean different things on different kinds. The consequence is a rule of
use, not a limitation: a node records what its own outputs say. When the reason
is computed from siblings, the node (usually a `compute`) exports it as an output
and `observe` reads that output. rc-train's `plan` needs exactly this
(section 13.1). Secrets are not in scope: they reach a run node as environment
variables and are never part of a CEL context.

### 3.4 Workflow-level defaults

A workflow's `observe:` applies to every node of that workflow, merged with the
node's own: `attributes` by key (the node wins), `events` concatenated (defaults
first), `metrics` by name (the node wins). It is not inherited by sub-workflows;
a child workflow's nodes follow the child's own defaults. Defaults are merged
**in the workflow**, when the request for a node is built, so the activity never
needs the definition; the merged block per request is capped at 4 KiB, because it
rides in Temporal history (section 8.1).

Because a default is evaluated against every node's different `output`, defaults
should be guarded with `when:` and `.?` access (`output.?exit_code.orValue(-1)`).
An expression that fails on a node it was not written for is counted and skipped
(section 4), never reported to the author as a node failure.

### 3.5 The `decision` convention

A decision is an event named `decision` with `name` (which decision), `value`
(what was chosen) and `reason` (why). By convention `value` is a short code
(`cut`, `no_cut`, `review:changes`, `take`, `hand_off`) and `reason` is a code too
when the author wants it in telemetry (section 8.2). Nothing enforces the
convention. The run view renders `decision` events as a one-line "name: value
(reason)" and every other event generically; dashboards can group by
`event.name = 'decision'`. The convention is documented in `docs/reference/nodes.mdx`
and used by the builtin workflows that decide something.

## 4. Evaluation

**Where.** There are two evaluation sites and one record path.

*A. Nodes that dispatch an activity* (`call_llm`, `execute_tools`, `compact`,
`action`, `invoke_tool`, `git_publish`, `create_worktree`, `run` through
`ExecuteRunStep`, and any other kind whose `model.IsActivityNode` holds,
`model/constants.go:93`). The workflow builds a `types.ObserveRequest` next to the
`SaveMessageRequest` (`step_executor.go:1650`): the merged block, the referenced
inputs, `workflow`, `iter`, the node's path and its loop scope. It rides in
`RuntimeContext` (`runtime_context.go:43` is the neighbour) or, for a run node,
under a flat-map key (as `RunStepSaveMessageKey`); `execute_tools` carries it
explicitly for the reason above. In `ActivityWrapper.Execute` the evaluation sits
**immediately after the node's `save_message` succeeds and before
`clearMessageOnlyFields`** (`registry.go:1215-1232`): the result is still whole,
and a failed save retries the activity before anything is observed, so a retry
cannot observe twice. The wrapper writes the row, sets the attributes and events
on `trace.SpanFromContext(ctx)` (the node's own span), and emits the metrics.

*B. Nodes the workflow completes itself* (`compute`, `join`, `router`, `loop`,
`workflow`, and the inline approval path). Evaluation is pure CEL over a result
the workflow already holds, so it is deterministic and safe to do in workflow
code (edge conditions already are). The **recording** is not: writing a row and
emitting telemetry are side effects, so the workflow hands the bounded, already
evaluated result to a new **local activity**, `RecordNodeObservation`, through
the existing `executeBookkeeping` seam (`bookkeeping_local.go:53`). It is gated by
a new `workflow.GetVersion` change id, as every addition to workflow commands is
(`bookkeepingLocalGate`, :47): a run that began before the release replays with no
such command and never records. For these nodes there was no `workflow.node` span;
the local activity's input names the node, so the run-scope interceptor
(`internal/observability/run_scope.go:51`) can open one. **To verify in the PR:**
the Temporal SDK hands worker interceptors to local-activity execution
(`internal_task_pollers.go:1333` passes `lath.interceptors` in the pinned v1.51.0),
but I did not run an interceptor against a local activity; if it does not apply,
the activity opens the span itself with `telemetry.StartNodeSpan`. Either way its
duration is the recording's, not the node's; the attribute reference says so and
the span carries `reliant.node.recorded=true`.

*Not evaluated:* a node that failed (no output to read; the span already has the
failure), a node skipped by its `condition:` (the `SkippedStep` row already says
so, `engine.go:289`), and a cancelled or paused activity. A **tolerated** failure
(`tolerate_failure`, outputs `{failed: true, error: ...}`) is evaluated, and
`output.failed` is readable.

**Never fails the node.** Each expression is evaluated alone under the existing
CEL cost limit (`MaxEvalCost`, `cel/env.go:200`) and a 50 ms budget for the whole
block, inside a `recover()`. An error drops that one key, event or metric, adds an
entry to the row's `errors` (the CEL message, cut to 200 bytes, durable only: the
author needs it and it is not telemetry), and increments
`reliant.observe.errors{kind}`. The write is best-effort on a detached context,
exactly as `writeStepExecution` is (`registry.go:2032`, 2049-2062): a failure is
logged and the activity result is returned unchanged. Observability can degrade;
it cannot change what a run does, which is the "we always need to recover
gracefully" rule applied to this feature.

## 5. Where it is stored: a sibling table, not a `step_executions` column

The owner's question had two answers on the table (a `decisions jsonb` column on
`step_executions`, or a sibling table). **A sibling table**, for four reasons.

1. **The primary use case has no row to hang a column on.** `compute` nodes write no
   `step_executions` row (section 2). Writing one would change what the run
   view, `workflow status` and `FirstHandledNodeFailure` (`step_execution_repo_ext.go`)
   count: a new row per `compute` node of every run, in a view that today shows
   user-facing attempts. A table keyed by the node, not by a step row, avoids
   changing any existing read.
2. **`step_executions` is the hot, bulky table, and observations are small and
   long-lived.** The ledger retention job exists because its internal rows are the
   bulk (`prunableStepsSQL` prunes by activity name, `step_execution_repo_ext.go:489`;
   "32,013 of 32,023 steps are internal", `model/internal_activities.go`). A decision
   should outlive the output it explains. A table lets it have its own retention
   (section 9) instead of the step table's.
3. **Reads stay as cheap as they are.** `ListRunNodeExecutions` is budgeted down to
   the byte (`:164-190`), and `GetBasicStepExecutionsForChat` is the chat
   timeline's hot path (`BasicChatStepExecutionsSQL` :374, "~6ms against ~1s").
   Adding a jsonb to every row those queries select, or widening them, puts the
   cost of a rare feature on the common read.
4. **Additive and rollback-safe.** A new table is `forge:backward-compatible` by
   construction; an old binary never reads it.

```sql
CREATE TABLE node_observations (
    id            text PRIMARY KEY,
    chat_id       text NOT NULL,          -- joins gen_ai.conversation.id
    workflow_id   text NOT NULL,          -- the workflows row of the graph the node ran in
    run_id        text NOT NULL DEFAULT '',   -- forge.ai.run.id
    step_id       text NOT NULL,
    node_path     text NOT NULL DEFAULT '',
    loop_node_id  text,
    loop_iteration bigint,
    attempt       integer NOT NULL DEFAULT 1,
    source        text NOT NULL DEFAULT 'workflow',  -- 'workflow' | 'model' (section 12)
    attributes    jsonb NOT NULL DEFAULT '{}',
    events        jsonb NOT NULL DEFAULT '[]',
    errors        jsonb NOT NULL DEFAULT '[]',
    truncated     boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- idempotent under an activity retry or a replayed local activity:
CREATE UNIQUE INDEX node_observations_node ON node_observations
    (workflow_id, step_id, node_path, coalesce(loop_node_id, ''),
     coalesce(loop_iteration, -1), attempt, source);
CREATE INDEX node_observations_chat ON node_observations (chat_id, created_at);
```

`chat_id` and `run_id` are stored on the row (not derived through `workflow_id`)
because the handoff's requirement is that every decision record carries both, and
because it makes account purge and project delete one statement each (section 9).
There is no foreign key: no run-scoped table has one (`project_delete.go:139-140`).
A row is written only when the evaluation produced at least one attribute or
event; a metrics-only block, such as a workflow-wide node-duration histogram,
writes nothing. The migration is created with `task db:migration`, and the ORM
and `internal/store` are regenerated (`task generate:forge`).

Metrics are **not** stored: they are aggregates, they go to the metrics pipeline,
and a counter has no per-node row to show. A decision wanted in the run view is an
event.

## 6. How it is emitted

**Span attributes** from `observe.attributes` are set on the node's span as
`reliant.observe.<key>`, so an author cannot collide with `gen_ai.*`, `forge.ai.*`,
`org.id` or `reliant.*` (`dev-docs/observability-attributes.md` namespaces).
**Span events** keep the author's event name and attribute keys (they are scoped to
the event). The span already carries the run scope, `org.id` and the chat id
(`telemetry.RunScope.SpanAttributes`, `internal/telemetry/run_scope.go:127`, and
`GenAIAttributes` :165), so every decision joins to the run and the conversation without the author
writing either. A node span written by `StartNodeSpan` uses `SpanAttributes`, not
`GenAIAttributes` (`observability-attributes.md`, "Known gaps"): the PR that adds
observation attributes must not change that, and the per-chat join for a decision
is the stored row's `chat_id` plus `reliant.chat.id` on the span.

**Metrics** are created on `otel.Meter("github.com/reliant-labs/reliant/internal/workflow/observe")`
(the existing pattern: `providers_worker.go:1044`) and named
`reliant.observed.<name>`. Counters and histograms are the OTEL instruments of the
same name; a gauge is `Int64Gauge`/`Float64Gauge` (present in the pinned
`otel/metric` v1.47.0). The Prometheus gatherer already merges OTEL metrics
(`observability.go:161`).

**Cardinality is capped at three layers**, because a CEL expression can emit
anything:

1. *By construction.* A metric's attributes are `forge.ai.workflow.name`,
   `forge.ai.node.id`, `forge.ai.node.type` and at most four author keys. Never
   `org.id`, a user, chat, run or thread id, never a message or output: an id on a
   metric is unbounded by definition. (When tenant export arrives, per-org
   metrics need `org.id`; that is a separate meter provider with its own cap and
   is out of scope here.)
2. *By value.* A metric attribute value must be a bool, an integer or a
   code-shaped string (8.2); anything else becomes the literal `other`, not an
   omission, so a bad value cannot split a series.
3. *By count.* Per process and per metric name, the first 64 distinct attribute sets
   are kept; later new sets collapse into `{overflow="true"}` and increment
   `reliant.observe.overflow{metric}`. At most 8 metrics per node, 32 per
   workflow after merging, and 4,096 series per process. A restart resets the
   per-process sets, so the backend sees at most (processes x 64) series per
   metric, which is a bound, not a guess.

## 7. Where it is shown

Both read paths gain an additive field; neither changes an existing one.

```proto
// common.proto (chat.proto already imports it; run.proto gains the import)
message NodeObservation {
  string node_id = 1;  string node_path = 2;  string loop_node_id = 3;
  optional int32 loop_iteration = 4;  int32 attempt = 5;
  string source = 6;                              // "workflow" | "model"
  google.protobuf.Struct attributes = 7;          // typed values
  repeated ObservedEvent events = 8;
  repeated string errors = 9;
  bool truncated = 10;
  string created_at = 11;
}
message ObservedEvent { string name = 1; google.protobuf.Struct attributes = 2; }

// run.proto:  GetRunResponse.repeated NodeObservation observations = 4;
// chat.proto: WorkflowExecution.repeated NodeObservation observations = 22;
```

A separate list, joined to attempts by node path, loop scope and attempt, rather
than a field on `RunNodeExecution`/`StepExecution`, because a `compute` node has no
attempt row to carry it (section 5). A client renders an observation under the
attempt it matches and, for a node with no attempt row, as its own entry.

- **`RunService.GetRun`** reads the run tree's observations next to its node rows,
  behind the same ownership check (`runOwnedBy`, `run.go:21`), bounded to the same
  200-row window (`rundiagnostics.RecordLimit`). The web run view
  (`RunNodes.tsx`) shows, per node, the `decision` events as one-liners and the
  rest as an expandable "Observed" block, and lists observations for rowless
  nodes in node order.
- **`ChatService.GetWorkflowExecutions`** returns them **only in the FULL view**
  (the one `reliant workflow status` already requests, `status.go:129`). The BASIC
  view is the chat timeline and stays as lean as it is. `SummarizeSteps`
  (`status.go:251`) gains an `Observations` list on `StatusStepGroup`, and the
  CLI prints decisions under their node.
- **Shared-chat viewers must not see them.** `ElideWorkflowsForViewer`
  (`internal/chatproto/chatproto.go:149`) strips what steps produced for viewers
  of a shared chat; observation attributes are derived from outputs and are elided
  the same way. This is easy to miss because the new list is not a step field.

`ListStepExecutions` (the builder's Run tab) is left alone in this design.

## 8. Size caps and redaction

### 8.1 Caps

| What | Limit | On exceeding |
|---|---|---|
| attribute / metric-attribute keys | `^[a-z][a-z0-9_.]{0,63}$` | parse error (the YAML is wrong) |
| attributes per node, per event, per metric | 32, 16, 4 | extras dropped in order; `truncated` set |
| events per node (after `each:`) | 16 (`each:` 20 per event) | extras dropped; `truncated` |
| a stored string value | 512 bytes (cut on a rune boundary, marked, as `truncateString`, `step_record.go:133`) | cut |
| one stored row (attributes + events) | 8 KiB of JSON, by the `boundedJSON` method (`step_record.go:96`): long strings first, then trailing events | marker + `truncated` |
| rows per root run | 2,000 | later rows not stored; spans and metrics still emitted; `reliant.observe.dropped` |
| the merged `observe` block in one activity request | 4 KiB | validation **warning**; the block is not sent |
| evaluation | `MaxEvalCost` per expression, 50 ms per node | the remainder skipped |

The 4 KiB request cap exists because the request rides in Temporal history
(the reason `SaveMessageRequest` ships only referenced inputs).

### 8.2 Redaction, and the rule that keeps telemetry clean

The agreed attribute rules (`dev-docs/observability-attributes.md`, "The rules")
forbid bodies, command text, params, free text and error text on a span, and ask
for a closed vocabulary when a span must say *why* (rules 3 and 5). A CEL
expression can emit any of those by writing `output.stdout`. Policing expressions
is impossible; the rule is applied to **values**, at one choke point.

There are two destinations with different trust:

- **The durable record, the run view and the CLI** are the tenant's own data, stored
  in the same database and behind the same chat access check as
  `step_executions.output_json`, which already holds full outputs. Strings are kept
  (bounded as above) after `logging.RedactText` (`logging/redact.go:118`) removes
  credential shapes. Free-text reasons live here.
- **Telemetry** (spans, span events, metrics) is exported off that store: to
  Reliant's staff ClickStack today and to tenant destinations later. Content
  capture is an **open owner decision** whose default is never. So a value leaves
  as telemetry only if it is a **number, a bool, or a code-shaped string**:
  `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`. Any other string is replaced with
  `[withheld:<n>b]` on a span or event and `other` on a metric; the full value
  remains in the stored row. One function applies this (`observe.SafeForExport`),
  so relaxing it later for an org that opted into content capture is one
  decision in one place. The shape check is a heuristic, not a proof: it
  guarantees one token with no spaces, slashes, `#` or `@` (so no sentence, URL,
  repository name or email), after `RedactText` has masked credential shapes. A
  deliberate one-word leak still passes, as it would any filter; the rule keeps
  accidents out, it does not stop an author who writes `value: "{{ output.token }}"`.
  The code-shape pattern is case-insensitive, so pr-review's `HIGH` and `PIN` tiers pass.

This is rule 5 made mechanical: `reason: "{{ output.hold }}"` where `hold` is
`main_unchanged` reaches telemetry and the run view; `detail: "{{ untrustedText(output.blocked, 200) }}"`
reaches only the run view. An author who wants a reason in telemetry writes a
code, which is what the rule asks for. The existing `untrustedText` and
`untrustedJson` CEL helpers (`cel/untrusted.go`) are the recommended way to bound
and clean a string taken from untrusted output.

Opaque ids (a chat id taken from an output, a node id) are code-shaped and pass.
`org.id` and the run scope are added by the platform, never by the author, and an
author key cannot be `org.id` (reserved by the `reliant.observe.` prefix). No error
text is ever recorded on a span (rule 4): an evaluation error is a counter plus a
durable `errors` entry.

## 9. Retention, purge and project delete

The new table is run- and chat-scoped with no cascade, so it joins every place
the handoff lists by hand. I did not find a test that enumerates chat-keyed
tables against these lists (I searched the purge and project-delete test files
and test names); the migration PR should add one that
fails when a table with a `chat_id` is in none of them.

| Site | Change |
|---|---|
| **Ledger retention** (`internal/db/ledger_retention.go:126`, `PruneStepExecutions`; the pass at `internal/app/providers_worker.go:1097`) | A sibling `PruneNodeObservations(ctx, cutoff, pause)` with its own cursor row in `ledger_retention_cursors` (as `stepRetentionCursorName`, `:36`), reusing the same judged-tree CTE (`stepRetentionTreeSQL`, `step_execution_repo_ext.go:472`) so a row is deleted only when its workflow is completed or cancelled before the cutoff. A new worker-config knob `node_observation_retention`, beside `step_execution_retention` (`proto/reliant/config/v1/worker_config.proto:208`), **off by default like its neighbours** (retention deletes data), set per environment. The separate knob is the reason for a separate table: decisions are kept longer than internal step output. |
| **Account purge** (`internal/accountpurge/accountpurge.go:180`) | `DELETE FROM node_observations WHERE chat_id IN (SELECT id FROM chats WHERE user_id = $1)` at the same depth as `step_executions`, before `chats` (`:218`). |
| **Project delete** (`internal/store/project_delete.go:141` and the bounded-slice list at `:346`) | `DELETE FROM node_observations WHERE chat_id = ANY(?0)` beside `step_executions`, and a bounded-slice entry copying the `step_executions` one (`:346-350`), so a project with many runs is thinned in slices, not in one transaction. |

Rows are small (8 KiB at the cap, around 1 KiB typical), but not free: ten
observed nodes on ten thousand runs a day is about 100,000 rows a day, around
100 MB a month before indexes, which is why retention is part of the design and
not a follow-up.

## 10. Validation and lint

**Validation** (`internal/workflow/validation`, enforced at every load):

- Structure is a **parse error** (the YAML is wrong, exactly like a typo in any other
  key): unknown keys inside `observe`, a `kind` outside the three, a bad name or
  key pattern, over-limit counts.
- **Expression content is a warning, not an error.** A new `siteObserve` in
  `cel_sites.go:58` compiles each expression in the `PostActivityContext` environment
  plus `node` and `item`, with `output` typed as the node's result as
  `validateSaveMessage` does (`:515`), and rejects `nodes.*` as `save_message`
  does (`rejectSaveMessageNodesRefs`, `cel.go:1264`). Findings go through
  `AddWarning`, because `load_workflow.go:109-112` blocks a run on any validation
  *error* and an observability typo must never stop a run. The lint rule below
  promotes the same finding to an error at the merge gate.

**Lint** (`internal/workflow/lint`):

- `observe-invalid` (error): the validation warnings above, surfaced where a
  definition is reviewed. This is where a typo in `observe` is stopped.
- `observe-decision-unrecorded` (warning): a node that **looks like it makes a
  decision and records none**. The heuristic: a `compute` or `run` node with an
  output named like a decision (`decision`, `decide`, `verdict`, `reason`, `why`,
  `skip`, `cut`, `hold`, `land`, `take`, `route`, `tier`, `label`, `handoff`,
  `give_up`, `unstick`) that something else reads (`readIndex.reads`,
  `graph.go:108`) and that has no `observe` of its own and is not covered by a
  workflow default. Silence it with `# lint:allow observe-decision-unrecorded - <reason>`
  (`allow.go:21`). Tuned against the four pipeline workflows (my scan of their
  definitions at control-plane `origin/main`, 133 `compute` nodes): the
  name-and-read heuristic flags 23 nodes, 10 of them where the output is read by
  an **edge condition** (pr-review 5: `early.skip`, `authority.verdict`,
  `held.label`, `lane.model_review`, `verdict.label`; pr-fix 5: `decide.skip`,
  `decide.stale`, `decide.give_up`, `decide.unstick`, `route.handoff`); rc-train's
  `plan.cut` is read by a `run` node's command, not an edge. A rule on **every**
  edge-source node would flag 50 of the 133, which is noise, so the rule is
  name-based and a warning, and it is a rule in the rollout sense: a new lint rule
  warns before it gates.
- Lint never runs at save or start (`lint.go:12-14`), and validation only warns about
  `observe` expressions, so no stored automation can be refused by this feature
  once the key parses.

The scenario harness already exercises save-message resolution through the same
entry point production uses (`scenario/runner/backend.go:2297`,
`resolveDelegatedSave`); observation evaluation is exposed the same way so a
scenario can assert "this run recorded `decision` `no_cut` because `main_unchanged`".

## 11. Compatibility and rollout

Reliant is at semver 1; the change is additive everywhere it touches a public
surface.

- **Workflow YAML.** A new optional key. The parser is strict, so a **server or
  CLI that predates the key rejects a file that uses it**, at parse, with the
  existing version-skew message (`unknown_field.go:34`: "reliant X does not define
  \"observe\" ... upgrade the server that runs it"). That is fail-closed at
  definition time and it is the same behaviour every earlier key (`concurrency`,
  `attention`) had; a file never half-runs. It means workflows adopt `observe`
  only after the release that parses it is the floor of every place the file is
  parsed (the control plane's pinned reliant, the merge gate's CLI).
- **Old worker, new definition.** At run time a node reaches an activity as
  protojson with `DiscardUnknown: true` (`activity_input.go:67`), the
  `RuntimeContext` and flat-map request are plain JSON that ignores unknown
  keys, and the new local activity is behind `GetVersion`, so an old activity
  worker ignores `observe` safely. **The one hazard is in workflow code:**
  `LoadWorkflow` parses the definition JSON with strict `protojson.Unmarshal`
  (`engine.go:475`) and its fallback is the strict YAML parser, so an old worker
  that replays a run whose definition contains `observe` (a rolling deploy, or a
  rollback after adoption) fails to load it. Old builds cannot be fixed, so the
  mitigation is sequencing:
  1. **Release A (tolerant reader):** the proto fields, the parser and validation
     accept and store `observe`, `LoadWorkflow` sets `DiscardUnknown: true`, and the
     runtime ignores the block. No workflow uses it yet.
  2. **Release B (the feature):** evaluation, the table, the local activity
     (behind `GetVersion`) and the RPC fields.
  3. **Adoption:** workflows add `observe` once A is the floor everywhere.
  `DiscardUnknown` in `LoadWorkflow` also removes this hazard for every later
  additive key, which is a good change on its own.
- **Database.** One new table, `forge:backward-compatible`. Old binaries never touch
  it; a rollback leaves it unread.
- **RPCs.** New optional fields on `GetRunResponse` and `WorkflowExecution`. Old
  web bundles, SDKs and CLIs ignore them and see exactly what they saw; the
  `buf breaking` gate passes.
- **Config.** One new knob, default off.

## 12. The model records observability itself

The owner's question: could the LLM record observability, perhaps on every tool
call so it can say what it decided, and what are other agents and tools doing?

### 12.1 What others do

Verified in this session unless marked.

- **Claude Code** exports OTEL metrics and, over the logs/events protocol,
  per-tool events (`tool_decision`, `tool_result`, `user_prompt`, ...), plus
  optional traces (beta). It does **not** ask the model for extra telemetry
  fields. Content is opt-in and off by default: `OTEL_LOG_TOOL_DETAILS` gates tool
  parameters and Bash commands, `OTEL_LOG_USER_PROMPTS` the prompt text,
  `OTEL_LOG_TOOL_CONTENT` tool content, with a 60 KB attribute cap, and spans
  "redact user prompt text, tool input details, and tool content by default"
  (code.claude.com monitoring docs). It passes a `TRACEPARENT` env var into Bash
  subprocesses so a script can parent its own spans. The Bash tool's model-written
  `description` is the closest thing to an "intent" field, and Reliant's shell
  tool carries the same pattern (below).
- **OpenAI Agents SDK** traces runs, turns, agents, generations, function calls,
  guardrails and handoffs by default; **developer code** adds `custom_span()`, the
  model does not. Inputs and outputs are captured by default
  (`trace_include_sensitive_data` is `True`) and one flag turns them off.
- **Laminar** (its Python SDK README) is developer-side as well: an `@observe()`
  decorator and `start_as_current_span`/`set_span_output`. (Its after-the-fact
  "signals" are from this chat's landscape notes, not re-checked.)
- **Anthropic's `think` tool** (engineering post, updated 2025-12-15) is a no-op
  tool whose arguments are the model's scratch reasoning. On tau-bench's airline
  domain it took pass^1 from 0.370 to 0.570 with an optimised prompt. The
  update says extended thinking is now recommended instead in most cases. It is
  a useful precedent for what option (a) is structurally (a tool whose payload is
  the point) and for its price (a tool call is a model round trip).
- **Raindrop "Signals" and LangSmith "Insights"** classify traces after the fact with
  a small model, with nothing added to the agent's turn (this chat's
  `llm-obs-landscape.md`; Raindrop confirmed there, LangSmith from prior knowledge).
- *Not verified here:* Codex CLI's `[otel]` config (documentation did not load) and
  LangGraph run metadata. I am not citing them.

The pattern across all of them: **the developer or the platform decides what is
recorded, and the model is not asked to narrate its own telemetry.** The one
exception is a human-readable description the model already writes for the
user's benefit (the Bash `description`).

### 12.2 What Reliant's tools already carry

The shell tool already has a model-authored, optional `description`
(`internal/llm/tools/shell.go:35`: "never executed, never inspected, and never
affects dispatch ... a reader (UI, transcript, audit) can see the intent of a
command ... Optional"), with prose guidance attached in `JSONSchemaExtend`
(`:88`, `shellDescriptionParamDoc` :66). So for the tool that runs arbitrary
commands, option (b) **already exists**. Other tools carry per-tool description
fields in places (`task_tool.go`, `plan_tool.go`) but no uniform intent field.
Tool call inputs are persisted in the message content blocks, so an intent in the
input is **durable with no new storage**, and the chat UI already renders tool
calls.

### 12.3 The options

**(a) A `record_event(name, attributes)` tool** the workflow or preset turns on
(`tools_config`). The handler appends to the node's observation with
`source = 'model'` and the span event is named `agent.<name>`; the same caps and the
same `SafeForExport` apply, plus a quota (20 per run) and a name pattern. It is the
only option that lets the model mark a *decision point* ("chose the minimal fix
over the refactor"). It is structurally the `think` tool. A tool call is a model
round trip, so unless the model batches it in parallel with a real call, each
event costs an extra turn: the full context is re-read.

**(b) An optional short `intent` on tool calls**, as Bash's `description`. The
model fills it in the same breath as the call, with no extra turn. It is useful
as audit text and as classifier input (option c reads "tool + intent" far better
than "tool + raw args"). It is also schema cost on every request, which is the
number that matters.

**(c) No model involvement:** classify after the fact over spans, messages and
tool results (Signals/Insights style), which is already section 4 of
`dev-docs/design/agent-analytics.md`'s direction. Nothing in the agent's turn
changes.

### 12.4 Cost of (b), the option the owner worried about

No measured tool-calls-per-run figure exists in the design notes I read
(`agent-analytics.md`, which plans a `tool_call_usage` table, and the LLM data
inventory), so these
are **assumptions to replace with a query** over `execute_tool` spans or
`step_executions`: a typical agent run makes 50 tool calls and a heavy one 300; an
`intent` averages 15 output tokens including its JSON key (the shell doc asks for
"5-10 words"); a property definition costs about 28 tokens of schema; a request
carries 15-60 preloaded tool schemas (`tools_config.preloaded_tools`; fewer with
lazy tool bodies). Prices below are placeholders, **assumed** $25/M output and
$1.50/M cache-read tokens; use the gateway's `llm_call_usage` rates.

| | Typical run (50 calls, 50 requests) | Heavy run (300 / 300) |
|---|---|---|
| (b) on every tool: output tokens | 750 | 4,500 |
| (b) output cost at the assumed price | about $0.02 | about $0.11 |
| (b) latency (about 80 tokens/s) | about 9 s | about 56 s |
| (b) on every tool: **schema tokens re-read** | 15 tools: 420/request, 21k/run; 60 tools: 1,680/request, 84k/run | 126k to 504k |
| schema cost at the assumed cache-read price | $0.03 to $0.13 | $0.19 to $0.76 |
| (b) scoped to 8 mutating tools: schema | 224 tokens/request, 11k/run | 67k |
| span bytes if exported (intent up to 160 B plus key) | about 10 KB/run | about 60 KB/run |
| (a) at 3 events/run (an extra turn each) | about 3 turns re-reading the context | not per-call |

The output tokens the owner worried about are the smaller half. The **schema** is
the larger: an extra property on every tool is re-sent on every request, and the
first rollout also invalidates the prompt cache for the tool-definition prefix.
So "every tool call" is the expensive way to do (b). The shell tool pays nothing
(it already has the field), and the tools whose calls an auditor actually asks about
are the side-effecting few: write/edit, integration calls, spawn, git publish.
Span export of an intent is excluded by rule 3 anyway, because model prose derived
from a command is content (8.2); it stays in the message and the UI.

### 12.5 Recommendation

1. **Do (c) first and keep it the default.** It costs the agent nothing and it is
   where the analytics direction is already heading.
2. **Add (b) narrowly:** reuse `description` as the shell tool's intent (no change), add an
   optional `intent` (one line, 15 words) to the 8 or so side-effecting tools, and
   gate it behind a preset/workflow switch (`tools_config`) so a workflow that
   audits its agent turns it on. Do not add it to every tool; measure the real
   tool-calls-per-run and tool-count first, and re-cost it.
3. **Offer (a) as an opt-in tool** for workflows whose agent genuinely decides
   something worth recording (a reviewer's verdict reasoning, a triage class).
   Few events per run, so the extra turn is negligible, and it lands in the same
   `node_observations` row as the workflow's own declarations. Do not make it
   default.

The reason is the shape of the cost: model-recorded data is the only option that
adds per-request tokens, so it is scoped to where the value is, while the
declarative `observe:` block and after-the-fact classification carry the general
case at no cost to the agent.

## 13. Worked examples

All five use real node ids and output names from control-plane's workflows at its
`origin/main`. Expressions marked "probed" were evaluated through
`wfcel.EvaluateTemplate` in this session and produced the shown types.

### 13.1 rc-train: why it did not cut

`plan` (rc-train.yaml, a `compute` node) computes `cut` as one boolean built from
nine conjuncts (the `cut:` expression), with no record of which failed. Today the
reason is a bash `echo`. `observe` reads outputs, so the node has to **export the
reason**; that is the one place the author does extra work, and it is what makes
the answer queryable.

```yaml
- id: plan
  type: compute
  outputs:
    cut: ...                      # unchanged
    # NEW: the first failing clause of `cut`, as a code (rc-train's own wording)
    hold: >-
      {{ cel.bind(w, nodes.w.world,
           inputs.dry_run ? 'dry_run'
         : w.mains == w.newest.inputs ? 'main_unchanged'
         : !(inputs.cut_now || w.asap || w.newest.cut_at == 0 || w.now - w.newest.cut_at >= w.config.cut_every_min * 60) ? 'cadence_not_elapsed'
         : w.?blocked.orValue('') != '' ? 'blocked'
         : w.?spent.orValue('') != '' ? 'spent_base'
         : w.?recurring.orValue('') != '' ? 'recurring_hold'
         : ... ) }}              # '' when cut is true
  observe:
    events:
      - name: decision
        attributes:
          name: cut
          value: "{{ output.cut ? 'cut' : 'no_cut' }}"                  # probed: "no_cut"
          reason: "{{ output.cut ? output.trigger : output.hold }}"     # probed with a stand-in field: the else-branch string
    metrics:
      - name: rc_train.ticks
        kind: counter
        attributes: { decision: "{{ output.cut ? 'cut' : 'no_cut' }}" }
```

"Why didn't it cut at 14:00?" is then the run view's `plan` entry for that tick,
or `reliant workflow status`, with `value: no_cut, reason: cadence_not_elapsed`,
and a time series of `reliant.observed.rc_train.ticks` by decision. (The `hold`
expression is abbreviated above: the real one lists the clauses of `cut` in order
and returns the first that fails. Free text such as `rc.sh`'s `blocked` message
would be exported as an output the same way and shown in the run view only, by
8.2.) The idle fast
tick, which ends at `idle` with "Idle: the train's state has not moved", is
the one trigger-adjacent case `observe` covers: put a `decision` event on `w`
(`output.idle`), value `idle` or `read`.

### 13.2 pr-review: tier and verdict

```yaml
- id: lane                                   # compute: tier, model_review
  observe:
    events:
      - name: decision
        attributes:
          name: review_tier
          value: "{{ output.tier }}"          # LOW|MEDIUM|HIGH|PIN|QUARANTINE|UNCLASSIFIED|ERROR
          reason: "{{ output.model_review }}" # none|single|deep|pin|legacy|error
          pr: "{{ inputs.repo }}#{{ inputs.pr_number }}"   # not code-shaped: stored, not exported
- id: verdict                                # compute: label, kind
  observe:
    events:
      - name: decision
        attributes:
          name: review_verdict
          value: "{{ output.label }}"         # review:passed | review:changes | review:close | ''
          reason: "{{ output.kind }}"         # code | judgment | reach
- id: enqueue                                # run: prints ENQUEUE=ok|held|failed
  observe:
    events:
      - name: decision
        attributes:
          name: enqueue
          value: "{{ output.stdout.contains('ENQUEUE=ok') ? 'enqueued' : output.stdout.contains('ENQUEUE=held') ? 'held' : 'failed' }}"
```

The `pr` attribute has `/` and `#`, so by 8.2 it never leaves as telemetry but is
in the run view: the repository name stays out of telemetry (rule 2) without the
author having to remember. A counter `review.tier{tier}` gives the tier mix.

### 13.3 pr-fix: take or hand off

```yaml
- id: decide                                 # compute: skip, stale, give_up, unstick, owner, what ...
  observe:
    events:
      - name: decision
        attributes:
          name: fix_route
          value: >-
            {{ output.skip != '' ? 'skip' : output.unstick != '' ? output.unstick
               : output.stale ? 'unstick_stale' : output.give_up ? 'give_up'
               : output.owner != '' ? 'hand_off' : 'take' }}            # same shape probed: "hand_off"
          reason: "{{ output.skip != '' ? output.skip : output.what }}"  # free text: stored, not exported
          owner_chat: "{{ output.owner }}"                              # an opaque id: code-shaped, exported
```

Caveat, and the guidance for all routing nodes: the `value` above **re-derives**
the route, and the graph routes on `decide.skip` / `decide.unstick` / ... in its
edges, so the two can drift. Where the node has a single route, write it as an
output (`route`) that the edges read too, and have `observe` read that output.
The scenario assertion in section 10 is the backstop.

### 13.4 hotpath: land or hold

`land` prints one `HOTPATH <repo>#<n> merged|queued|skip|failed <why>` line per PR
and `tally` (a `compute`) exposes `lines`, `merged`, `failed`, `summary`. A sweep
decides several PRs in one run, so this uses `each:`:

```yaml
- id: tally
  observe:
    events:
      - name: decision
        each: "{{ output.lines }}"
        attributes:
          name: land
          value: "{{ item.split(' ')[2] }}"              # probed: "merged"
          pr: "{{ item.split(' ')[1] }}"                 # repo#n: stored, not exported
          reason: "{{ untrustedText(item, 200) }}"       # durable only
    metrics:
      - name: hotpath.merged
        kind: counter
        value: "{{ size(output.merged) }}"
```

### 13.5 GitHub API cost per node

**Integration steps need no YAML:** every `github/*` and `http/request@1` action
node already gets an `integration.call` span with `reliant.integration.rate_limit.{limit,
remaining,used,resource,reset,retry_after}` (#3286, pending merge;
`dev-docs/observability-attributes.md`), a child of that node's `workflow.node`
span, so cost per node and per workflow is a query. **Shell `gh` calls** go
through `control-plane/scripts/pipeline/bot-gh.sh`, which logs only to a
per-machine ledger (`gh_ledger`), so they make no span. The honest options are to
move those calls onto action nodes (automatic, preferred) or to have the script
print one summary line (`GH_BUDGET calls=14 remaining=4120 limit=5000 reset=...`,
which does not exist today and is a control-plane script change) and read it:

```yaml
- id: land
  observe:
    metrics:
      - name: github.api.calls
        kind: counter
        unit: "{call}"
        value: "{{ int(output.stdout.split('\\n').filter(l, l.startsWith('GH_BUDGET ')).join('').split(' ')[1].substring(6)) }}"   # probed: 14
        attributes: { identity: bot }
```

A **workflow-level** default gives per-node latency for everything, with no
per-node YAML:

```yaml
observe:
  metrics:
    - name: node.duration
      kind: histogram
      unit: ms
      value: "{{ node.duration_ms }}"
      attributes: { node_type: "{{ node.type }}" }
```

### 13.6 Trigger skips: out of scope for `observe`

A trigger firing that is skipped (filtered, superseded by `waiting: latest`,
capacity-expired) launches **no run**, so there is no node to carry a block. Those
outcomes are `trigger_events.outcome`/`outcome_detail` rows today, which the
delivery inbox reads, and `trigger.fire` spans (#3289, pending merge) put them in
traces. `observe` covers the trigger-adjacent cases that *do* start a run, such as
rc-train's idle tick (13.1). I would not stretch `observe:` to triggers.

## 14. Rejected alternatives

- **A `decision:` key (and nothing else).** My first shape, and the first
  instinct (`decision: {name, value, reason}`). It fits rc-train and nothing
  else: the GitHub cost needs numbers, per-node duration is a metric, a
  trigger-adjacent skip is an event with a different shape. Each would need
  another key, and a user's own observation (a count of retries, a model-chosen
  branch) has no home. A decision is an event with three conventional attributes;
  making it a schema element buys one convention and costs the generality the
  owner asked for.
- **Record every node's outputs automatically.** The outputs are already there for
  rows that have them, and they are the wrong data: they carry stdout (rc.sh's
  world document is more than 16 KB), model messages and free text, which is why
  `ListRunNodeExecutions` elides them to a stdout tail (`:174-190`) and
  `GetRunResponse` promises "arbitrary outputs are never included"; the attribute
  rules forbid it on spans (rule 3); `compute` nodes have no row; and an output
  holds a *value*, not the *reason* a human wants. Auto-recording would also make
  the cardinality and redaction problems unsolvable, since nothing says which
  field matters. Declaring what to record is the point.
- **The CLI (`reliant run decide <name> <value> --reason ...`).** Opt-in by the
  script, so it is forgotten, which is the owner's objection; and a run step
  cannot rely on it: `toolexec.ToolRequest.Environment` reaches no daemon, and a
  `credentials: none` sandbox hides the daemon credential the CLI would
  authenticate with.
- **Also considered: a structured-stdout protocol** (`OBSERVE {json}` lines the
  runtime parses out of a run node's output). It records only what a script
  remembers to print, so it is the CLI's problem again, and it covers no
  `compute` or agent node.
- **Also considered: a `decisions jsonb` column** on `step_executions`; rejected
  in section 5. And **span-only recording** (no store): the staff exporter is the
  only reader, so nothing reaches the run view, which is a settled requirement.

## 15. Implementation plan

Four PRs, in this order. Each is additive and independently shippable.

1. **Release A: schema and tolerant reader.** `NodeObserve` in `workflow_v2.proto`
   (`Node.observe = 30`, `Workflow.observe = 27`); the three parser spots
   (including the `validateActivityNodeTopLevelKeys` allow-list, and the generator
   list at `yamlbindingsgen/main.go:82`) with a test over **every** node kind
   (the `tolerate_failure` omission shows why); marshal round trip; validation
   (`siteObserve`) as warnings; `observe-invalid` lint; `LoadWorkflow` with
   `DiscardUnknown`. The runtime ignores the block. Docs: `docs/reference/nodes.mdx`,
   `docs/workflows/lint.mdx`.
2. **Evaluation and storage for activity nodes.** The migration, store and
   repository methods; `ObserveRequest`; evaluation in `ActivityWrapper`;
   `observe` package (limits, `SafeForExport`, metrics with the cardinality
   layers); the span attribute and event emission; the attribute reference
   (`dev-docs/observability-attributes.md`) gains an "Observed attributes" section.
   Tests: never-fails (an error, a panic, a slow expression, a failing store), caps,
   redaction, a retry does not double-record.
3. **Workflow-side nodes.** The `RecordNodeObservation` local activity behind
   `GetVersion`; replay fixtures with and without it (as `bookkeepingLocalGate` has);
   `compute`, `join`, `router`, `loop`, `workflow`.
4. **Reads, retention and surfaces.** The `NodeObservation` message and the two RPC
   fields; `ElideWorkflowsForViewer`; the web run view; `workflow status`;
   retention, account purge, project delete and the guard test; the
   `observe-decision-unrecorded` lint rule; scenario assertions.
   Then adopt it in control-plane's rc-train, pr-review, pr-fix and hotpath
   (one PR there, after A is the floor).

A model-recorded `record_event` tool and the scoped `intent` field (section 12) are
a separate PR after 2, only if the owner accepts the recommendation.

## 16. Questions for the owner

1. **A sibling table, not a column** (section 5). Needed for `compute` nodes; the
   alternative is writing step rows for them, which changes what the run view and
   `workflow status` show.
2. **Only code-shaped strings leave as telemetry** (8.2). Free-text reasons stay in
   the run view and CLI. This is the mechanical form of the agreed rules; it
   changes if content capture is ever opted into per org.
3. **`when:`, `each:` and the `node` namespace** are my additions to the shape
   (3.2, 3.3). Cut any; hotpath's per-PR decisions need `each:`.
4. **No `nodes.*` in `observe` expressions** (3.3): a node records its own outputs,
   so a `compute` exports the reason it wants recorded.
5. **Section 12's recommendation:** (c) by default, (b) only on the side-effecting
   tools behind a switch, (a) opt-in.
6. **Observation retention** is a separate worker-config knob, off by default
   (section 9); the production value is the owner's.
7. **Release A ships alone first** (section 11), and workflows adopt `observe` one
   release later.

## Appendix: what I probed

- `ParseWorkflow` on a `run` node and a `compute` node with `observe: {}` at the
  base `3b8d3b81f`: both fail with `unknown structural arg key "observe"` and the
  version-skew hint. `tolerate_failure: true` parses on `run`/`compute` and fails
  with `node: unknown key` on `action`, `call_llm` and `execute_tools`.
- `wfcel.EvaluateTemplate` over a `PostActivityContext` with a pipeline-shaped
  `output` returned native types for the expressions in section 13 that are marked
  "probed" (a string, an integer 14 from a `GH_BUDGET` line, a bool-driven code).
- The pipeline workflow scan in section 10 parsed rc-train, pr-review, pr-fix and
  hotpath from control-plane `origin/main` and counted `compute` nodes whose
  decision-named outputs are read elsewhere.
- Not measured: tool calls per run, tool count per request, real token prices.
  Section 12.4 says which numbers are assumptions.
