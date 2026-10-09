// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Captured shape of a Codex response.completed event's usage block. Before the
// fix only TotalTokens was copied, so every Codex turn billed 0 output tokens.
const completedWithUsage = `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024},"output_tokens":87,"output_tokens_details":{"reasoning_tokens":64},"total_tokens":1287}}}`

func TestStreamResponse_PopulatesTokenUsageBreakdown(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
		sseEvent("response.completed", completedWithUsage),
	)
	u := streamOnce(t, srv).Usage
	assertCodexUsage(t, u)
}

// assertCodexUsage pins how the completedWithUsage block maps onto
// llm.TokenUsage. In Responses semantics input_tokens (1200) ALREADY includes
// the cached prefix (1024), so:
//   - TokenCount is the reported total_tokens, once — cached tokens are not
//     added on top (it is the context size that drives compaction and trimming);
//   - InputTokens is the UNCACHED remainder and CacheReadInputTokens the cached
//     prefix, the split every other driver reports, so that
//     InputTokens+CacheRead+CacheCreation (`[CallLLM] Usage` promptTokens) is
//     the real prompt size and cacheReadPct is not a permanent 0 for codex.
func assertCodexUsage(t *testing.T, u llm.TokenUsage) {
	t.Helper()
	if u.TokenCount != 1287 {
		t.Errorf("TokenCount = %d, want total_tokens 1287 (cached counted once)", u.TokenCount)
	}
	if u.InputTokens != 176 || u.CacheReadInputTokens != 1024 || u.CachedInputTokens != 1024 {
		t.Errorf("input split = uncached %d / cacheRead %d / cached %d, want 176/1024/1024",
			u.InputTokens, u.CacheReadInputTokens, u.CachedInputTokens)
	}
	if prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens; prompt != 1200 {
		t.Errorf("prompt total = %d, want input_tokens 1200", prompt)
	}
	if u.OutputTokens != 87 || u.ReasoningTokens != 64 {
		t.Errorf("output = %d (reasoning %d), want 87 (64)", u.OutputTokens, u.ReasoningTokens)
	}
}

// The non-streaming path maps the same usage block the same way.
func TestSendMessages_PopulatesTokenUsageBreakdown(t *testing.T) {
	var body struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal([]byte(completedWithUsage), &body); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body.Response)
	}))
	t.Cleanup(srv.Close)

	client := &CodexClient{
		options: llm.DriverOptions{
			Model:     models.Model{ID: models.GPT53Codex, APIModel: "gpt-5.3-codex"},
			MaxTokens: 1024,
		},
		sessionID: "test-session",
		client: llm.NewOpenAISDKClient(
			option.WithBaseURL(srv.URL),
			option.WithAPIKey("test-key"),
			option.WithMaxRetries(0),
		),
	}
	resp, err := client.SendMessages(context.Background(), []string{"be helpful"},
		[]message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}, nil)
	if err != nil {
		t.Fatalf("SendMessages: %v", err)
	}
	assertCodexUsage(t, resp.Usage)
}
