// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// wireCacheControl is one cache_control object as Vertex AI receives it.
type wireCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl"`
}

// collectClaudeCacheControls marshals a full Claude-on-Vertex request and
// returns every cache_control it carries, in Anthropic's prefix order
// (tools -> system -> messages). Walking the marshaled JSON rather than the Go
// structs is the point: it is the only view that proves what Vertex receives.
func collectClaudeCacheControls(t *testing.T, req *claudeRequest) []wireCacheControl {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	var found []wireCacheControl
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if cc, ok := x["cache_control"]; ok {
				b, err := json.Marshal(cc)
				if err != nil {
					t.Fatalf("marshal cache_control: %v", err)
				}
				var w wireCacheControl
				if err := json.Unmarshal(b, &w); err != nil {
					t.Fatalf("unmarshal cache_control: %v", err)
				}
				found = append(found, w)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	for _, key := range []string{"tools", "system", "messages"} {
		walk(body[key])
	}
	return found
}

func ttlTestClient(disableCache bool) *VertexAIClient {
	return &VertexAIClient{options: llm.DriverOptions{
		Model:        models.Model{ID: models.VertexClaude55Opus, APIModel: "claude-opus-5-5"},
		MaxTokens:    1024,
		DisableCache: disableCache,
	}}
}

func ttlTestConversation() ([]string, []message.Message, []tools.Tool) {
	prompts := []string{"You are a helpful assistant.", "Be concise."}
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "list files"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "toolu_1", Name: "shell", Input: `{"command":"ls"}`, Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "toolu_1", Name: "shell", Content: "a.go\nb.go"},
		}},
	}
	toolsList := []tools.Tool{tools.NewSetTitleTool(), tools.NewListPresetsTool()}
	return prompts, msgs, toolsList
}

// TestCacheControlTTL_VertexClaude pins every breakpoint this driver emits
// (last tool, both cached system prompts, last message) at a bare
// {type:"ephemeral"}: the API's 5m default, no ttl.
func TestCacheControlTTL_VertexClaude(t *testing.T) {
	c := ttlTestClient(false)
	prompts, msgs, toolsList := ttlTestConversation()

	req := c.buildClaudeRequest(prompts, msgs, toolsList, false)
	ccs := collectClaudeCacheControls(t, req)

	// 1 tool + 2 system + 1 message: Anthropic's 4-breakpoint maximum.
	if len(ccs) != 4 {
		t.Fatalf("got %d breakpoints, want 4: %+v", len(ccs), ccs)
	}
	for i, cc := range ccs {
		if cc.Type != "ephemeral" {
			t.Errorf("breakpoint %d type = %q, want ephemeral", i, cc.Type)
		}
		if cc.TTL != "" {
			t.Errorf("breakpoint %d ttl = %q, want none", i, cc.TTL)
		}
	}
}

func TestCacheControlTTL_VertexClaude_DisableCache(t *testing.T) {
	c := ttlTestClient(true)
	prompts, msgs, toolsList := ttlTestConversation()

	req := c.buildClaudeRequest(prompts, msgs, toolsList, false)
	if ccs := collectClaudeCacheControls(t, req); len(ccs) != 0 {
		t.Fatalf("DisableCache emitted %d breakpoints: %+v", len(ccs), ccs)
	}
}
