// Copyright (c) 2025 Reliant Labs
//
// Package responseswire holds the parts of OpenAI's Responses API wire format
// that more than one driver has to understand.
//
// It exists for the same reason as anthropicwire: one provider API is reached
// by two driver packages that cannot share a type. internal/llm/drivers/openai
// speaks the Responses API against api.openai.com, and
// internal/llm/drivers/codex speaks it against the ChatGPT Codex backend with
// codex-tui's headers and envelope. Both decode the same `responses.Response`
// and both have to answer the same question — why did this turn end? — and each
// had its own answer. The codex copy mapped every `incomplete` status to
// MaxTokens regardless of reason; the openai copy did not look at status at all
// and reported EndTurn for a failed response. One table, called from both, is
// the structural fix.
//
// The parent package internal/llm/drivers cannot hold this: it imports every
// driver package for registration side effects, so anything a driver imported
// from it would be an import cycle. A leaf package next to the drivers is the
// nearest home both can reach.
package responseswire

import (
	"encoding/json"

	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Incomplete-status reasons. The SDK types IncompleteDetails.Reason as a bare
// string documented as "max_output_tokens", "max_messages" or "content_filter".
// Codex CLI additionally knows "interrupted", which the SDK does not list — see
// EndTurn for why we trust Codex's vocabulary over the published one here.
const (
	ReasonMaxOutputTokens = "max_output_tokens"
	ReasonContentFilter   = "content_filter"
	ReasonInterrupted     = "interrupted"
)

// endTurnField is the undocumented top-level boolean the Responses API puts on
// the response object carried by `response.completed`. It is absent from the
// openai-go types, so it is only reachable through JSON.ExtraFields.
const endTurnField = "end_turn"

// FinishReason maps a terminal Responses API response to the internal finish
// reason.
//
// The interesting case is a `completed` response with `end_turn: false`. That
// is the provider saying "I am not done, hand the conversation back" even
// though it requested no tools, and it is the signal Codex CLI acts on:
// codex-rs reads `end_turn` off the `response.completed` payload with
// `#[serde(default)] end_turn: Option<bool>` and sets `needs_follow_up = true`
// on `Some(false)` (core/src/session/turn.rs:3009), which continues its agent
// loop with zero tool calls. Reporting that turn as EndTurn — which is what
// both drivers did before this package — tells our runtime the model finished
// when it explicitly said it had not, and is the defect behind the chat that
// stopped after "…I'll inspect the local dev auth setup before attempting the
// workflow again." PauseTurn is the existing internal reason for "the provider
// paused and expects the conversation back", so it is the honest mapping.
//
// `incomplete` + `interrupted` is the same request, arriving differently:
// codex-rs normalizes that status/reason pair to `end_turn: Some(false)`
// (codex-api/src/sse/responses.rs:418), so it maps to PauseTurn too.
//
// Two documented reasons map to Unknown ON PURPOSE, not by omission:
//
//   - "max_messages" is a limit on the CONVERSATION, not on this turn's
//     output. Mapping it to MaxTokens would make it stop_reason "truncated",
//     which an agent loop continues on — but the next request carries the
//     same history plus more, so it hits the same limit and the loop spins.
//     Unknown surfaces as stop_reason "error": the run stops and says why.
//   - "steered" is only produced by a WebSocket `response.steer` event, after
//     which the SERVER creates the successor response itself. We speak HTTP
//     SSE and never send response.steer, so it is unreachable here; if it ever
//     appears, re-issuing the request ourselves would race the server's own
//     successor, so stopping is the safe reading.
//
// Any other `incomplete` reason we do not recognise becomes Unknown rather than
// MaxTokens. The previous codex mapping assumed every incomplete response was
// a token-budget truncation, which turned a content filter and an interruption
// into a claim about token limits that the runtime then reported to the user.
// Unknown is honest about not knowing, and the caller logs the raw reason.
//
// Tool calls override the status ONLY when it is `completed`. An incomplete,
// failed or cancelled response can carry half-streamed function calls whose
// arguments were cut off mid-JSON; promoting those to ToolUse would claim a
// complete tool request and send malformed input to a tool. That precedence
// matches what both drivers already did.
//
// A nil response means the stream ended without a terminal event at all. We
// keep the drivers' existing fallback — EndTurn, or ToolUse if calls were
// streamed — rather than inventing an error for it, because that path is
// reached by healthy streams against mocks and replays.
func FinishReason(resp *responses.Response, toolCallCount int) message.FinishReason {
	if resp == nil {
		if toolCallCount > 0 {
			return message.FinishReasonToolUse
		}
		return message.FinishReasonEndTurn
	}

	switch resp.Status {
	case responses.ResponseStatusFailed:
		return message.FinishReasonError

	case responses.ResponseStatusCancelled:
		return message.FinishReasonCancelled

	case responses.ResponseStatusIncomplete:
		switch resp.IncompleteDetails.Reason {
		case ReasonMaxOutputTokens:
			return message.FinishReasonMaxTokens
		case ReasonContentFilter:
			return message.FinishReasonRefusal
		case ReasonInterrupted:
			return message.FinishReasonPauseTurn
		default:
			return message.FinishReasonUnknown
		}
	}

	// Completed — and anything non-terminal that somehow reached us, which can
	// only be a stream that ended early and is best described as a plain end.
	if toolCallCount > 0 {
		return message.FinishReasonToolUse
	}
	if value, present := EndTurn(resp); present && !value {
		return message.FinishReasonPauseTurn
	}
	return message.FinishReasonEndTurn
}

// EndTurn reports the undocumented top-level `end_turn` boolean, and whether it
// was present at all.
//
// Presence is a distinct answer from false. Codex CLI models it as
// `Option<bool>` and acts only on `Some(false)`: absent means a legacy model
// that never sends the field, and falls back to "tool calls ⇒ continue"
// (specs/research-agent-loop-stop-semantics.md §3). Collapsing absent into
// false would make every pre-gpt-5.3 turn look like a continuation request and
// loop forever.
//
// The field is not in openai-go's types — `rg end_turn` finds nothing in the
// OpenAI SDKs — so it is read from JSON.ExtraFields, which holds whatever the
// decoder could not place. A field that is present but not a JSON boolean is
// reported absent rather than coerced: a provider that changes the type is
// telling us something we do not understand yet, and guessing would silently
// invent a loop decision.
//
// Do NOT gate this on respjson.Field.Valid(). For an UNTYPED extra field there
// is no struct member to decode into, so apijson's decoder leaves the metadata
// at status `invalid` and Valid() returns false for every extra field the
// response carries, including ones that decoded perfectly well. Valid() is
// meaningful for declared fields only. Raw() is the usable signal here: it is
// "" when the key was omitted and the literal "null" when it was null, which
// are exactly the two absences we want, and json.Unmarshal rejects the rest.
func EndTurn(resp *responses.Response) (value bool, present bool) {
	if resp == nil {
		return false, false
	}
	field, ok := resp.JSON.ExtraFields[endTurnField]
	if !ok {
		return false, false
	}
	raw := field.Raw()
	if raw == "" || raw == "null" {
		return false, false
	}
	var decoded bool
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return false, false
	}
	return decoded, true
}

// AssistantPhase reports the `phase` of the last assistant message in the
// response output, or "" when there is none.
//
// Phase is "commentary" or "final_answer" and labels interim narration versus
// the turn's terminal answer. It is for LOGGING only here. Codex CLI does not
// use phase to decide whether to loop — that decision is `end_turn` plus tool
// calls — but it correlates with it, so a served line that carries both is what
// lets us check the inference that commentary turns also arrive with
// `end_turn: false` against real traffic rather than against a guess.
//
// The last message wins because a turn may narrate before answering, and it is
// the final emission that characterises how the turn ended.
func AssistantPhase(resp *responses.Response) string {
	if resp == nil {
		return ""
	}
	phase := ""
	for _, out := range resp.Output {
		if msg := out.AsMessage(); msg.Type == "message" {
			phase = string(msg.Phase)
		}
	}
	return phase
}
