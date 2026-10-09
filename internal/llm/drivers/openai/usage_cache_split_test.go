// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestStreamResponse_ResponsesUsageCacheSplit: on the Responses API
// input_tokens INCLUDES input_tokens_details.cached_tokens. TokenCount is the
// reported total (cached counted once); InputTokens is the uncached remainder
// and CacheReadInputTokens the cached prefix, matching every other driver so
// `[CallLLM] Usage` reports the real prompt size and a real cache-hit rate.
// Copilot's Responses traffic goes through this same driver.
func TestStreamResponse_ResponsesUsageCacheSplit(t *testing.T) {
	completed := `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":5000,"input_tokens_details":{"cached_tokens":4096},"output_tokens":300,"output_tokens_details":{"reasoning_tokens":200},"total_tokens":5300}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`)
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
	}))
	defer srv.Close()

	c := NewClient(llm.DriverOptions{
		Model:     models.Model{ID: models.GPT55, APIModel: "gpt-5.5", PreferredEndpoint: "responses"},
		ApiKey:    "k",
		BaseURL:   srv.URL,
		MaxTokens: 1024,
	})
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}

	var final *llm.DriverResponse
	for ev := range c.StreamResponse(context.Background(), nil, msgs, nil) {
		switch ev.Type {
		case llm.EventError:
			t.Fatalf("stream error: %v", ev.Error)
		case llm.EventComplete:
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("no completion event")
	}
	u := final.Usage
	if u.TokenCount != 5300 {
		t.Errorf("TokenCount = %d, want total_tokens 5300 (cached counted once)", u.TokenCount)
	}
	if u.InputTokens != 904 || u.CacheReadInputTokens != 4096 || u.CachedInputTokens != 4096 {
		t.Errorf("input split = uncached %d / cacheRead %d / cached %d, want 904/4096/4096",
			u.InputTokens, u.CacheReadInputTokens, u.CachedInputTokens)
	}
	if prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens; prompt != 5000 {
		t.Errorf("prompt total = %d, want input_tokens 5000", prompt)
	}
	if u.OutputTokens != 300 || u.ReasoningTokens != 200 {
		t.Errorf("output = %d (reasoning %d), want 300 (200)", u.OutputTokens, u.ReasoningTokens)
	}
}
