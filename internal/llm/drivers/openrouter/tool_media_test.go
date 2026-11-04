// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Both OpenRouter converters send a turn's tool-result media as ONE user
// message after all of its tool messages. They used to emit one after each
// result, which put a user message between two tool messages whenever a turn
// ran several tools — a request OpenAI-format endpoints reject.
func TestConverters_ToolResultMediaFollowsAllToolMessages(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G'}
	pdf := []byte("%PDF-1.4")
	history := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call_view", Name: "view", Input: `{}`},
			message.ToolCall{ID: "call_bash", Name: "bash", Input: `{}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_view", Content: "Image file: shot.png", BinaryParts: []message.BinaryContent{
				{MIMEType: "image/png", Data: png},
				{Path: "/w/report.pdf", MIMEType: "application/pdf", Data: pdf},
			}},
			message.ToolResult{ToolCallID: "call_bash", Content: "ok"},
		}},
	}

	for _, tc := range []struct {
		model   string
		convert func(*Client) any
	}{
		{"google/gemini-3-pro", func(c *Client) any { return c.convertMessagesForGemini(nil, history) }},
		{"anthropic/claude-opus-4-8", func(c *Client) any { return c.convertMessagesWithCacheControl(nil, history) }},
	} {
		t.Run(tc.model, func(t *testing.T) {
			client := &Client{OpenaiClient: &openai.OpenaiClient{Options: llm.DriverOptions{
				Model: models.Model{APIModel: tc.model}, DisableCache: true,
			}}}
			data, err := json.Marshal(tc.convert(client))
			if err != nil {
				t.Fatal(err)
			}
			var out []map[string]any
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			roles := make([]string, 0, len(out))
			for _, m := range out {
				roles = append(roles, m["role"].(string))
			}
			if got := strings.Join(roles, ","); !strings.HasSuffix(got, "tool,tool,user") {
				t.Fatalf("roles = %s, want the tool messages contiguous, then one user message", got)
			}
			follow, _ := json.Marshal(out[len(out)-1])
			for _, want := range []string{
				`"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `"`,
				`"file_data":"data:application/pdf;base64,` + base64.StdEncoding.EncodeToString(pdf) + `"`,
				`"filename":"report.pdf"`,
			} {
				if !strings.Contains(string(follow), want) {
					t.Errorf("follow-up is missing %s: %s", want, follow)
				}
			}
		})
	}
}
