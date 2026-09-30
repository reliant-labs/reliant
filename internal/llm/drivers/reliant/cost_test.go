package reliant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Cost is passthrough. Reliant holds no per-token price table, so the only
// number that may ever land in TokenUsage.Cost is the one the gateway
// (LiteLLM) reported for this exact request. Anything computed locally was a
// guess that drifted the moment a vendor changed a price, and it was being
// written to messages.cost as if it were authoritative.
//
// LiteLLM 1.83.10 reports it in three shapes, verified live:
//   - non-stream /v1/chat/completions: header x-litellm-response-cost
//   - non-stream /v1/messages:         only x-litellm-response-cost-original
//   - streams (include_cost_in_streaming_usage): the final usage event's
//     "usage":{..."cost":N}
//
// so these pin all three, plus the case that matters most: no cost reported
// means zero, never a locally derived substitute.

const costTestModelJSON = `{
	"id": "chatcmpl-1",
	"object": "chat.completion",
	"created": 1,
	"model": "claude-sonnet-5",
	"choices": [{
		"index": 0,
		"message": {"role": "assistant", "content": "hi"},
		"finish_reason": "stop"
	}],
	"usage": %s
}`

func TestSendMessages_CostComesFromGateway(t *testing.T) {
	// A model whose name screams "expensive". Nothing may infer a price from it.
	model := models.Model{ID: "claude-sonnet-5", APIModel: "claude-sonnet-5"}

	tests := []struct {
		name     string
		headers  map[string]string
		usage    string
		wantCost float64
	}{
		{
			name:     "x-litellm-response-cost header",
			headers:  map[string]string{"x-litellm-response-cost": "7.9e-05"},
			usage:    `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}`,
			wantCost: 7.9e-05,
		},
		{
			// /v1/messages omits the plain header entirely.
			name:     "only x-litellm-response-cost-original header",
			headers:  map[string]string{"x-litellm-response-cost-original": "7.9e-05"},
			usage:    `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}`,
			wantCost: 7.9e-05,
		},
		{
			// -original is authoritative only when positive: LiteLLM emits a
			// zero there for requests it did not price, and treating that as
			// "cost is known to be zero" would mask the body's real number.
			name: "zero -original defers to usage.cost in the body",
			headers: map[string]string{
				"x-litellm-response-cost-original": "0",
			},
			usage:    `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150, "cost": 0.000079}`,
			wantCost: 7.9e-05,
		},
		{
			name:     "usage.cost in the body with no headers",
			usage:    `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150, "cost": 0.000079}`,
			wantCost: 7.9e-05,
		},
		{
			name:     "no cost reported anywhere is zero, whatever the model is called",
			usage:    `{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}`,
			wantCost: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range tt.headers {
					w.Header().Set(key, value)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, costTestModelJSON, tt.usage)
			}))
			defer srv.Close()

			client := NewClient(llm.DriverOptions{ApiKey: "test", BaseURL: srv.URL, Model: model})

			resp, err := client.SendMessages(context.Background(), nil, nil, nil)
			if err != nil {
				t.Fatalf("SendMessages: %v", err)
			}
			if resp.Usage.Cost != tt.wantCost {
				t.Errorf("Cost = %v, want %v — cost must be the gateway's number, not a local calculation", resp.Usage.Cost, tt.wantCost)
			}
			// Token counts are unrelated to the cost source and must survive.
			if resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 50 {
				t.Errorf("tokens = in %d / out %d, want 100 / 50", resp.Usage.InputTokens, resp.Usage.OutputTokens)
			}
		})
	}
}

// The stream carries no cost header; the number arrives in the final usage
// chunk, which the openai-go SDK has no field for. It must be read out of that
// chunk's raw JSON or it is lost.
func TestStreamResponse_CostComesFromFinalUsageChunk(t *testing.T) {
	const body = "data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"claude-sonnet-5",` +
		`"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"claude-sonnet-5",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"claude-sonnet-5","choices":[],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"cost":0.000079}}` + "\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := NewClient(llm.DriverOptions{
		ApiKey:  "test",
		BaseURL: srv.URL,
		Model:   models.Model{ID: "claude-sonnet-5", APIModel: "claude-sonnet-5"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var complete *llm.DriverResponse
	for event := range client.StreamResponse(ctx, nil, nil, nil) {
		switch event.Type {
		case llm.EventError:
			t.Fatalf("stream error: %v", event.Error)
		case llm.EventComplete:
			complete = event.Response
		}
	}
	if complete == nil {
		t.Fatal("stream produced no EventComplete")
	}
	if complete.Usage.Cost != 7.9e-05 {
		t.Errorf("Cost = %v, want 7.9e-05 — the final usage chunk's cost was dropped", complete.Usage.Cost)
	}
	if complete.Usage.InputTokens != 100 || complete.Usage.OutputTokens != 50 {
		t.Errorf("tokens = in %d / out %d, want 100 / 50", complete.Usage.InputTokens, complete.Usage.OutputTokens)
	}
}
