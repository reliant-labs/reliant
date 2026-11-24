// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// reliant.gateway.call_id names a call Reliant's own gateway billed. A call
// through any other provider never carries it, even when the endpoint is a
// LiteLLM of the user's own that sends x-litellm-call-id.
func TestNonGatewayProviderNeverSetsGatewayCallID(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	completed := `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-litellm-call-id", "someone-elses-litellm")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
	}))
	defer srv.Close()

	model := models.Model{ID: models.GPT55, APIModel: "gpt-5.5", PreferredEndpoint: "responses"}
	c := NewClient(llm.DriverOptions{Model: model, ApiKey: "k", BaseURL: srv.URL, MaxTokens: 1024})
	msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}

	ctx, span := llm.StartGenAISpan(context.Background(), "openai", model, true)
	for ev := range c.StreamResponse(ctx, nil, msgs, nil) {
		span.Observe(ev)
		require.NotEqual(t, llm.EventError, ev.Type, "stream error: %v", ev.Error)
	}
	span.End()

	// The chat span and the transport's HTTP spans under it.
	ended := rec.Ended()
	require.NotEmpty(t, ended)
	for _, s := range ended {
		for _, kv := range s.Attributes() {
			require.NotEqual(t, telemetry.AttrGatewayCallID, string(kv.Key), "span %q: a non-gateway provider recorded a gateway call id", s.Name())
		}
	}
}
