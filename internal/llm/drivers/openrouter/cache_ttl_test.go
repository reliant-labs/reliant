// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/cache"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// ttlMockTool is a minimal tools.Tool so the request carries real tool
// definitions — the last one is where the tool breakpoint lands.
type ttlMockTool struct {
	name string
}

func (m *ttlMockTool) Name() string        { return m.name }
func (m *ttlMockTool) Description() string { return "mock " + m.name }
func (m *ttlMockTool) ParamSchema() *jsonschema.Schema {
	type mockInput struct {
		Input string `json:"input" jsonschema:"description=Test input"`
	}
	return jsonschema.Reflect(&mockInput{})
}
func (m *ttlMockTool) RequiresPermission(*rctx.ToolContext, toolsPkg.ToolCall) (bool, error) {
	return false, nil
}
func (m *ttlMockTool) Run(*rctx.ToolContext, toolsPkg.ToolCall) (toolsPkg.ToolResponse, error) {
	return toolsPkg.ToolResponse{Content: "mock result"}, nil
}

// captureRequestBody stands in for OpenRouter and returns the raw body of the
// one request SendMessages sends. Asserting on the wire bytes rather than the
// intermediate maps is the point: it is the only view that proves what
// OpenRouter actually receives, through whichever send path the model selects.
func captureRequestBody(t *testing.T, modelID models.ModelID) map[string]any {
	t.Helper()

	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		raw = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-1","object":"chat.completion","model":"test",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer server.Close()

	client := NewClient(llm.DriverOptions{
		ApiKey:    "test-key",
		BaseURL:   server.URL,
		Model:     models.Model{ID: modelID},
		MaxTokens: 1024,
	})

	prompts := []string{"You are a helpful assistant.", "Be concise."}
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "list files"}}},
	}
	tools := []toolsPkg.Tool{&ttlMockTool{name: "shell"}, &ttlMockTool{name: "view"}}

	_, err := client.SendMessages(context.Background(), prompts, msgs, tools)
	require.NoError(t, err)
	require.NotEmpty(t, raw, "server captured no request body")

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	return body
}

// collectCacheControls walks a marshaled request body and returns every
// cache_control object it carries, in Anthropic's required prefix order
// (tools -> system/messages).
func collectCacheControls(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()

	var found []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if cc, ok := x["cache_control"]; ok {
				obj, ok := cc.(map[string]any)
				require.True(t, ok, "cache_control is not an object: %#v", cc)
				found = append(found, obj)
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
	for _, key := range []string{"tools", "messages"} {
		walk(body[key])
	}
	return found
}

// TestCacheControlTTL_AnthropicModel pins every breakpoint this driver emits
// for an anthropic/* model — last tool, both cached system prompts, last
// message — at the 1h extended TTL. A bare 5m breakpoint among them would both
// expire the prefix mid-turn and, in the wrong position, 400 the request on
// Anthropic's longer-TTL-first ordering rule.
func TestCacheControlTTL_AnthropicModel(t *testing.T) {
	body := captureRequestBody(t, models.Claude45Sonnet)
	require.Equal(t, "anthropic/claude-sonnet-4.5", body["model"])

	ccs := collectCacheControls(t, body)

	// 1 tool + 2 system + 1 message: Anthropic's 4-breakpoint maximum.
	require.Len(t, ccs, 4, "breakpoints: %+v", ccs)
	for i, cc := range ccs {
		require.Equal(t, map[string]any{"type": "ephemeral", "ttl": cache.ExtendedTTL}, cc,
			"breakpoint %d must be ephemeral at the extended TTL", i)
	}
}

// TestCacheControlTTL_NonAnthropicModel pins the other half of the contract:
// OpenRouter drops ttl for non-Anthropic providers, so this driver sends no
// cache_control at all for them rather than a breakpoint that silently does
// nothing.
func TestCacheControlTTL_NonAnthropicModel(t *testing.T) {
	body := captureRequestBody(t, models.GPT55)
	require.Equal(t, "openai/gpt-5.5", body["model"])

	require.Empty(t, collectCacheControls(t, body),
		"non-Anthropic models must carry no cache_control")
}
