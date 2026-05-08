package runtime

import (
	"testing"
)

func TestExtractLLMErrorSummary(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		expected string
	}{
		{
			name:     "anthropic overloaded JSON",
			errMsg:   `activity error (type: CallLLM, scheduledEventID: 104, startedEventID: 105, identity: 53948@host): failed to stream LLM response: LLM streaming error: received error while streaming: {"type":"error","error":{"details":null,"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CZ9Dhrfa8CWNE5TY9tjnt"}`,
			expected: "The AI provider is currently overloaded (Overloaded)",
		},
		{
			name:     "anthropic internal server error JSON",
			errMsg:   `activity error (type: CallLLM, scheduledEventID: 104, startedEventID: 105, identity: 53948@host): failed to stream LLM response: LLM streaming error: received error while streaming: {"type":"error","error":{"details":null,"type":"api_error","message":"Internal server error"},"request_id":"req_011CZ9E42A8KFvma9wHbnCSF"}`,
			expected: "AI provider internal server error (Internal server error)",
		},
		{
			name:     "anthropic rate limit JSON",
			errMsg:   `failed to stream LLM response: LLM streaming error: received error while streaming: {"type":"error","error":{"details":null,"type":"rate_limit_error","message":"Rate limited"},"request_id":"req_abc"}`,
			expected: "Rate limited by the AI provider (Rate limited)",
		},
		{
			name:     "claude oauth session expired string",
			errMsg:   `claude token refresh failed: Claude session expired: please reconnect Claude`,
			expected: "Claude session expired. Please reconnect Claude",
		},
		{
			name:     "claude oauth unauthorized anthropic request",
			errMsg:   `activity error (type: CallLLM, scheduledEventID: 35, startedEventID: 36, identity: 82721@MacBook-Pro-5.local@): failed to stream LLM response: LLM streaming error: POST "https://api.anthropic.com/v1/messages": 401 Unauthorized (Request-ID: req_011CZNNCYXCXRnaXLCCfLmMW) {"type":"error","error":{"type":"authentication_error","message":"Invalid authentication credentials"},"request_id":"req_011CZNNCYXCXRnaXLCCfLmMW"}`,
			expected: "Claude session expired. Please reconnect Claude",
		},
		{
			name:     "codex authentication required",
			errMsg:   `codex authentication required: connect Codex from Settings`,
			expected: "Codex session expired. Please reconnect Codex",
		},
		{
			name:     "codex session expired string",
			errMsg:   `codex session expired: please reconnect Codex`,
			expected: "Codex session expired. Please reconnect Codex",
		},
		{
			name:     "unknown API error type in JSON",
			errMsg:   `LLM streaming error: received error while streaming: {"type":"error","error":{"details":null,"type":"new_error_type","message":"Something new"},"request_id":"req_xyz"}`,
			expected: "AI provider error: Something new",
		},
		{
			name:     "local gcloud reauthentication guidance",
			errMsg:   "failed to stream LLM response: LLM streaming error: litellm.APIConnectionError: Reauthentication is needed. Please run gcloud auth application-default login to reauthenticate.",
			expected: "Local Google Cloud auth expired. Run `gcloud auth application-default login` and retry",
		},
		{
			name:     "pattern match: timeout without JSON",
			errMsg:   "context deadline exceeded: timeout waiting for LLM response",
			expected: "Request to the AI provider timed out",
		},
		{
			name:     "pattern match: rate limit without JSON",
			errMsg:   "rate limit exceeded for model claude-opus-4-20250514",
			expected: "Rate limited by the AI provider",
		},
		{
			name:     "pattern match: connection refused",
			errMsg:   "dial tcp: connection refused",
			expected: "Could not connect to the AI provider",
		},
		{
			name:     "no match - generic error",
			errMsg:   "some completely unrecognized error",
			expected: "",
		},
		{
			name:     "empty string",
			errMsg:   "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractLLMErrorSummary(tt.errMsg)
			if result != tt.expected {
				t.Errorf("extractLLMErrorSummary() = %q, want %q", result, tt.expected)
			}
		})
	}
}
