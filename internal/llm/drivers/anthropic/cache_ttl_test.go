// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// wireCacheControl is one cache_control object as it appears on the wire.
type wireCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl"`
}

// collectCacheControls marshals a full request and returns every cache_control
// it carries, in wire order (tools -> system -> messages). Walking the marshaled
// JSON rather than the param structs is the point: it is the only view that
// proves what Anthropic actually receives.
func collectCacheControls(t *testing.T, params anthropic.MessageNewParams) []wireCacheControl {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	var found []wireCacheControl
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if cc, ok := x["cache_control"]; ok {
				b, err := json.Marshal(cc)
				require.NoError(t, err)
				var w wireCacheControl
				require.NoError(t, json.Unmarshal(b, &w))
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
	// Walk in Anthropic's prefix order so a failure message reads naturally.
	for _, key := range []string{"tools", "system", "messages"} {
		walk(body[key])
	}
	return found
}

func ttlTestConversation() ([]string, []message.Message, []toolsPkg.Tool) {
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
	tools := []toolsPkg.Tool{
		&mockTool{name: "shell", description: "Run a command"},
		&mockTool{name: "view", description: "Read a file"},
	}
	return prompts, msgs, tools
}

// requireTTLOrdering asserts Anthropic's rule that a longer-TTL breakpoint never
// follows a shorter one in request order (tools -> system -> messages).
func requireTTLOrdering(t *testing.T, ccs []wireCacheControl) {
	t.Helper()
	rank := func(ttl string) int {
		if ttl == "1h" {
			return 1
		}
		return 0
	}
	for i := 1; i < len(ccs); i++ {
		require.LessOrEqual(t, rank(ccs[i].TTL), rank(ccs[i-1].TTL),
			"breakpoint %d (ttl %q) is longer-lived than the one before it (ttl %q): %+v", i, ccs[i].TTL, ccs[i-1].TTL, ccs)
	}
}

// TestCacheControlTTL_AnthropicAPI pins every breakpoint the direct Anthropic
// client emits (last tool, cached system prompts, last message) at a bare
// {type:"ephemeral"}: the API's 5m default, no ttl.
func TestCacheControlTTL_AnthropicAPI(t *testing.T) {
	client := NewAnthropicClient(llm.DriverOptions{
		Model:     models.Model{ID: models.Claude45Sonnet, APIModel: "claude-sonnet-4-5"},
		MaxTokens: 1024,
	})
	prompts, msgs, tools := ttlTestConversation()

	params := client.preparedMessages(prompts, client.convertMessages(msgs), client.convertTools(tools))
	ccs := collectCacheControls(t, params)

	// 1 tool + 2 system + 1 message: Anthropic's 4-breakpoint maximum.
	require.Len(t, ccs, 4, "breakpoints: %+v", ccs)
	for i, cc := range ccs {
		require.Equal(t, "ephemeral", cc.Type, "breakpoint %d", i)
		require.Empty(t, cc.TTL, "breakpoint %d must not carry a ttl", i)
	}
	requireTTLOrdering(t, ccs)
}

// TestCacheControlTTL_ClaudeCode pins the claude-code driver: the two base
// system blocks keep real Claude Code's ttl "1h" (see .dev/claude captures);
// every other breakpoint (caller system block, message) carries no ttl, and the
// 1h blocks precede all of them.
func TestCacheControlTTL_ClaudeCode(t *testing.T) {
	client := NewClaudeCodeClient(llm.DriverOptions{
		ApiKey:    "sk-ant-oat01-test",
		Model:     models.Model{ID: models.Claude55Opus, APIModel: apiModelOpus55},
		MaxTokens: 1024,
	})
	prompts, msgs, tools := ttlTestConversation()

	params := client.preparedMessages(prompts, client.convertMessages(msgs), client.convertTools(tools))
	ccs := collectCacheControls(t, params)

	// 2 base system blocks + 1 caller system block + 1 message.
	require.Len(t, ccs, 4, "breakpoints: %+v", ccs)
	for i, cc := range ccs {
		require.Equal(t, "ephemeral", cc.Type, "breakpoint %d", i)
		if i < 2 {
			require.Equal(t, "1h", cc.TTL, "base system block %d", i)
		} else {
			require.Empty(t, cc.TTL, "breakpoint %d must not carry a ttl", i)
		}
	}
	requireTTLOrdering(t, ccs)
}

// TestCacheControlTTL_ForeignHostIsBare pins clients built via
// NewAnthropicClientWithOptions (Copilot) at a bare {type:"ephemeral"}.
func TestCacheControlTTL_ForeignHostIsBare(t *testing.T) {
	client := NewAnthropicClientWithOptions(llm.DriverOptions{
		Model:     models.Model{ID: models.Claude45Sonnet, APIModel: "claude-sonnet-4.5"},
		MaxTokens: 1024,
	})
	prompts, msgs, tools := ttlTestConversation()

	params := client.preparedMessages(prompts, client.convertMessages(msgs), client.convertTools(tools))
	ccs := collectCacheControls(t, params)

	require.Len(t, ccs, 4, "breakpoints: %+v", ccs)
	for i, cc := range ccs {
		require.Equal(t, "ephemeral", cc.Type, "breakpoint %d", i)
		require.Empty(t, cc.TTL, "breakpoint %d must not carry a ttl", i)
	}
	requireTTLOrdering(t, ccs)
}
