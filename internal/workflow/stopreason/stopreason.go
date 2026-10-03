// Copyright (c) 2025 Reliant Labs

// Package stopreason derives call_llm's stop_reason: the single, closed-vocabulary
// answer to "why did this turn end" that a loop's while-condition reads.
//
// It replaces three overlapping signals — tool-call presence, the `aborted`
// bit, and the four-value `stop_kind` — which every agent loop had to OR
// together, with match polarities that differed per loop because each signal
// had its own absent-value hazard. One field, derived in one place, means a
// loop states the outcomes that earn another turn and nothing else.
//
// See specs/stop-reason-normalization.md.
package stopreason

import "github.com/reliant-labs/reliant/internal/models/message"

// The vocabulary. Closed by construction: Derive maps every provider finish
// reason — including ones that do not exist yet — into one of these, so a
// workflow condition written against them cannot silently miss a new case.
const (
	// Interrupted: our stream was cut short mid-flight. The turn's text and
	// tool calls are whatever arrived before the cut, not what the model
	// intended, so the work is unfinished.
	Interrupted = "interrupted"
	// ToolUse: the model requested at least one tool. Running them produces
	// new input, so another turn is never a repeat of this one.
	ToolUse = "tool_use"
	// Refused: the safety system declined. The request has to change before
	// the answer will.
	Refused = "refused"
	// Truncated: the model ran out of output room mid-turn. The work is a
	// fragment, and re-sending the SAME request truncates the same way.
	Truncated = "truncated"
	// Incomplete: the provider paused the turn and expects the conversation
	// handed back so the model can carry on — Anthropic pause_turn, OpenAI
	// Responses end_turn:false or incomplete:interrupted — and the turn
	// produced text, so continuing sends the model something new: its own
	// progress.
	Incomplete = "incomplete"
	// Error: a provider or transport failure, a pause that produced nothing,
	// or a finish reason this vocabulary does not recognize.
	Error = "error"
	// Done: the model finished on its own terms.
	Done = "done"
)

// Turn is what Derive needs to know about the turn that just ran.
type Turn struct {
	// FinishReason is the provider's raw stop reason, as normalized by the
	// driver.
	FinishReason message.FinishReason
	// ToolCalls is the number of finished tool calls the turn produced.
	ToolCalls int
	// Interrupted reports that our stream was cut short before the provider
	// finished it.
	Interrupted bool
	// ProducedText reports that the turn produced non-empty response text.
	ProducedText bool
}

// Derive collapses a turn into its stop_reason. First match wins, and the
// order is the policy:
//
//   - Interrupted first: a cut stream's tool calls and finish reason are
//     whatever had arrived, so neither can be trusted to describe the turn.
//   - ToolUse before the failure kinds: when the model requested tools, the
//     tools run (the loop body routes on tool_calls, not on this), and their
//     results make the next request different from this one whatever the
//     finish reason said. A loop that continues on tool_use therefore always
//     makes progress.
//   - A pause becomes Incomplete only when it produced text. A pause that
//     produced nothing adds nothing to history, so re-calling would send a
//     byte-identical request and most likely get the identical answer — that
//     is the one way a "keep going" signal could spin, and mapping it to Error
//     closes it without a turn counter.
//   - Anything unrecognized is Error, never Done. Treating an unknown stop as
//     a clean finish is what makes a new provider behavior look like success.
func Derive(t Turn) string {
	if t.Interrupted {
		return Interrupted
	}
	if t.ToolCalls > 0 {
		return ToolUse
	}
	switch t.FinishReason {
	case message.FinishReasonEndTurn, message.FinishReasonToolUse:
		// ToolUse with zero surviving calls: the provider said it stopped to
		// call a tool, but no call was usable. There is nothing to run, so
		// the model has nothing pending — the turn is over.
		return Done
	case message.FinishReasonRefusal:
		return Refused
	case message.FinishReasonMaxTokens:
		return Truncated
	case message.FinishReasonPauseTurn:
		if t.ProducedText {
			return Incomplete
		}
		return Error
	default:
		// Cancelled, Error, ToolUseError, PermissionDenied, Unknown, "", and
		// anything a provider adds later.
		return Error
	}
}

// FillDefault gives an activity output map a stop_reason when it has none, in
// place. A set value is left alone.
//
// Every call_llm output a workflow sees passes through this — the runtime's
// output normalizer and the scenario runner's — so no while-condition ever
// reads an empty stop_reason. The fallback is derived from the one signal
// every shape carries: tool calls mean tool_use, otherwise done. That is
// exactly how agent loops behaved before stop_reason existed, so a replayed
// iteration or a scenario fixture that predates the field keeps its meaning.
func FillDefault(output map[string]interface{}) {
	if output == nil {
		return
	}
	if reason, _ := output["stop_reason"].(string); reason != "" {
		return
	}
	if calls, _ := output["tool_calls"].([]interface{}); len(calls) > 0 {
		output["stop_reason"] = ToolUse
		return
	}
	output["stop_reason"] = Done
}
