# One scenario engine: the real DynamicWorkflow

Status: implementing (branch `workflow-history`). Pre-launch: delete, don't
deprecate.

## Why

Two engines ran the same scenario YAML:

- `internal/workflow/runtime/simulator` + `runtime/simulator*.go` (~8.5k lines):
  a graph walker that re-implements the runtime's routing, loops, joins, spawn,
  CEL scope.
- `internal/workflow/runtime/scenariotemporal`: runs `DynamicWorkflow` itself
  inside Temporal's in-memory test environment (`go.temporal.io/sdk/testsuite`),
  mocking only activities. No server, no DB.

Every divergence found on this branch was the simulator being wrong while
scenarios stayed green: loop `while` could not see `nodes` in the runtime but
could in the simulator; `save_message` for activity-backed nodes was never
evaluated; `agent/manual_mode_denied` loops differently. A second engine is a
second definition of workflow semantics, and it drifts. Scenarios exist to prove
the runtime works, so they run on the runtime.

## Target

- **Package** `internal/workflow/scenario` owns everything scenario:
  types (`Scenario`, `Expectation`, `SimulatedEvent` → rename to `MockEvent`
  only if cheap; keep YAML/JSON keys identical), YAML/JSON parsing, discovery
  (`loader.go`), `CheckExpectations`, false-pass analysis, message
  expectations, and the runner (today's `scenariotemporal` backend). Suggested:
  move `scenariotemporal` → `internal/workflow/scenario/runner` (or merge) and
  the reusable parts of `runtime/simulator/{types,loader,messages}.go` into
  `internal/workflow/scenario`.
- **Delete**: `internal/workflow/runtime/simulator/engine.go` (the graph
  walker), `runtime/simulator.go`, `runtime/simulator_*.go`
  (`simulator_spawn.go`, `simulator_compaction.go`), the core-parity /
  semantic-parity tests that only compare the two engines, and every comment
  that says "the fast simulator …". `v2.SimWorkflowLoader` becomes the runner's
  loader type.
- **All callers use the one runner**:
  - CLI `reliant workflow scenario run` (cmd/reliant/commands/workflow.go).
  - gRPC `WorkflowService.RunScenario` (+ any other scenario RPC that
    executes) in internal/grpc/services/scenario.go.
  - Agent tool `run_scenario` (internal/llm/tools/scenario_tools.go).
  - Builtin scenario tests: `builtin.TestBuiltinWorkflowScenarios` and
    `TestBuiltinScenarios_RealRuntime` collapse into ONE test that runs every
    builtin scenario on the runner, with no "known gaps" list.
- **Production-safety of the testsuite env**: the runner runs inside API
  processes (RPC, agent tool). Verify and handle: per-run isolation (fresh
  env per scenario), concurrency (many runs in parallel; the SDK test env is
  per-instance — confirm no process-global state, e.g. the global activity
  schema registry the earlier refactor avoided), panics contained and returned
  as a scenario error, a wall-clock timeout per scenario (context +
  env.SetTestTimeout), and no network/DB access (every activity the workflow
  can dispatch is mocked or a pure local function; an unmocked activity =
  scenario error, never a real call). Measure per-scenario latency on the
  builtin corpus and report it (the simulator was "fast"; we need the runner to
  be fast enough for interactive use from the builder UI).

## Close the runner gaps (knownRealRuntimeGaps must end empty)

1. **Preset-mode routers** (default-router ×3): mock `selected_preset`
   decisions the same way node-mode routing decisions are mocked.
2. **`black_box: true` on a loop node** (pitch-deck ×10): black-box a loop
   node as a unit — the scenario event supplies the loop's aggregate output
   (`_results`/declared outputs/`_iterations`), the body does not run. Mirror
   how `ref:` nodes are black-boxed (inlineBlackBoxedRefs), at the loop node.
3. **Events keyed by ref URL** (parallel-loop-sample ×2): decide semantics
   deliberately — either support `builtin://agent`-keyed events (all nodes
   whose ref is that URL), or migrate those scenarios to node-path keys and
   make ref-URL keys a validation error. Prefer node paths (explicit) unless
   ref-URL keying is documented user-facing syntax; check docs/scenario-schema.
4. **`agent/manual_mode_denied`**: the runtime re-enters the agent loop after a
   denied approval (while sees the denied iteration's tool_calls). Decide
   which is CORRECT product behavior: after a user denies tool execution,
   should the agent get another turn (it currently gets the tool calls with no
   results → the model is re-prompted), or should the loop end and yield to the
   user? Read agent.yaml's while/approval comments and the approval UX; if the
   runtime behavior is right, fix the scenario (add the call_llm event); if it
   is wrong, fix agent.yaml. Report the decision and reasoning.

## Scenario validation

`ValidateScenario` (unknown node ids in events/expectations, invalid start_at)
must keep working against the real graph (it only reads the workflow
definition — move it with the types).

## Tests / gates

- Every builtin scenario (scenarios/** + testdata) passes on the single runner.
- The CLI, RPC and agent tool each have a test proving they execute on the
  runner (e.g. a scenario whose outcome differs between the old simulator and
  the runtime — the loop-while-reads-nodes shape is a good one).
- Concurrency test: N scenarios in parallel in one process, all correct.
- Timeout test: a scenario that would hang returns a timeout error.
- `go build ./... && go vet ./... && DATABASE_URL=<55434> go test ./internal/workflow/... ./internal/llm/tools/... ./internal/grpc/... ./cmd/... -count=1`.
- Replay fixtures unaffected (TestReplayFixtures).
- `make generate` targets that document scenarios (generate-scenario-schema,
  mintlify reference) regenerated if types move — regenerate ONLY the scenario
  schema outputs and verify the diff; do not blanket-regenerate unrelated docs.
