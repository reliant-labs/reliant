# Workflow history size: keeping bulk data out of Temporal

Status: in progress (branch `workflow-history`). No backwards compatibility:
in-flight runs are not replay-protected; replay fixtures are regenerated.

## Incident

Chat `0e15fdba-dd25-4905-a743-13ec904a1daf`: run `f5c4569e` was terminated by
Temporal at **52.4 MB / 38,108 events** — the SIZE cap (50 MB) bound, not the
51,200-event cap.

| Where the bytes are | Share |
|---|---|
| `SaveMessage` activities (2,809 — two per LLM turn) | ~47% |
| `ExecuteTools` | ~21% |
| `CallLLM` | ~20% |

Heavy payloads, each stored at least twice (producer result, then again as
`SaveMessage` input): tool result `content` ~17.6 MB, thinking `signature`
~11.5 MB (one was 107 KB; nothing in any workflow reads it). Payloads ≥4 KB
are 77% of payload bytes; zlib compresses them ~2.1x.

## Design: two uniform rules + one transparent layer

### Rule 1 — a node's `save_message` is written by whoever executes the node

- **Activity-backed nodes** (call_llm, execute_tools, run, invoke_tool,
  compact, …): the **`ActivityWrapper`** (runtime/registry.go) writes the
  message right after the activity returns, in the worker, for EVERY
  activity, with no per-activity code. The workflow never dispatches a
  separate `SaveMessage` for them.
- **Workflow-assembled outputs** (workflow / loop / router / ask_question
  nodes, and an execute_tools batch that contains spawn/ask_user calls, whose
  combined result is built workflow-side in `executeToolsWithSpawnSupport`):
  the workflow writes it via the `SaveMessage` activity, as today — the
  workflow is the executor that produced that output.
- One evaluator (`evaluateSaveMessageConfig` + condition), one CEL
  environment, both places.

No analyzer, no eligibility, no version gate, no "did it save?" flag: the
workflow decides deterministically at dispatch whether the save is delegated
(it attached the request), and the activity either succeeds (message
written) or fails (Temporal retries it).

**save_message CEL environment = `output`, `inputs`, `workflow`, `iter`.**
`nodes` is removed (validation rejects it; no builtin or example uses it, and
reading sibling outputs at completion time was racy anyway).

**What the activity needs** travels in `RuntimeContext.SaveMessage`
(`types.SaveMessageRequest`), attached by the StepExecutor at dispatch:
- `Config` — the node's `save_message`, verbatim (protojson, like
  `ActivityInput.Node`). Carried explicitly because the ExecuteTools path
  rebuilds its node without `save_message`.
- `Inputs` — only the `inputs.<key>`s the config references (bare `inputs` or
  a dynamic index ⇒ the whole map). The full inputs map is several KB; sending
  it on every CallLLM would re-bloat the history this exists to shrink.
- `Iter`, `Workflow` — tiny; always sent.
- `StepID` — `"<node>-save"` (the UI keys on that step_executions row's
  `message_id`), `AgentName`.
The run node's flat-map input carries the same request under a key.

**Wrapper save, precisely:**
1. Result → map exactly as the workflow would see it (protojson
   `UseProtoNames` → `map[string]any`, then the same normalization
   `StepExecutor.normalizeOutput` applies — extract it to a package func used
   by both).
2. Condition → evaluate → skip on empty role or content-free assistant (same
   guard as today).
3. Write through the SAME function `SaveMessageActivity` uses (refactor its
   body into a reusable writer). `runtime` cannot import `handlers` or
   `threads` (`threads` depends on `runtime`), so the writer is injected into
   the `ActivityRegistry` at registration (activities/register.go).
   Idempotency: `workflowID-runID-<producerActivityID>-save`, attempt number
   from activity info (existing delete-and-recreate-on-retry semantics),
   pre-allocated `AssistantMessageID` convergence unchanged.
4. Record the `"<node>-save"` step_executions row
   (`{message_id, thread, thread_token_count, message_count}`).
5. A write failure is retried inside the wrapper (short backoff, bounded
   ~15 s) then fails the activity. ExecuteTools retries are safe (terminal
   idempotency returns recorded tool results); a CallLLM retry re-streams
   under the same message id.
The interrupted-turn partial (`CallLLM.persistInterruptedTurn`) is unchanged.

### Rule 2 — message-only fields never go back to the workflow

A field annotated `[(reliant) = {message_only: true}]` on an activity output
proto is persisted with the message (it is in the map the wrapper evaluates)
and then **cleared** from the result the workflow receives — generically, by
protoreflect, for every activity. The CEL type registry excludes
message-only fields, so validation rejects `nodes.x.<field>`.
`CallLLMOutput.thinking` is message-only. Tool result `content` is NOT — it
is real output that workflows read; the codec handles its size.

### Layer — claim-check payload codec (done)

`internal/temporal/claimcheck`: any payload ≥ threshold is zstd-compressed,
stored in Postgres (`temporal_payload_blobs`, content-addressed), and replaced
by a reference; decode is transparent, so CEL/templates/resume see identical
values. Wired in api-server + worker; GC (30 d horizon) in api-server.
Threshold to be tuned from the post-change measurement.

## Validation

- Unit: wrapper save — result→map parity with the workflow's view,
  condition/role/content-free skips, message-only stripping, write retry,
  step row.
- Temporal testsuite: agent loop → NO `SaveMessage` activity for call_llm /
  regular-tools execute_tools; mixed spawn batch still saves workflow-side;
  messages land with thinking + full tool content.
- Validation: `nodes.*` in save_message rejected; `nodes.x.thinking` rejected.
- Replay fixtures regenerated (`make replay-fixtures`, DB on 55434) and green.
- Measure `historySizeBytes` before/after on the fixture generator.

Test DB: `DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55434/postgres?sslmode=disable`
(container `wfhist-test-pg`; NEVER 5434 — real data).
