// Copyright (c) 2026 Reliant Labs
package telemetry

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// The closed vocabulary of AttrErrorType on the AI spans (workflow.node,
// invoke_agent, execute_tool, chat): what class of failure ended one. A failed
// span carries one of these as error.type and as its status description, and
// nothing else about the failure: no error text, no RecordError. A provider or
// a machine can put anything in an error's message (a prompt it echoes, a path,
// a token), and these spans are written to be exported to an org's own
// destination. The full error stays in the staff logs, beside the span by its
// trace and run ids.
//
// A class the vocabulary does not know is ErrorTypeError, never its text. Add a
// value beside the others; never rename one: dashboards group on them.
const (
	// ErrorTypeTimeout: a deadline ran out (context.DeadlineExceeded).
	ErrorTypeTimeout = "timeout"
	// ErrorTypeToolError: a tool ran and reported an error. The tool's
	// outcome, not a failure of the call: the span's status is left alone.
	ErrorTypeToolError = "tool_error"
	// ErrorTypeError: any other failure.
	ErrorTypeError = "error"

	// The classes of a failed provider call (internal/llm.ErrorType), also on
	// the agent turn that made it.

	// ErrorTypeContextOverflow: the request exceeded the model's context
	// window.
	ErrorTypeContextOverflow = "context_overflow"
	// ErrorTypeCreditExhausted: the provider's quota, credit or billing is
	// exhausted.
	ErrorTypeCreditExhausted = "credit_exhausted"
	// ErrorTypePromptRefused: the provider refused the prompt under its
	// usage policy.
	ErrorTypePromptRefused = "prompt_refused"
	// ErrorTypeStreamStalled: the stream sent only keepalives past its
	// content-stall deadline.
	ErrorTypeStreamStalled = "stream_stalled"
	// ErrorTypeStreamEndedEarly: the stream closed before its terminal event.
	ErrorTypeStreamEndedEarly = "stream_ended_early"
	// ErrorTypeMalformedOutput: the model's response could not be read.
	ErrorTypeMalformedOutput = "malformed_output"
	// ErrorTypeEmptyInput: the request had nothing to send.
	ErrorTypeEmptyInput = "empty_input"
	// ErrorTypeNoProvider: no provider could serve the model.
	ErrorTypeNoProvider = "no_provider"
)

// errorTypePrefixHTTP is the prefix of the one family of values formed from a
// number: "http_<status>", an HTTP status of 400-599 the provider answered
// with (ErrorTypeHTTP).
const errorTypePrefixHTTP = "http_"

var knownErrorTypes = map[string]bool{
	ErrorTypeCanceled:         true,
	ErrorTypeTimeout:          true,
	ErrorTypeToolError:        true,
	ErrorTypeError:            true,
	ErrorTypeContextOverflow:  true,
	ErrorTypeCreditExhausted:  true,
	ErrorTypePromptRefused:    true,
	ErrorTypeStreamStalled:    true,
	ErrorTypeStreamEndedEarly: true,
	ErrorTypeMalformedOutput:  true,
	ErrorTypeEmptyInput:       true,
	ErrorTypeNoProvider:       true,
}

// ErrorTypeHTTP is the class of a failure the provider answered with HTTP
// status: "http_<status>" for 400-599, ErrorTypeError for anything else.
func ErrorTypeHTTP(status int) string {
	if status < 400 || status > 599 {
		return ErrorTypeError
	}
	return errorTypePrefixHTTP + strconv.Itoa(status)
}

// IsErrorType reports whether t is a value of the vocabulary.
func IsErrorType(t string) bool {
	if knownErrorTypes[t] {
		return true
	}
	if n, ok := strings.CutPrefix(t, errorTypePrefixHTTP); ok {
		status, err := strconv.Atoi(n)
		return err == nil && ErrorTypeHTTP(status) == t
	}
	return false
}

// ErrorTypeOf is err's class as far as its type says without knowing who
// produced it: ErrorTypeCanceled, ErrorTypeTimeout, else ErrorTypeError; ""
// for nil. A caller that knows more (the provider call, internal/llm) names the
// class itself (EndAISpanAs).
func ErrorTypeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return ErrorTypeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorTypeTimeout
	}
	return ErrorTypeError
}

// SetAISpanError records that span ended in err, of class errType (a value of
// the vocabulary; anything else is ErrorTypeError, an empty one ErrorTypeOf).
// A cancellation is the caller hanging up (the user pressed stop, a newer turn
// superseded this one), not a failure: error.type=canceled and no status.
// Anything else is error.type=<class> and an error status described by the
// same class. The error's text is never recorded on the span. Nil err records
// nothing.
func SetAISpanError(span trace.Span, err error, errType string) {
	if err == nil {
		return
	}
	if errType == "" {
		errType = ErrorTypeOf(err)
	}
	if !IsErrorType(errType) {
		errType = ErrorTypeError
	}
	span.SetAttributes(attribute.String(AttrErrorType, errType))
	if errType == ErrorTypeCanceled {
		return
	}
	span.SetStatus(codes.Error, errType)
}
