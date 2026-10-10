// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestClaudeCodeMaxTokens_MatchesCaptures pins the subscription request's
// max_tokens to what claude-cli itself sends, even though the catalog declares
// each model's published maximum (128,000 for every 1M Claude model). The
// catalog value is what the resolver hands every driver; the Claude Code driver
// must not forward it verbatim, or a catalog correction silently changes the
// fingerprint of every subscription request.
//
// Captured values: the "max_tokens" field of the request bodies in .dev/claude/
// (gitignored, so restated here).
func TestClaudeCodeMaxTokens_MatchesCaptures(t *testing.T) {
	reg := models.MustGetRegistry()
	catalogMax := map[string]int64{}
	for _, def := range reg.ListModelsByProvider("anthropic") {
		for _, p := range def.Providers {
			if p.Driver == "anthropic" {
				catalogMax[p.APIModel] = int64(def.Capabilities.MaxOutputTokens)
			}
		}
	}

	tests := []struct {
		apiModel string
		captured int64
		capture  string
	}{
		{"claude-fable-5-1", 64000, "fable-5.1.json (2.1.261)"},
		{"claude-fable-5", 64000, "fable.json"},
		{"claude-haiku-4-5-20251001", 32000, "haiki.json"},
		{"claude-opus-4-8", 64000, "high-thinking.json, opus-switch.json"},
		{"claude-opus-5-5", 128000, "opus-5.5.json (2.1.280)"},
		{"claude-opus-5", 64000, "opus5.json"},
		{"claude-sonnet-5", 64000, "sonnet-5.json"},
	}
	for _, tc := range tests {
		t.Run(tc.apiModel, func(t *testing.T) {
			requested, ok := catalogMax[tc.apiModel]
			if !ok {
				t.Fatalf("%s has no anthropic mapping in the catalog", tc.apiModel)
			}
			client := NewClaudeCodeClient(llm.DriverOptions{
				Model:           models.Model{APIModel: tc.apiModel, ThinkingMode: "adaptive", CanReason: true},
				ReasoningEffort: "high",
				MaxTokens:       requested,
			})
			params := client.preparedMessages(nil, []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
			}, nil)
			if params.MaxTokens != tc.captured {
				t.Errorf("max_tokens = %d (catalog requested %d), want %d as captured in %s",
					params.MaxTokens, requested, tc.captured, tc.capture)
			}
		})
	}
}

// A request smaller than the captured value (validation, titling) is sent as
// asked: the capture is a ceiling, not a floor.
func TestClaudeCodeMaxTokens_SmallerRequestUnchanged(t *testing.T) {
	client := NewClaudeCodeClient(llm.DriverOptions{
		Model:     models.Model{APIModel: "claude-sonnet-5", ThinkingMode: "adaptive", CanReason: true},
		MaxTokens: 4096,
	})
	params := client.preparedMessages(nil, []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
	}, nil)
	if params.MaxTokens != 4096 {
		t.Errorf("max_tokens = %d, want 4096", params.MaxTokens)
	}
}
