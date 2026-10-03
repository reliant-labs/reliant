# call_llm `stop_reason`: one normalized field for "why did this turn end"

Status: approved design, in implementation. Research behind it:
`specs/research-agent-loop-stop-semantics.md` (opencode / crush / Codex CLI).

## Problem

Chat `b43b41fe-8963-489d-a28e-55b5336b97be` (gpt-5.6-terra via the `codex`
driver) ended its final turn with text — "…I'll inspect the local dev auth
setup before attempting the workflow again." — and no tool calls. The codex
driver reported `end_turn`, `deriveStopKind` made that `stop_kind=complete`,
and `agent.yaml`'s while-condition exited the loop. The model was not done.

Two defects:

1. **The loop decision is spread across five fields.** `agent.yaml` continues on
   `size(tool_calls) > 0 || has_feedback || pending_inbox || aborted ||
   stop_kind == 'truncated'`; `structured-agent.yaml` has its own negative list.
   Absent-field hazards forced opposite match polarities in each file.
2. **We drop OpenAI's "keep going" signal.** The Responses API puts an
   (undocumented, SDK-untyped) `end_turn` boolean on the response object in
   `response.completed`. Codex CLI continues when `end_turn == false`, even with
   zero tool calls, and treats `response.incomplete` with reason `interrupted`
   the same way. Our codex driver never reads `end_turn`, and it only handles
   `response.completed` — `response.incomplete` / `response.failed` events are
   silently ignored, so `finalResp` is nil and the turn reads as `end_turn`.

## Design

### The field

`CallLLMOutput.stop_reason` (string, proto field 18). Closed vocabulary,
computed ONCE in Go, first matching row wins:

| stop_reason   | When                                                                 |
|---------------|----------------------------------------------------------------------|
| `interrupted` | our stream was cut short (`streamInterrupted`; today's `aborted`)    |
| `tool_use`    | ≥1 finished tool call                                                |
| `refused`     | FinishReasonRefusal                                                  |
| `truncated`   | FinishReasonMaxTokens                                                |
| `incomplete`  | FinishReasonPauseTurn AND the turn produced non-empty response text  |
| `error`       | FinishReasonPauseTurn with no text, Cancelled, Error, ToolUseError, PermissionDenied, Unknown, "" and anything new |
| `done`        | FinishReasonEndTurn, or FinishReasonToolUse with zero surviving calls |

Why this order:
- `interrupted` first: matches today, where `aborted` wins regardless.
- `tool_use` before the failure kinds: if the model requested tools, the tools
  run (edges route on `size(tool_calls)`), and their results are new input, so
  the next request can never be identical. Today's agent loop already continues
  on any tool call regardless of finish reason; this keeps that.
- `incomplete` requires text: a "keep going" turn that produced nothing adds
  nothing to history, so re-calling would send an identical request. That is
  the only spin risk, and it maps to `error` (stop) instead of needing a turn
  cap. Codex CLI has no cap either.

`aborted` (field 15) and `stop_kind` (field 16) are REMOVED from
`CallLLMOutput` and reserved. `finish_reason` (17, raw provider value) stays as
the escape hatch.

### Absent values

Every `CallLLM` output map passes through the runtime normalizer
(`runtime.normalizeActivityOutput`) and the scenario runner's
`normalizeOutput`. Both call `stopreason.FillDefault(output)`: if `stop_reason`
is absent or `""`, set it to `tool_use` when `tool_calls` is non-empty, else
`done`. So CEL never sees an empty value and no workflow needs positive/negative
match gymnastics. This covers scenario fixtures that only set `tool_calls`.

### Package

`internal/workflow/stopreason` — a leaf package (imports only
`internal/models/message`):

```go
const (Interrupted="interrupted"; ToolUse="tool_use"; Refused="refused";
       Truncated="truncated"; Incomplete="incomplete"; Error="error"; Done="done")
type Turn struct { FinishReason message.FinishReason; ToolCalls int; Interrupted bool; ProducedText bool }
func Derive(t Turn) string
func FillDefault(output map[string]interface{}) // in place; no-op when set
```

### Raw provider mapping (drivers)

`message.FinishReasonPauseTurn` becomes "the provider paused the turn and
expects the conversation handed back": Anthropic `pause_turn`, AND OpenAI
Responses `end_turn == false` or `incomplete` with reason `interrupted`.

Responses status mapping (shared helper used by codex + openai drivers):
- `completed` + `end_turn: false` → PauseTurn (unless tool calls → ToolUse)
- `completed` otherwise → EndTurn / ToolUse
- `incomplete` + `max_output_tokens` → MaxTokens
- `incomplete` + `content_filter` → Refusal
- `incomplete` + `interrupted` → PauseTurn
- `incomplete` + anything else → Unknown (logged)
- `failed` → Error; `cancelled` → Cancelled

### Continuing an incomplete turn

An `incomplete` turn leaves history ending with the assistant's own message.
`call_llm` currently YIELDS on an assistant-tailed history (returns without
calling the provider) — that guard exists to unwedge chats and must stay.

New `CallLLMArgs.continue_turn` (CelBool, field 15): "the previous turn asked to
continue; an assistant-tailed history is expected — call the provider." When
true, the guard is bypassed. Explicit and declarative: the loop that saw
`incomplete` is the one that says so. Iteration 0 / a fresh run reads false.

### Workflows

```yaml
# agent.yaml — tool-presence loop
while: outputs.stop_reason in ['tool_use', 'incomplete', 'truncated', 'interrupted']
       || outputs.has_feedback == true || outputs.pending_inbox == true
outputs:
  stop_reason: "{{has(nodes.call_llm) && has(nodes.call_llm.stop_reason) ? nodes.call_llm.stop_reason : 'done'}}"
call_llm args:
  continue_turn: "{{has(outputs.stop_reason) && outputs.stop_reason == 'incomplete'}}"
ask_question edge:
  size(nodes.call_llm.tool_calls) == 0 && nodes.call_llm.stop_reason != 'incomplete' && inputs.ask

# structured-agent.yaml — sentinel loop, behavior unchanged
while: (outputs.completed != true || outputs.has_feedback == true || outputs.pending_inbox == true)
       && !(outputs.stop_reason in ['truncated', 'refused'])
       && (inputs.max_turns == 0 || iter.iteration < inputs.max_turns)
```

Structured-agent keeps `completed` (the response tool's payload validated by
execute_tools) as its exit condition; an `incomplete` turn there still routes to
`remind_response`, whose user message makes the next request non-identical.

### Behavior change matrix (agent.yaml)

| Turn                                   | Before    | After     |
|----------------------------------------|-----------|-----------|
| tool calls, any finish reason          | continue  | continue  |
| text, end_turn                         | exit      | exit      |
| text, OpenAI end_turn=false            | exit      | **continue** |
| text, Anthropic pause_turn             | exit      | **continue** |
| pause/end_turn=false with no text      | exit      | exit (error) |
| truncated, no tools                    | continue  | continue  |
| interrupted                            | continue  | continue  |
| refused / error, no tools              | exit      | exit      |

structured-agent: identical before/after.

### Replay safety

Removing `aborted`/`stop_kind` from `CallLLMOutput` broke the
`router_dispatch` replay fixture: `router_executor` decodes the RECORDED
CallLLM payload into a typed `reliantv1.CallLLMOutput`, and the data converter
rejected the now-unknown `stop_kind` key (TMPRL1100 → wedged in-flight run on
deploy). Fixed at the root: `internal/temporal/data_converter.go` sets
`AllowUnknownFields`, so retiring (reserving) any proto field is replay-safe.
All six fixtures replay unchanged — no regeneration needed.

### No turn cap

`incomplete` has no consecutive-turn cap. Each continued turn appends the
model's own text to history, so the next request always differs; the one
non-progressing shape (a pause with no text) maps to `error` and stops. This
matches Codex CLI, which continues on every `end_turn:false` uncapped.

## Out of scope (follow-ups)

- Codex/OpenAI drivers replay assistant history as `role: user` and drop
  `phase` (`commentary` / `final_answer`). The SDK says to preserve and resend
  phase for gpt-5.3-codex+. Persisting phase needs storage (content block
  column + migration). Tracked separately.
- In-flight workflows started before deploy keep their recorded (old) YAML;
  they lose truncated/aborted continuation for their remaining iterations
  because `aborted`/`stop_kind` are no longer emitted. Tool-call continuation is
  unaffected.
