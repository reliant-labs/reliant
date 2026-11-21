// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// StopError is a Gemini candidate that finished with a reason that is a
// failure, not an end of turn. Its text is opencode's ("Gemini stopped with
// X"), with the server's finishMessage when it sent one: for
// MALFORMED_FUNCTION_CALL that message is the call the model tried to make,
// which is the only clue to why it failed.
//
// What the turn does with it is decided by identity, not text:
//
//   - MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL and MALFORMED_RESPONSE
//     match drivererrors.ErrMalformedModelOutput, which CallLLM retries once.
//   - MISSING_THOUGHT_SIGNATURE matches ErrThoughtSignatureRejected, which the
//     drivers' signature recovery retries once with stand-in signatures.
//   - Every other error reason (content filters, TOO_MANY_TOOL_CALLS,
//     NO_IMAGE) matches neither and ends the turn.
type StopError struct {
	Source Source
	// Reason is the raw finishReason, e.g. "MALFORMED_FUNCTION_CALL".
	Reason string
	// Message is the candidate's finishMessage, or "".
	Message string
}

func (e *StopError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Gemini stopped with %s: %s", e.Reason, e.Message)
	}
	return fmt.Sprintf("Gemini stopped with %s", e.Reason)
}

// Is reports the retry class of the stop; see StopError.
func (e *StopError) Is(target error) bool {
	switch target {
	case drivererrors.ErrMalformedModelOutput:
		return isMalformedOutputStop(e.Reason)
	case ErrThoughtSignatureRejected:
		return e.Reason == "MISSING_THOUGHT_SIGNATURE"
	}
	return false
}

// isMalformedOutputStop is the set opencode classifies as a retryable
// incomplete stream (gemini.ts: MALFORMED_RESPONSE, MALFORMED_FUNCTION_CALL,
// UNEXPECTED_TOOL_CALL). Each says the model's output could not be parsed or
// was not allowed, not that the request was wrong, so a fresh sample is the
// remedy.
func isMalformedOutputStop(reason string) bool {
	switch reason {
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MALFORMED_RESPONSE":
		return true
	}
	return false
}

// StopFailure returns the error a driver must end its response with instead
// of completing it, or nil when reason does not call for one.
//
// Only the reasons something can be done about are errors here: the
// malformed-output stops (retried once by CallLLM) and
// MISSING_THOUGHT_SIGNATURE (retried once with stand-in signatures). The
// other error reasons keep completing with message.FinishReasonError, which
// ends the turn as it always has; a driver that already turns every error
// reason into an error (vertexai) can use &StopError{} for those directly.
//
// A driver checks this only when the candidate carried no tool calls: calls
// that did parse are worth running, and the model's next turn can redo the
// one that did not.
func StopFailure(src Source, reason, finishMessage string) error {
	if isMalformedOutputStop(reason) || reason == "MISSING_THOUGHT_SIGNATURE" {
		return &StopError{Source: src, Reason: reason, Message: finishMessage}
	}
	return nil
}
