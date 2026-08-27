// Copyright (c) 2025 Reliant Labs
//
// Package anthropicwire holds the parts of Anthropic's message API wire format
// that more than one driver has to understand.
//
// It exists because the same provider is reached by two code paths that cannot
// share a type: the direct Anthropic driver (internal/llm/drivers/anthropic,
// which also serves Claude Code OAuth and, by delegation, Bedrock) and
// Claude-on-Vertex (internal/llm/drivers/vertexai, which speaks the same API
// over raw HTTP). Both received the same stop_reason vocabulary and each kept
// its own copy of the mapping; the copies drifted — "stop_sequence" was mapped
// on one side and not the other, so an ordinary end of turn arrived as
// FinishReasonUnknown on Vertex only. One table, called from both, is the
// structural fix.
//
// The parent package internal/llm/drivers cannot hold this: it imports every
// driver package for registration side effects, so anything the drivers import
// from it would be an import cycle. A leaf package next to the drivers is the
// nearest home that both can reach.
package anthropicwire

import (
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Source names the driver that read the stop_reason, so an unrecognised value
// says which path saw it. That is the whole point of logging it: the drift
// this package exists to prevent showed up as one transport disagreeing with
// the other, and a log line that cannot tell them apart would not have caught
// it. A named type rather than a bare string so the two arguments cannot be
// swapped silently.
type Source string

const (
	// SourceAnthropic is the direct Anthropic driver. It covers API-key,
	// Claude Code OAuth and Bedrock traffic, all of which share one baseClient.
	SourceAnthropic Source = "anthropic"

	// SourceVertexAI is Claude served through Google Vertex AI.
	SourceVertexAI Source = "vertexai"
)

// FinishReason maps an Anthropic message API stop_reason to the internal
// finish reason.
//
// The vocabulary is closed and small — anthropic-sdk-go types StopReason as
// exactly "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn"
// and "refusal" — so every value the provider can send has a case here, and
// anything else is a provider change we have not caught up with yet.
//
// An unrecognised value is reported rather than silently absorbed. Unknown is
// the reason the runtime treats as "nothing to say about this turn", so a
// mapping gap does not fail anything loudly; it just degrades the turn. The
// warning is the only thing standing between a new stop_reason and a user
// staring at an unexplained empty response.
func FinishReason(src Source, stopReason string) message.FinishReason {
	switch stopReason {
	case "end_turn":
		return message.FinishReasonEndTurn

	case "max_tokens":
		return message.FinishReasonMaxTokens

	case "tool_use":
		return message.FinishReasonToolUse

	case "stop_sequence":
		// A custom stop sequence fired. The turn is complete and the model
		// chose to end it, which is what EndTurn means here — the sequence
		// itself is reported separately on the response and nothing downstream
		// distinguishes the two.
		return message.FinishReasonEndTurn

	case "pause_turn":
		// The model paused mid-turn and expects to be handed the same
		// conversation back to carry on. NOT an end of turn: mapping it to
		// EndTurn would claim the model finished when it did not.
		return message.FinishReasonPauseTurn

	case "refusal":
		// The provider stopped generation itself. The turn arrives with zero
		// content blocks, so without a reason of its own it would reach the
		// runtime indistinguishable from a healthy empty turn and silently end
		// the chat. call_llm turns this into a visible message.
		return message.FinishReasonRefusal

	default:
		logging.Warn("Unrecognized Anthropic stop_reason; the turn will be treated as an unknown finish",
			"source", string(src), "stop_reason", stopReason)
		return message.FinishReasonUnknown
	}
}
