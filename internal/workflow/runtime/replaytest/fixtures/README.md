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
| `greenfield_probe.json` | `builtin://agent` | A chat's first turn: the start event carries `GreenfieldProbe: true`, and the run schedules the `GreenfieldProbe` activity after preflight/status bookkeeping and before its first CallLLM. Fixtures recorded before that field existed (every other one, until regenerated) decode it as false and schedule no probe — which is why the probe needs no version gate. |
| `spawn.json` | `builtin://agent` | Spawn: a spawn tool call dispatches the child agent detached (`dispatchSpawnBackground`), settling immediately with a handle; the parent's loop blocks without spinning (`InlineLoopExecutor.awaitLiveDetachedSpawns`) until the detached child's completion lands in its mailbox, then reacts to it on its next turn. |
| `spawn_failure.json` | `builtin://agent` | Sub-agent failure isolation: the spawned agent's `CallLLM` fails (the scripted driver fails that agent's requests only — `ScriptedLLM.FailWhen`), its exhausted step ends THAT agent (`subagent-exhaustion-fails-agent` marker, a thread-scoped `WorkflowError`, the failed report into the parent's mailbox, the spawn's terminal "failed"), and the parent — never paused — takes one more turn and completes. |
| `action_approval.json` | `builtin://agent` (`tools: [http__request]`) | Action approval gate: an attended turn calls a mutating integration action, so the batch first raises an approval (`ApprovalCreate`, a timer, `signal.approval.*`); it is denied, and `ExecuteTools` refuses the call (`refused_tool_calls`) before the next turn completes the run. |
| `late_user_message.json` | `builtin://agent` | Late wake: a `thread_wake` signal lands while the run's only (tool-less) turn is in flight, with nothing queued for its `pending_inbox` probe and nothing live. The loop-exit gate re-enters for it (`late-user-wake-gets-a-turn` version marker, a second `CallLLM`) instead of completing. A history recorded before that change — one `CallLLM`, then completion, with the signal unanswered — must keep replaying that way; that is what the version marker is for. |
| `machine_wait.json` | `builtin://agent` (a `RemoteExecutor` over a router whose machine attaches 65s after the first check; text-only) | A run held for a machine that is still starting: the first `PreflightDaemonCheck` polls a whole 60s slice and reports `waiting`, the run sleeps on its 30s recheck timer, and the next check finds the machine up before the first `CallLLM`. |
| `machine_wait_signaled.json` | same | The same wait woken by its machine: the `machine_wait` signal (sent by the api-server when one of the user's machines connects) lands while the run sleeps on the recheck timer, the timer is cancelled, and the check runs at once. |

## Frozen sets

`frozen/<set>/*.json` are histories that runs ALREADY IN FLIGHT were recorded
with. `TestReplayFixtures` replays them exactly like the current set, but
nothing ever regenerates them, and `TestFrozenFixturesAreUnchanged` pins their
bytes (each set's `SHA256SUMS`). Each set has a README naming the shape it
pins and the `workflow.GetVersion` gate that keeps it replaying.

They exist because the current set cannot catch a break on its own: it is
rewritten by `make replay-fixtures`, so it only proves the code replays
histories the same code just recorded. #641 reordered the run's startup
(preflight after "started"), regenerated every fixture, passed this suite, and
on the 2026-10-09 deploy wedged every run in flight with the old order —
chats 97654413 and 3f03dc31 retried their workflow task with TMPRL1100 for
hours while showing as active. `frozen/2026-10-08-preflight-before-started` is
that shape.

| Set | Pins | Kept replaying by |
|---|---|---|
| `2026-10-08-preflight-before-started` | Every fixture at `fbca55fe^`: `PreflightDaemonCheck` scheduled before the "started" `WorkflowStatus` | `preflightAfterStartedChangeID` (`runtime/workflow.go`) |
| `2026-10-10-machine-wait-ten-minute-budget` | `machine_wait.json` at `43c3e8fc`: the old 10-minute wait, 60s slices with a 2s timer between them | No gate: the durable wait issues the same commands (a timer is matched by id, not duration; an activity by type, not input) — this set is the proof |
| `2026-10-10-subagent-exhaustion-pauses-run` | `spawn_failure.json` recorded at `78bfc941`: a sub-agent's exhausted step self-pauses the whole run; a resume re-dispatches it | `subAgentFailsAloneChangeID` (`runtime/subagent_failure.go`) |

Retire a set only together with its gate, once no run older than the gate's
deploy can still be open.

## When `TestReplayFixtures` fails

Your change made `DynamicWorkflow` (or code it calls **inside the workflow
sandbox** — executors, routers, CEL/template evaluation that gates commands)
emit a different command sequence for at least one recorded history. If it
deploys, every in-flight run recorded with that shape fails its next workflow
task with TMPRL1100 and retries it forever.

Things that break replay: adding/removing/reordering `workflow.ExecuteActivity`
calls, changing an activity's registered name, adding/removing timers
(`workflow.Sleep`, `workflow.NewTimer`), changing `workflow.Go` coroutine
structure, changing side effects, or changing any branch condition that gates
the above. Things that do NOT break replay: activity *implementation* changes,
changes to values that don't alter the command sequence (activity inputs and
options included, and a timer's duration — a replayed timer is matched by its
id), logging. If you rely on that to skip a gate, prove it the way
`frozen/2026-10-10-machine-wait-ten-minute-budget` does: record the old shape
with the old code, freeze it, and replay it against the new.

**Make the change replay-compatible, then freeze the old shape:**

1. Gate the change with `workflow.GetVersion(ctx, "<change-id>",
   workflow.DefaultVersion, 1)`: a history without your marker keeps the order
   it was recorded in; a new run records the marker and takes the new path.
   Call it at the point where the two paths first differ, and only on the
   path that differs (the preflight gate is only consulted when the run needs
   a daemon). The runtime already carries a dozen of these; follow them.
2. Freeze the current fixtures BEFORE regenerating — they are the shape the
   in-flight runs have:

   ```
   make freeze-replay-fixtures NAME=<yyyy-mm-dd>-<what-changed>
   ```

   and add a README to the new set (shape, gate, incident if any).
3. Regenerate the current set: `make replay-fixtures`. The new fixtures carry
   your marker; the frozen set still replays without it.

### Marker-less histories of two different shapes

A gate only distinguishes "recorded before the gate" from "recorded with it".
If a break has ALREADY shipped ungated — as #641 did — there are two
marker-less shapes in flight, the one before the break and the one the break
recorded, and no `GetVersion` call can tell them apart. Nothing a workflow can
read deterministically before the divergence (start time, inputs, build id)
reliably separates them either: #641's own fixtures were recorded before prod
chats that still ran the old code. Pick the population to keep (for #641: the
older one — every long-lived parked chat), freeze it, and rely on recovery for
the other: the reconciler terminates a run whose latest workflow task failed
TMPRL1100, and the resume path refuses to reset-and-replay such a history
(`ErrReplayDiverged`) and starts a fresh execution at the checkpoint instead.
That recovery loses the old run's in-memory node outputs and costs a visible
interruption, which is why the gate comes first.

## Regenerating

`make replay-fixtures` — runs the build-tagged generator
(`go test -tags replayfixtures ./internal/workflow/runtime/replaytest/`, which
boots an ephemeral Temporal dev server per run and drives every scenario with
the scripted LLM — no model is ever called), rewrites every `fixtures/*.json`,
then runs the untagged replay test to verify the new fixtures replay cleanly
against the current code.

### Which Postgres it uses

The generator needs a Postgres it can migrate and write to; `DATABASE_URL`
decides which:

- **`DATABASE_URL` set** — that database is used as-is and nothing is started.
  This is the mode for anyone who must not touch shared infrastructure (an
  agent, a second worktree): point it at an isolated database you created.

  ```
  DATABASE_URL='postgres://postgres:postgres@localhost:55434/reliant_mine?sslmode=disable' \
    make replay-fixtures
  ```

  `make replay-fixtures DATABASE_URL=...` is equivalent.
- **`DATABASE_URL` unset (or empty)** — `docker compose up -d postgres` brings
  up this repo's compose Postgres, published on `localhost:5433`, and the
  generator uses its `reliant` database. That server is shared by every
  `scripts/dev.sh` stack on the machine, so prefer the first mode whenever you
  have a database of your own.

That choice is the Makefile's, and it is the same for every Make target that
runs Go tests (see "Which Postgres the Go tests use" in the `Makefile`). The
generator itself never picks a server: run by hand without `DATABASE_URL`, it
exits with an error instead.

The two steps are also fine to run by hand, which is all the target does. Only
the first needs a database; the replay check is hermetic:

```
DATABASE_URL=... go test -tags replayfixtures -count=1 -timeout=10m -v ./internal/workflow/runtime/replaytest/
go test -count=1 -timeout=5m -v -run TestReplayFixtures ./internal/workflow/runtime/replaytest/
```

Add `-run TestGenerateFixture_PauseResume` (for example) to the first to
regenerate a single fixture.

Add a new fixture by adding a `TestGenerateFixture_*` scenario in
`generate_gen_test.go` — prefer shapes that mirror an e2e story
(`e2e/stories/`) so the pinned history corresponds to a flow that is verified
end-to-end.

### The scripted LLM is shared — auxiliary requests must not consume turns

A scenario's `Turn`s are for the AGENT LOOP. Other production code paths share
the same injected driver and are not part of that sequence: the compaction
summary, and **chat title generation**, which `StartChat` dispatches as its
own workflow that races the agent loop. `ScriptedLLM.StreamResponse` recognizes
each and answers it with a canned reply instead of advancing the script.

This is the sharp edge, and it has drawn blood once. Titling used to call
`SendMessages`, so it stayed off the scripted path by construction; #229
switched it to `accumulator.StreamAndAccumulate` (the Codex backend requires
`stream: true`) and it silently began consuming turn 1 of every scenario. Each
fixture then recorded a shape one turn short — `agent_tool_loop` lost its
`ExecuteTools` entirely — and those truncated histories still replayed **green**,
so the suite looked healthy while pinning the wrong contract.

Two guards in `ExportHistory` now make that loud instead of silent: a scenario
that runs past the end of its script, or that consumes fewer turns than it
scripted, **refuses to export** rather than overwriting a good fixture with a
degenerate one. If you add a consumer that calls the LLM outside the agent
loop, teach `StreamResponse` to recognize it — identify it by something
structural (the tool the request is pinned to), not by prompt wording, which
drifts.

Sanity-check a regenerated fixture by its activity mix, where a truncated shape
is obvious at a glance:

```
jq -r '.events[] | select(.eventType=="EVENT_TYPE_ACTIVITY_TASK_SCHEDULED")
       | .activityTaskScheduledEventAttributes.activityType.name' \
  fixtures/agent_tool_loop.json | sort | uniq -c
```

### The generator needs a synced config snapshot

`newHarness` writes a `project_configs` row under a non-seed daemon id, standing
in for the daemon's config push. Without it `Config.SnapshotSynced` stays false,
and a node that preloads skills (the spawn scenario's `general` child requests
`general-agent`) treats the empty catalog as *not yet known* and therefore
RETRYABLE — so `CallLLM` retries to its limit and the workflow fails. The
generator has no daemon, so an empty snapshot from a real-looking daemon is the
truthful answer: a daemon has reported, and this project genuinely has no skills.

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
identical either way and both variants replay cleanly. `spawn.json` is the
other exception, and there the command order itself varies: the parent's
exit-candidate turn races the child's only turn (see
`TestGenerateFixture_Spawn`), so the two threads' activities interleave
differently from one generation to the next. Every interleaving replays
cleanly against the same code — 24 independent generations were replayed to
check — so a reordered `spawn.json` diff is not a change by itself; compare its
activity mix instead. Whitespace/key-order
of the JSON is normalized at export. Review regeneration diffs by event-type
sequence, e.g.:

```
jq -r '.events[].eventType' fixtures/agent_tool_loop.json
```

## Caveats

- The replay test only covers the shapes captured here. A non-deterministic
  change on a path no fixture exercises (e.g. parallel loops, multiple
  concurrent spawns, daemon-offline breaker) will not be caught — add a
  fixture when you add or materially change such a path.
- Fixtures pin the workflow-side contract of activity *interfaces* recorded in
  history (names, payload decoding), not activity implementations.

## Replaying a deployed environment's live runs

The fixtures pin shapes recorded in a harness. They cannot tell you whether the
runs that actually exist in an environment will replay on a build. That is the
question for every release, and the first one in every wedge investigation.
`TestReplayRecordedHistory` (`prod_history_replay_test.go`, build tag
`prodreplay`) answers it from histories downloaded out of that environment's
Temporal:

```
temporal workflow show --workflow-id <id> --output json > /tmp/h/<id>.json
REPLAY_HISTORY=/tmp/h \
REPLAY_PAYLOAD_DSN='postgres://…/reliant?sslmode=disable&options=-c%20default_transaction_read_only%3Don' \
  go test -tags prodreplay -run TestReplayRecordedHistory -v -count=1 \
  ./internal/workflow/runtime/replaytest/
```

`REPLAY_PAYLOAD_DSN` points at the environment's reliant database, which holds
the claim-checked payloads. The harness only reads it. Replay re-encodes
re-issued activity inputs, and those writes are discarded on purpose. A
read-only connection that is not wrapped this way panics inside
`ExecuteActivity`, with "yield during panic unwinding", and that looks exactly
like a replay break.

Histories and payloads are user data, so they are never committed. A shape
worth keeping goes in as a frozen fixture recorded by the generator.

Measured on 2026-10-10 against prod's two live runs, 97654413 (11,319 events)
and 098c210d: both fail with TMPRL1100 at ActivityId 11 on `cc4ef48d` (the
build before #672), and both pass on `d3b903f1`. Prod was then running
`4c6f6d3e`, which differs from `d3b903f1` only in `internal/db`.
