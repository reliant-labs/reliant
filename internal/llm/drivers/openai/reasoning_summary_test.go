// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Event shapes copied from research/probe-runs/wire/gpt-5.5_codex (same
// Responses stream format this driver parses).
func TestStreamResponse_ReasoningSummaryDeltaIsThinking(t *testing.T) {
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","summary":[]},"output_index":0,"sequence_number":1}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"part":{"type":"summary_text","text":""},"sequence_number":3,"summary_index":0}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"**Calculating final sum**","item_id":"rs_1","output_index":0,"sequence_number":4,"summary_index":0}`,
		`{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"104104"}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":120,"total_tokens":130,"output_tokens_details":{"reasoning_tokens":94}},"output":[]}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			var typ struct{ T string }
			typ.T = e[len(`{"type":"`) : strings.Index(e[len(`{"type":"`):], `"`)+len(`{"type":"`)]
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.T, e)
		}
	}))
	defer srv.Close()

	c := NewClient(llm.DriverOptions{
		Model:     models.Model{ID: models.GPT55, APIModel: "gpt-5.5", PreferredEndpoint: "responses"},
		ApiKey:    "k",
		BaseURL:   srv.URL,
		MaxTokens: 1024,
	})
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}

	var thinking strings.Builder
	var final *llm.DriverResponse
	for ev := range c.StreamResponse(context.Background(), nil, msgs, nil) {
		switch ev.Type {
		case llm.EventError:
			t.Fatalf("stream error: %v", ev.Error)
		case llm.EventThinkingDelta:
			thinking.WriteString(ev.Thinking)
		case llm.EventComplete:
			final = ev.Response
		}
	}
	if thinking.String() != "**Calculating final sum**" {
		t.Errorf("thinking = %q", thinking.String())
	}
	if final == nil || final.Usage.ReasoningTokens != 94 || final.Usage.OutputTokens != 120 {
		t.Errorf("usage = %+v, want reasoning 94 / output 120", final)
	}
}
