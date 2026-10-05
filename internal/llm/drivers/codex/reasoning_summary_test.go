// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Events copied from research/probe-runs/wire/gpt-5.5_codex/001-chatgpt.com.log.
// The summary text arrives in response.reasoning_summary_text.delta; the
// summary_part.added event carries an empty part.
func TestStreamResponse_ReasoningSummaryDeltaIsThinking(t *testing.T) {
	srv := sseResponsesServer(t,
		sseEvent("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`),
		sseEvent("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","summary":[]},"output_index":0,"sequence_number":1}`),
		sseEvent("response.reasoning_summary_part.added", `{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"part":{"type":"summary_text","text":""},"sequence_number":3,"summary_index":0}`),
		sseEvent("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"**Calculating final sum**","item_id":"rs_1","obfuscation":"AJsJgRi","output_index":0,"sequence_number":4,"summary_index":0}`),
		sseEvent("response.reasoning_summary_text.done", `{"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"sequence_number":5,"summary_index":0,"text":"**Calculating final sum**"}`),
		sseEvent("response.completed", `{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":120,"total_tokens":130,"output_tokens_details":{"reasoning_tokens":94}},"output":[]}}`),
	)

	final, events := streamAll(t, srv)

	var thinking strings.Builder
	for _, ev := range events {
		if ev.Type == llm.EventThinkingDelta {
			thinking.WriteString(ev.Thinking)
		}
	}
	if got := thinking.String(); got != "**Calculating final sum**" {
		t.Errorf("thinking text = %q, want the summary delta", got)
	}
	if final.Usage.ReasoningTokens != 94 {
		t.Errorf("ReasoningTokens = %d, want 94", final.Usage.ReasoningTokens)
	}
	if final.Usage.OutputTokens != 120 {
		t.Errorf("OutputTokens = %d, want 120 (reasoning is a subset, not added)", final.Usage.OutputTokens)
	}
}

func streamAll(t *testing.T, srv *httptest.Server) (*llm.DriverResponse, []llm.DriverEvent) {
	t.Helper()
	client := &CodexClient{
		options:   llm.DriverOptions{Model: models.Model{ID: models.GPT53Codex, APIModel: "gpt-5.3-codex"}, MaxTokens: 1024},
		sessionID: "test-session",
		client:    llm.NewOpenAISDKClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0)),
	}
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	var final *llm.DriverResponse
	var events []llm.DriverEvent
	for ev := range client.StreamResponse(context.Background(), []string{"be helpful"}, msgs, nil) {
		if ev.Type == llm.EventError {
			t.Fatalf("stream error: %v", ev.Error)
		}
		if ev.Type == llm.EventComplete {
			final = ev.Response
		}
		events = append(events, ev)
	}
	if final == nil {
		t.Fatal("no EventComplete")
	}
	return final, events
}
