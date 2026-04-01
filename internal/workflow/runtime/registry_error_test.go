// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"strings"
	"testing"

	"go.temporal.io/sdk/temporal"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		isTerminal bool
		category   string
	}{
		{
			name:       "not found error is terminal",
			err:        errors.New("message not found"),
			isTerminal: true,
			category:   "not_found",
		},
		{
			name:       "invalid input is terminal",
			err:        errors.New("invalid input: field cannot be empty"),
			isTerminal: true,
			category:   "validation",
		},
		{
			name:       "network timeout is transient",
			err:        errors.New("connection timeout"),
			isTerminal: false,
			category:   "network",
		},
		{
			name:       "rate limit is transient",
			err:        errors.New("rate limit exceeded"),
			isTerminal: false,
			category:   "rate_limit",
		},
		{
			name:       "context deadline exceeded is transient",
			err:        errors.New("context deadline exceeded"),
			isTerminal: false,
			category:   "network",
		},
		{
			name:       "unknown model is terminal",
			err:        errors.New("unknown model: gpt-5"),
			isTerminal: true,
			category:   "unknown",
		},
		{
			name:       "service unavailable is transient",
			err:        errors.New("service unavailable"),
			isTerminal: false,
			category:   "unknown",
		},
		{
			name:       "prompt is too long is terminal",
			err:        errors.New("prompt is too long: 202216 tokens > 200000 maximum"),
			isTerminal: true,
			category:   "unknown",
		},
		{
			name:       "too many tokens is terminal",
			err:        errors.New("too many tokens in request"),
			isTerminal: true,
			category:   "unknown",
		},
		{
			name:       "context length exceeded is terminal",
			err:        errors.New("maximum context length exceeded"),
			isTerminal: true,
			category:   "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Classify the error
			classified := classifyError(tt.err)

			// Check if it's terminal
			terminal := isTerminal(classified)
			if terminal != tt.isTerminal {
				t.Errorf("expected isTerminal=%v, got %v", tt.isTerminal, terminal)
			}

			// Check category
			category := categorizeError(tt.err)
			if category != tt.category {
				t.Errorf("expected category=%s, got %s", tt.category, category)
			}

			// Verify Temporal error type
			if tt.isTerminal {
				var appErr *temporal.ApplicationError
				if !errors.As(classified, &appErr) {
					t.Errorf("terminal error should be ApplicationError, got %T", classified)
				} else if appErr.Type() != "TerminalError" {
					t.Errorf("expected ApplicationError type=TerminalError, got %s", appErr.Type())
				}
			} else {
				// Transient errors should NOT be ApplicationError
				var appErr *temporal.ApplicationError
				if errors.As(classified, &appErr) && appErr.Type() == "TerminalError" {
					t.Errorf("transient error should not be TerminalError ApplicationError")
				}
			}

			// Verify error message is preserved
			if !strings.Contains(classified.Error(), tt.err.Error()) {
				t.Errorf("classified error should contain original error message")
			}
		})
	}
}

func TestTerminalError(t *testing.T) {
	cause := errors.New("original error")
	termErr := &TerminalError{
		Message: "validation failed",
		Cause:   cause,
	}

	// Test Error() method
	errMsg := termErr.Error()
	if !strings.Contains(errMsg, "validation failed") {
		t.Errorf("error message should contain 'validation failed'")
	}
	if !strings.Contains(errMsg, "original error") {
		t.Errorf("error message should contain cause")
	}

	// Test Unwrap()
	if !errors.Is(termErr, cause) {
		t.Errorf("TerminalError should unwrap to cause")
	}

	// Test classification
	classified := classifyError(termErr)
	if !isTerminal(classified) {
		t.Errorf("TerminalError should be classified as terminal")
	}
}

func TestDefaultTransient(t *testing.T) {
	// Unknown errors should default to transient (retryable)
	unknownErr := errors.New("some random error")
	classified := classifyError(unknownErr)

	if isTerminal(classified) {
		t.Errorf("unknown error should default to transient, not terminal")
	}

	// Should not be wrapped as ApplicationError
	var appErr *temporal.ApplicationError
	if errors.As(classified, &appErr) && appErr.Type() == "TerminalError" {
		t.Errorf("unknown error should not be terminal ApplicationError")
	}
}

func TestMalformedJSONErrorsAreTransient(t *testing.T) {
	// These tests verify that malformed JSON errors from streaming are treated as transient
	// They should NOT be terminal because they typically succeed on retry

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "MalformedJSONError from our code",
			err:  errors.New("malformed JSON in tool input (transient): tool=test_tool, id=call_123, input=\"{...\"}"),
		},
		{
			name: "SDK RawMessage error - unexpected end",
			err:  errors.New("json: error calling MarshalJSON for type json.RawMessage: unexpected end of JSON input"),
		},
		{
			name: "SDK RawMessage error - invalid character",
			err:  errors.New("json: error calling MarshalJSON for type json.RawMessage: invalid character 'x' looking for beginning of value"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := classifyError(tt.err)

			// Should NOT be terminal
			if isTerminal(classified) {
				t.Errorf("malformed JSON error should be transient (retryable), not terminal")
			}

			// Should not be ApplicationError with TerminalError type
			var appErr *temporal.ApplicationError
			if errors.As(classified, &appErr) && appErr.Type() == "TerminalError" {
				t.Errorf("malformed JSON error should not be TerminalError ApplicationError")
			}
		})
	}
}

func TestClassifyError_DNS(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		isTerminal bool
		category   string
	}{
		{
			name:       "DNS no such host is transient",
			err:        errors.New(`dial tcp: lookup api.anthropic.com: no such host`),
			isTerminal: false,
			category:   "network",
		},
		{
			name:       "dial tcp connection refused is transient",
			err:        errors.New(`dial tcp 1.2.3.4:443: connect: connection refused`),
			isTerminal: false,
			category:   "network",
		},
		{
			name:       "full DNS error chain is transient",
			err:        errors.New(`failed to stream LLM response: LLM streaming error: Post "https://api.anthropic.com/v1/messages?beta=true": dial tcp: lookup api.anthropic.com: no such host`),
			isTerminal: false,
			category:   "network",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := classifyError(tt.err)

			terminal := isTerminal(classified)
			if terminal != tt.isTerminal {
				t.Errorf("expected isTerminal=%v, got %v", tt.isTerminal, terminal)
			}

			category := categorizeError(tt.err)
			if category != tt.category {
				t.Errorf("expected category=%s, got %s", tt.category, category)
			}

			// Transient errors should NOT be terminal ApplicationError
			var appErr *temporal.ApplicationError
			if errors.As(classified, &appErr) && appErr.Type() == "TerminalError" {
				t.Errorf("DNS error should not be TerminalError ApplicationError")
			}
		})
	}
}

func TestRegularMalformedErrorsAreStillTerminal(t *testing.T) {
	// Verify that regular "malformed" errors (not JSON streaming) are still terminal
	// For example, validation errors

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "malformed request",
			err:  errors.New("malformed request body"),
		},
		{
			name: "invalid input",
			err:  errors.New("invalid input parameter"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := classifyError(tt.err)

			// Should be terminal (non-retryable)
			if !isTerminal(classified) {
				t.Errorf("regular malformed/invalid errors should be terminal")
			}
		})
	}
}
