// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

func TestFinishReason(t *testing.T) {
	tests := []struct {
		reason string
		want   message.FinishReason
	}{
		{"STOP", message.FinishReasonEndTurn},
		{"MAX_TOKENS", message.FinishReasonMaxTokens},
		{"MALFORMED_FUNCTION_CALL", message.FinishReasonToolUseError},
		{"UNEXPECTED_TOOL_CALL", message.FinishReasonToolUseError},
		{"SAFETY", message.FinishReasonError},
		{"RECITATION", message.FinishReasonError},
		{"BLOCKLIST", message.FinishReasonError},
		{"PROHIBITED_CONTENT", message.FinishReasonError},
		{"SPII", message.FinishReasonError},
		{"LANGUAGE", message.FinishReasonError},
		{"MODEL_ARMOR", message.FinishReasonError},
		{"IMAGE_SAFETY", message.FinishReasonError},
		{"IMAGE_PROHIBITED_CONTENT", message.FinishReasonError},
		{"IMAGE_RECITATION", message.FinishReasonError},
		{"TOO_MANY_TOOL_CALLS", message.FinishReasonError},
		{"NO_IMAGE", message.FinishReasonError},
		{"MALFORMED_RESPONSE", message.FinishReasonError},
		{"MISSING_THOUGHT_SIGNATURE", message.FinishReasonError},
		{"something_unrecognized", message.FinishReasonUnknown},
		{"", message.FinishReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			for _, src := range []Source{SourceGemini, SourceVertexAI, SourceAntigravity} {
				if got := FinishReason(src, tt.reason); got != tt.want {
					t.Errorf("FinishReason(%s, %q) = %q, want %q", src, tt.reason, got, tt.want)
				}
			}
		})
	}
}

func TestIsErrorFinishReason(t *testing.T) {
	errorReasons := []string{
		"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL",
		"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"LANGUAGE", "MODEL_ARMOR", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION",
		"TOO_MANY_TOOL_CALLS", "NO_IMAGE", "MALFORMED_RESPONSE", "MISSING_THOUGHT_SIGNATURE",
	}
	for _, r := range errorReasons {
		if !IsErrorFinishReason(r) {
			t.Errorf("IsErrorFinishReason(%q) = false, want true", r)
		}
	}
	nonErrorReasons := []string{"STOP", "MAX_TOKENS", "", "UNKNOWN_THING"}
	for _, r := range nonErrorReasons {
		if IsErrorFinishReason(r) {
			t.Errorf("IsErrorFinishReason(%q) = true, want false", r)
		}
	}
}
