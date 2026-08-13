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
			name:       "local gcloud reauthentication required is terminal",
			err:        errors.New("litellm.APIConnectionError: Reauthentication is needed. Please run gcloud auth application-default login to reauthenticate."),
			isTerminal: true,
			category:   "auth",
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

// One failing activity must produce ONE error row in the transcript, not one
// per attempt. chat_updates dedup per entity_id and the frontend's error log
// dedups by id, so the retry series has to share a single id for the badge to
// advance in place ("Retrying (Attempt 1/3)" → "Attempt 3/3") instead of
// stacking three rows for one failure.
func TestActivityErrorEventID(t *testing.T) {
	const workflowID = "wf-1"

	t.Run("retries of one activity share an id", func(t *testing.T) {
		// Temporal reuses activity_id across attempts; attempt_number varies.
		first := activityErrorEventID(workflowID, "42")
		second := activityErrorEventID(workflowID, "42")
		third := activityErrorEventID(workflowID, "42")

		if first != second || second != third {
			t.Fatalf("all attempts of one activity must share an id, got %q, %q, %q",
				first, second, third)
		}
	})

	t.Run("different activities get different ids", func(t *testing.T) {
		if a, b := activityErrorEventID(workflowID, "42"), activityErrorEventID(workflowID, "43"); a == b {
			t.Fatalf("distinct activities must not collapse into one row, both got %q", a)
		}
	})

	// Activity ids restart at 1 for every run, including a continue-as-new
	// successor. Without the workflow id in the key, a new run's first failure
	// would overwrite the previous run's recorded error.
	t.Run("same activity id in different runs stays distinct", func(t *testing.T) {
		if a, b := activityErrorEventID("wf-1", "1"), activityErrorEventID("wf-2", "1"); a == b {
			t.Fatalf("activity id 1 of two different runs must not collide, both got %q", a)
		}
	})

	// Defensive: with nothing stable to key on, fall back to a unique id so
	// unrelated failures are never merged into a single row.
	t.Run("missing activity id falls back to unique", func(t *testing.T) {
		if a, b := activityErrorEventID(workflowID, ""), activityErrorEventID(workflowID, ""); a == b {
			t.Fatalf("blank activity ids must not merge unrelated errors, both got %q", a)
		}
	})
}
