# Research: how coding-agent CLIs decide CONTINUE vs END after an LLM turn

Research only; nothing in reliant was changed. Sources are shallow clones taken on the research date:

| Repo | Commit |
|---|---|
| openai/codex | `b741e480e203` |
| sst/opencode | `907b3bc518fa` |
| charmbracelet/crush | `bdcf796cb1ff` |
| charmbracelet/fantasy | `d272c40391c5` |
| openai/openai-python | HEAD at clone time |

Line numbers refer to those commits.

## TL;DR

- **No project continues the loop just because the model *said* it would act.** In all three, a turn that has text and no tool call ends the loop when the provider reports a normal finish.
- **Codex is the only one that reads an OpenAI-side "keep going" signal: `end_turn`.** This is a top-level boolean on the Responses `response.completed` payload, not on the message item. When `end_turn == false`, Codex sets `needs_follow_up = true` and samples again even though there were no tool calls. A `response.incomplete` with reason `interrupted` is also mapped to `end_turn = Some(false)`.
- **`phase: "commentary" | "final_answer"` exists on assistant message items** and is in the public OpenAI SDK. Codex does **not** use `phase` to decide whether to loop. It uses `phase` for mailbox/steer delivery (commentary keeps the current turn open to injected input), for choosing which message counts as the final answer for sub-agents, and it round-trips `phase` back to the API.
- **Implication for reliant:** the GPT-5.x turn ("I'll inspect the local dev auth setup…", no tool calls) most likely arrived as a message with `phase: "commentary"` and/or `end_turn: false`. Codex would have looped on `end_turn:false`. To match that, reliant needs to:
  1. capture `end_turn` from `response.completed`;
  2. capture `phase` per message;
  3. resend `phase` on assistant history items, because the SDK says dropping it degrades gpt-5.3-codex+;
  4. add a "provider requests continuation" condition to the loop.

  This has not been verified on a captured payload. Check the raw response for this chat before building on it.

## 1. opencode (`packages/opencode/src/session/prompt.ts`)

The loop exits before calling the model when the last assistant message has a finish reason, that reason is not `tool-calls` or `unknown`, and it has no tool parts (`prompt.ts:1111-1116`):

```ts
if (
  lastAssistant?.finish &&
  !["tool-calls", "unknown"].includes(lastAssistant.finish) &&
  !hasToolCalls &&
  lastAssistant.parentID === lastUser.id
) { ... break }
```

After each step (`prompt.ts:1295-1333`), `finished = finish && !["tool-calls","unknown"].includes(finish)`. That flag only triggers error surfacing (content-filter becomes `ContentFilterError`; json_schema with no output becomes `StructuredOutputError`). The step then returns `"break"` if the processor result is `"stop"`, and `"continue"` otherwise. Compaction is also handled here, with `overflow: !finish`.

So opencode looks at both signals:

- It continues if the finish is `tool-calls` OR `unknown`, or if any tool part is present.
- `length` and `stop` count as finished, so `length` ends the loop. It is not auto-continued.
- `unknown` causes another iteration.

Responses-API mapping (`packages/llm/src/protocols/openai-responses.ts:523-528`):

```ts
const reason = event.response?.incomplete_details?.reason
if (reason == null) return hasFunctionCall ? "tool-calls" : "stop"
if (reason === "max_output_tokens") return "length"
if (reason === "content_filter") return "content-filter"
return hasFunctionCall ? "tool-calls" : "unknown"
```

- Text with no function call and no `incomplete_details` maps to `"stop"`, which ends the loop. opencode does not read `end_turn` or `phase`.
- An unrecognised incomplete reason (e.g. `steered`) maps to `unknown`, which continues the loop.

Anthropic mapping: `anthropic-messages.ts:559` maps `end_turn | stop_sequence | pause_turn` to `"stop"`. That means **`pause_turn` ends the loop**, and the server-tool continuation is lost.

## 2. crush + fantasy

crush delegates the loop to `fantasy.Agent.Stream`. crush's `StopWhen` (`crush/internal/agent/agent.go:1095+`) only contains a context-window/auto-summarize condition. Its `OnStepFinish` (`agent.go:1044-1075`) maps fantasy reasons to UI reasons (`Length→MaxTokens`, `Stop→EndTurn`, `ToolCalls→ToolUse`, content filter, default `Unknown`). This mapping is display-only.

The loop decision is in fantasy (`fantasy/agent.go:1763`):

```go
shouldContinue := len(stepToolCalls) > 0 && stepFinishReason == FinishReasonToolCalls && !hasStopTurn(toolResults)
```

The non-streaming loop condition is the same (`agent.go:638`):

```go
if shouldStop || stopTurnRequested || len(stepToolCalls) == 0 || result.FinishReason != FinishReasonToolCalls { break }
```

This is the strictest rule of the three: **both** tool calls AND `FinishReasonToolCalls` are required.

- `Length`, `Error`, `ContentFilter` and `Unknown` are treated as abnormal (`agent.go:555-558`, `1639-1642`). Tool calls are only dispatched when the finish is `ToolCalls` (`agent.go:1705`, CHARM-2020), so tool calls on a truncated or unknown finish are suppressed and the loop stops.
- Anthropic `pause_turn` is mapped to `FinishReasonStop` (`providers/anthropic/anthropic.go:1304`), so it ends the loop.
- There is no `end_turn`/`phase` handling.

## 3. OpenAI Codex CLI (`codex-rs`)

**Per-item.** `stream_events_utils.rs:356` sets `output.needs_follow_up = true` for every dispatched tool call. `stream_events_utils.rs:424` does the same for a tool error that is answered back to the model (`RespondToModel`). A plain message (`Ok(None)`, ~line 360) only records `last_agent_message` and never sets follow-up.

**Per-response, the `end_turn` signal.** The SSE parser (`codex-api/src/sse/responses.rs:108-115`) deserializes it:

```rust
struct ResponseCompleted { id: String, usage: ..., usage_metadata: ..., #[serde(default)] end_turn: Option<bool> }
```

`responses.rs:418-455` handles completion:

```rust
"response.completed" | "response.incomplete" => {
    let interrupted = event.kind == "response.incomplete";
    // incomplete reasons: content_filter -> ApiError::ContentFilter;
    // anything other than "interrupted" -> ApiError::Stream("Incomplete response returned, reason: …")
    ...
    end_turn: if interrupted { Some(false) } else { resp.end_turn },
```

`core/src/session/turn.rs:3009-3015` consumes it:

```rust
if let Some(false) = end_turn {
    needs_follow_up = true;
}
break Ok(SamplingRequestResult { needs_follow_up, last_agent_message });
```

**Loop.** `turn.rs:566`: `let needs_follow_up = model_needs_follow_up || has_pending_input;`. If true, the loop continues (with optional auto-compact). At `turn.rs:653`, `if !needs_follow_up { … run_turn_stop_hooks … }` ends the turn. Stop hooks can block the stop and force another iteration.

**Signal semantics:**

| Signal | Effect |
|---|---|
| `end_turn: Some(false)` | Continue, even with zero tool calls. |
| `end_turn: None` | Falls back to "tool calls ⇒ continue" (legacy models). |
| `end_turn: Some(true)` | No effect beyond the tool-call rule. |
| `incomplete` + `max_output_tokens` (or other non-`interrupted` reason) | Stream **error** (retry path), not a silent stop. |
| `incomplete` + `interrupted` | Continue. |

**`phase`.** `protocol/src/models.rs:940-953`:

```rust
/// Classifies an assistant message as interim commentary or final answer text.
/// Providers do not emit this consistently, so callers must treat `None` as
/// "phase unknown" and keep compatibility behavior for legacy models.
pub enum MessagePhase {
    /// Mid-turn assistant text (for example preamble/progress narration).
    /// Additional tool calls or assistant output may follow before turn completion.
    Commentary,
    /// The assistant's terminal answer text for the current turn.
    FinalAnswer,
}
```

Codex uses `phase` in these places:

- `stream_events_utils.rs:294`: `defers_mailbox_delivery_to_next_turn = !matches!(phase, Some(Commentary)) && text.is_some()`. Commentary keeps queued user/agent input deliverable within the current turn. A final or unknown phase defers it to the next turn (tests at `stream_events_utils_tests.rs:540-555`).
- `stream_events_utils.rs:535`, `agent/control/spawn.rs:92` and `user_authorization.rs:264`: commentary is excluded when picking a sub-agent's "final answer".
- `send_message_to_user_async.rs:89` emits `FinalAnswer`.
- It is stored on `ResponseItem::Message`/`ResponseInputItem::Message` (`models.rs:846, 1032`) and replayed to the API.

**`phase` does not affect `needs_follow_up`.** The loop-continuation decision is `end_turn` (plus tool calls and pending input). That said, models that emit `phase: "commentary"` presumably also emit `end_turn: false` on such responses. This is an inference, not verified.

**Which models:** the SDK docstring names "`gpt-5.3-codex` and beyond" for `phase`. `end_turn` does not appear in the openai-python SDK (`rg end_turn` returned nothing), so it is an undocumented or internal field that Codex reads with `#[serde(default)]`.

## 4. OpenAI Responses API types (openai-python)

`src/openai/types/responses/response_output_message.py`:

```python
role: Literal["assistant"]
status: Literal["in_progress", "completed", "incomplete"]
type: Literal["message"]
phase: Optional[Literal["commentary", "final_answer"]] = None
"""Labels an `assistant` message as intermediate commentary (`commentary`) or the
final answer (`final_answer`). For models like `gpt-5.3-codex` and beyond, when
sending follow-up requests, preserve and resend phase on all assistant messages
— dropping it can degrade performance. Not used for user messages."""
```

`src/openai/types/responses/response.py`:

- `status: Optional[ResponseStatus]` (completed / failed / in_progress / cancelled / queued / incomplete).
- `class IncompleteDetails: reason: Optional[Literal["max_output_tokens", "max_messages", "content_filter", "steered"]]`. The SDK says `steered` "means the response stopped at a safe output boundary after a WebSocket `response.steer` event. The server can then create a successor response automatically with the queued input."
- There is no `end_turn` in the SDK types. Codex additionally knows an `interrupted` incomplete reason that the SDK does not list.

## 5. Anthropic stop_reason

| `stop_reason` | Meaning |
|---|---|
| `end_turn` | Natural stop. |
| `tool_use` | Client tools requested. Continue. |
| `max_tokens` | Truncated. |
| `stop_sequence` | Custom stop sequence hit. |
| `pause_turn` | A long-running server-tool turn (web search, etc.) was paused. The client should resend the conversation, including the paused assistant content, to let it continue. |
| `refusal` | Refused. |

Both opencode and fantasy/crush fold `pause_turn` into "stop", so neither continues it. Codex is OpenAI-only.

## Comparison

| Project | Continue when | Stop when | Ambiguous / unknown |
|---|---|---|---|
| **opencode** | finish ∈ {`tool-calls`, `unknown`} OR tool parts present | any other finish (`stop`, `length`, `content-filter`) with no tool parts | `unknown` ⇒ **continue**. `length` ⇒ stop. Unrecognised Responses incomplete reason ⇒ `unknown` ⇒ continue. `pause_turn` ⇒ stop. |
| **crush / fantasy** | tool calls > 0 AND finish == `ToolCalls` AND no StopTurn result AND no StopWhen hit | everything else | `Unknown`/`Length`/`Error`/`ContentFilter` ⇒ stop, and tool calls are suppressed. `pause_turn` ⇒ stop. |
| **Codex** | any dispatched tool call, OR a tool error answered back to the model, OR `end_turn == false` (incl. `response.incomplete` + `interrupted`), OR pending input; stop hooks can also force a continue | no follow-up, and stop hooks allow it | `end_turn` absent ⇒ tool-call rule. `incomplete` + other reason ⇒ error/retry. `content_filter` ⇒ ContentFilter error. |
| **reliant (today)** | tool_calls OR feedback OR inbox OR aborted OR `stop_kind == truncated` | otherwise | n/a. `end_turn` and `phase` are not read. |

## "Said it would act, emitted no tool call" mitigations

- **Codex:** relies on the provider's `end_turn: false` signal to continue a tool-less response. It does not inspect message text. `phase: "commentary"` keeps the mailbox open but does not by itself loop. Configurable stop hooks (`run_turn_stop_hooks`) can veto a stop.
- **opencode:** no nudge. Its only implicit retry path is `unknown` ⇒ continue.
- **crush / fantasy:** none.

## Recommendation for reliant

Do these in the OpenAI Responses adapter:

1. Parse top-level `end_turn` from the `response.completed` payload. Treat `response.incomplete` with reason `interrupted` or `steered` as continue.
2. Parse `phase` on output message items, persist it, and resend it in history.
3. Normalize the result into a new loop signal, e.g. `stop_kind = "continue"` or a `provider_wants_followup` bool, and add it to the loop's continue condition.

Optionally, treat a final message with `phase == "commentary"` and no tool calls as continue, as a safety net. Codex does not do this, so cap it to avoid infinite loops.

Before implementing, verify against the raw payload of the failing turn (proxy capture or a log of `response.completed`) that it actually carried `end_turn:false` and/or `phase:"commentary"`.
