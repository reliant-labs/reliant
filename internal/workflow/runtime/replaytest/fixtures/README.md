# Replay-compatibility history fixtures

These JSON files are **real Temporal event histories** of `DynamicWorkflow`
runs, captured from an ephemeral Temporal dev server driving the production
worker registration path (`workersetup.StartWorker`) with a scripted LLM.

They are a contract: **the current workflow code must stay replay-compatible
with these histories.** `TestReplayFixtures` (in the parent package, plain
`go test`, no external dependencies) replays every fixture through the current
`DynamicWorkflow` registration on every test run.

## Why

Temporal re-executes ("replays") a workflow's recorded history through the
current code whenever a worker picks up an in-flight run — on deploy, worker
restart, or sticky-cache eviction. If the code now issues a different sequence
of workflow commands (activities, timers, side effects, signal handling,
`workflow.Go` coroutines, child workflows) than what the history recorded, the
run fails its workflow task with
`WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR` (TMPRL1100) and retries
that failure **forever** — the run is wedged and makes no progress.

This happened in production: a multi-day run wedged after worker rebuilds
changed workflow code mid-run, and nothing caught it before deploy. These
fixtures make that class of change fail at **test time** instead.

## Fixture inventory

| Fixture | Workflow | Shape it pins |
|---|---|---|
| `agent_tool_loop.json` | `builtin://agent` | Plain agent loop: load → preflight/status bookkeeping → CallLLM → ExecuteTools (real bash) → CallLLM → end turn → completion. |
| `structured_agent_loop.json` | `builtin://structured-agent` | Inline loop node + inline agent pipeline (call_llm/execute_tools edges), a regular-tool iteration, then a response-tool iteration that exits the loop. |
| `router_dispatch.json` | `replay-router` (user-draft workflow, node-router → `builtin://agent` sub-workflow node) | Pitch-deck-like router dispatch: routing CallLLM with `node_routing_decision` response tool, dynamic dispatch, inline sub-workflow execution with `thread.mode: new` + inject, `save_message`. |
| `pause_resume.json` | `builtin://agent` (`ask: true`) | Signal machinery: `signal.question.*` blocking, `signal.pause` delivered while blocked, question resolution with feedback, park on the epoch-broadcast pause `Await`, `signal.resume`, second turn, second question, completion. |
| `compaction.json` | `builtin://agent` (tiny `compaction_threshold`) | Compaction edge: token count exceeds threshold after execute_tools → compact node (summary LLM request, new context window) → post-compaction turn → completion. |

## When `TestReplayFixtures` fails

Your change made `DynamicWorkflow` (or code it calls **inside the workflow
sandbox** — executors, routers, CEL/template evaluation that gates commands)
emit a different command sequence for at least one recorded history. You have
exactly two options — pick one **deliberately**:

### (a) Make the change replay-compatible (default choice)

Old histories must keep taking the old code path. The standard tool is
[`workflow.GetVersion`](https://docs.temporal.io/develop/go/versioning):

```go
v := workflow.GetVersion(ctx, "my-change-id", workflow.DefaultVersion, 1)
if v == workflow.DefaultVersion {
    // old behavior — replayed histories land here
} else {
    // new behavior — fresh runs land here
}
```

Things that break replay: adding/removing/reordering `workflow.ExecuteActivity`
calls, changing an activity's registered name, adding/removing timers
(`workflow.Sleep`, `workflow.NewTimer`), changing `workflow.Go` coroutine
structure, changing side effects, or changing any branch condition that gates
the above. Things that do NOT break replay: activity *implementation* changes,
changes to values that don't alter the command sequence, logging.

### (b) Accept the break and regenerate the fixtures

```
make replay-fixtures
```

then commit the updated JSON together with your change.

**Understand what you are accepting.** When this deploys, every in-flight
workflow run whose history matches the old shape will wedge with TMPRL1100 the
next time its worker replays it (immediately, on the deploy itself). Those
runs will NOT self-heal: they depend on the reconciler detecting the wedged
execution and the resume/checkpoint machinery starting a replacement run from
the last position checkpoint, with thread history as conversation truth.
That recovery loses in-memory workflow state (node outputs of the old run) and
costs a user-visible interruption. Acceptable for genuinely-needed breaking
changes; not acceptable as a side effect nobody noticed — which is the failure
mode this suite exists to prevent.

## Regenerating

`make replay-fixtures` — brings up Postgres via docker compose, runs the
build-tagged generator (`go test -tags replayfixtures ./internal/workflow/runtime/replaytest/`,
which boots an ephemeral Temporal dev server per run), rewrites every
`fixtures/*.json`, then runs the untagged replay test to verify the new
fixtures replay cleanly against the current code.

Add a new fixture by adding a `TestGenerateFixture_*` scenario in
`generate_gen_test.go` — prefer shapes that mirror an e2e story
(`e2e/stories/`) so the pinned history corresponds to a flow that is verified
end-to-end.

## Determinism of regeneration

Two generation runs do **not** produce byte-identical files: histories embed
server-assigned timestamps, run IDs, task-queue suffixes, and DB-generated
IDs inside activity payloads. That is expected and harmless — replay
compatibility is about the **command sequence**, not payload bytes. The
ordered event-type sequence is stable across runs for these scripted
scenarios (verified by generating twice and diffing), with one known benign
exception: in `router_dispatch.json` a fire-and-forget `SaveMessage`
activity's STARTED/COMPLETED events race workflow completion, so they may or
may not appear at the tail of the history. The workflow's command sequence is
identical either way and both variants replay cleanly. Whitespace/key-order
of the JSON is normalized at export. Review regeneration diffs by event-type
sequence, e.g.:

```
jq -r '.events[].eventType' fixtures/agent_tool_loop.json
```

## Caveats

- The replay test only covers the shapes captured here. A non-deterministic
  change on a path no fixture exercises (e.g. parallel loops, spawn,
  daemon-offline breaker) will not be caught — add a fixture when you add or
  materially change such a path.
- Fixtures pin the workflow-side contract of activity *interfaces* recorded in
  history (names, payload decoding), not activity implementations.
