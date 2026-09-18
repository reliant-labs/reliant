// Copyright (c) 2025 Reliant Labs
//
// Package geminiwire holds the parts of Google's Gemini wire format that more
// than one driver has to understand: internal/llm/drivers/gemini and
// internal/llm/drivers/vertexai both reach the genai SDK's
// GenerateContentResponse, and internal/llm/drivers/antigravity reaches the
// same enum as a bare string through its own hand-rolled envelope. All three
// see one closed vocabulary of finish reasons, and each kept its own copy of
// the mapping. The copies drifted: gemini's copy mapped only STOP and
// MAX_TOKENS, leaving SAFETY, RECITATION, BLOCKLIST, PROHIBITED_CONTENT, SPII
// and the malformed/unexpected-tool-call reasons to fall through to Unknown,
// while vertexai — reached through the IDENTICAL genai.FinishReason enum —
// already mapped those to an error. A blocked or malformed response on the
// direct Gemini driver looked like an ordinary empty turn instead of the
// error it was. One table, called from all three, is the structural fix; see
// anthropicwire.FinishReason for the Claude-side precedent this follows.
//
// This is a leaf package: internal/llm/drivers imports every driver package
// for registration side effects, so anything a driver imports from it would
// be an import cycle. A leaf next to the drivers is the nearest home all
// three can reach.
package geminiwire

import (
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Source names the driver that read the finish reason, so an unrecognised
// value says which transport saw it — the same discipline as
// anthropicwire.Source, for the same reason: the drift this package exists to
// prevent showed up as one transport disagreeing with another.
type Source string

const (
	// SourceGemini is the direct Gemini API driver (genai SDK, API key auth).
	SourceGemini Source = "gemini"

	// SourceVertexAI is Gemini served through Google Vertex AI (genai SDK
	// over Vertex credentials).
	SourceVertexAI Source = "vertexai"

	// SourceAntigravity is the Antigravity/cloudcode endpoint, reached over a
	// hand-rolled double-enveloped HTTP+SSE transport rather than the SDK.
	SourceAntigravity Source = "antigravity"
)

// FinishReason maps a Gemini finishReason wire value to the internal finish
// reason.
//
// Callers pass the raw string: genai.FinishReason is itself a string type, so
// string(reason) costs nothing, and antigravity's envelope already carries a
// bare string. A shared string-keyed function is what lets three transports
// with two different Go types for "the same enum" call one table instead of
// each converting to their own.
//
// The vocabulary is the closed set google.golang.org/genai's FinishReason
// declares. STOP and MAX_TOKENS are unambiguous ends of turn.
// MALFORMED_FUNCTION_CALL and UNEXPECTED_TOOL_CALL mean the model tried to
// call a tool and failed — retryable as a tool error, not a clean end of
// turn. SAFETY, RECITATION, BLOCKLIST, PROHIBITED_CONTENT and SPII are the
// provider refusing to continue, reported as an error so a blocked response
// is not read as an ordinary empty turn.
//
// An unrecognised value (including the empty string a provider sends when it
// has not actually finished, or omits the field) is reported and treated as
// Unknown rather than guessed at. A provider genuinely mid-turn is not
// "ended", so nothing here defaults it to EndTurn.
func FinishReason(src Source, reason string) message.FinishReason {
	switch reason {
	case "STOP":
		return message.FinishReasonEndTurn

	case "MAX_TOKENS":
		return message.FinishReasonMaxTokens

	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return message.FinishReasonToolUseError

	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return message.FinishReasonError

	default:
		logging.Warn("Unrecognized Gemini finishReason; the turn will be treated as an unknown finish",
			"source", string(src), "finish_reason", reason)
		return message.FinishReasonUnknown
	}
}

// IsErrorFinishReason reports whether reason represents the provider
// refusing or failing to produce a candidate, as opposed to a normal end of
// generation (STOP, MAX_TOKENS) or a not-yet-finished stream chunk.
//
// This is the same closed set FinishReason maps to
// message.FinishReasonToolUseError / message.FinishReasonError; kept as its
// own predicate because callers that need to abort the request entirely
// (rather than just label the finish reason) want a bool, not a
// message.FinishReason to compare against two constants.
func IsErrorFinishReason(reason string) bool {
	switch reason {
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL",
		"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return true
	default:
		return false
	}
}
