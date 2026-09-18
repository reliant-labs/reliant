// Copyright (c) 2025 Reliant Labs
//
// TestGeminiClient_FinishReason pins a real behavioral gap the geminiwire
// unification fixed: before it, this driver's own switch recognized only
// STOP and MAX_TOKENS, so a blocked or malformed response (SAFETY,
// MALFORMED_FUNCTION_CALL, ...) arrived as FinishReasonUnknown here even
// though the vertexai driver — reached through the identical
// genai.FinishReason enum — already mapped the same values to an error.
package gemini

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"google.golang.org/genai"
)

func TestGeminiClient_FinishReason(t *testing.T) {
	g := &GeminiClient{}
	tests := []struct {
		reason genai.FinishReason
		want   message.FinishReason
	}{
		{genai.FinishReasonStop, message.FinishReasonEndTurn},
		{genai.FinishReasonMaxTokens, message.FinishReasonMaxTokens},
		{genai.FinishReasonMalformedFunctionCall, message.FinishReasonToolUseError},
		{genai.FinishReasonUnexpectedToolCall, message.FinishReasonToolUseError},
		{genai.FinishReasonSafety, message.FinishReasonError},
		{genai.FinishReasonRecitation, message.FinishReasonError},
		{genai.FinishReasonBlocklist, message.FinishReasonError},
		{genai.FinishReasonProhibitedContent, message.FinishReasonError},
		{genai.FinishReasonSPII, message.FinishReasonError},
		{genai.FinishReasonOther, message.FinishReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			if got := g.finishReason(tt.reason); got != tt.want {
				t.Errorf("finishReason(%s) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}
