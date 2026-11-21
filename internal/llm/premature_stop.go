// Copyright (c) 2025 Reliant Labs
package llm

// PrematureStop names the way a completed turn looks like the model stopped
// early (DriverResponse.PrematureStop). The empty value means the turn looks
// like an ordinary end.
//
// Only the OpenAI Responses drivers report one (see
// responseswire.PrematureStop). Codex models sometimes end a turn on a
// commentary-phase preamble ("I'll inspect the auth setup next.") or on
// reasoning alone, without the tool call they were about to make and without
// the end_turn:false that would have told us to continue. The provider calls
// both "completed", so the finish reason cannot carry the difference.
type PrematureStop string

const (
	// PrematureStopCommentary: the last assistant message is phase
	// "commentary" and no "final_answer" message exists.
	PrematureStopCommentary PrematureStop = "commentary"
	// PrematureStopReasoningOnly: the turn produced no visible text (reasoning
	// only, or nothing at all).
	PrematureStopReasoningOnly PrematureStop = "reasoning_only"
)
