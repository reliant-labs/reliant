// Copyright (c) 2025 Reliant Labs
package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// The tracer provider Init installs keeps every AI span whatever the base
// sampler says (forge's genai.KeepSampler, which observe.Setup wraps the
// OTEL_TRACES_SAMPLER sampler in): token and cost totals are sums over AI
// spans, so a sampled-away model call is a missing cost. Any other span is
// still the base sampler's. The test sets the environment and swaps the global
// tracer provider, so it does not run in parallel.
func TestInitKeepsAISpansUnderADroppingSampler(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_PROFILES_EXPORTER", "none")
	t.Setenv("FORGE_OBSERVE_RESOURCE_METRICS", "off")
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	provider, err := Init(Config{
		ServiceName:  "reliant-worker",
		OTLPEnabled:  true,
		OTLPEndpoint: collector.URL,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Shutdown() })

	tracer := otel.Tracer("test")
	for name, attrs := range map[string][]attribute.KeyValue{
		"a model call":    {attribute.String(telemetry.AttrGenAIOperationName, telemetry.GenAIOpChat)},
		"a tool call":     {attribute.String(telemetry.AttrGenAIOperationName, telemetry.GenAIOpExecuteTool)},
		"a workflow node": {attribute.String(telemetry.AttrAINodeID, "call_llm")},
	} {
		_, span := tracer.Start(context.Background(), name, trace.WithAttributes(attrs...))
		assert.True(t, span.SpanContext().IsSampled(), "%s is kept", name)
		span.End()
	}
	_, plain := tracer.Start(context.Background(), "GET /healthz")
	assert.False(t, plain.SpanContext().IsSampled(), "a span that is not an AI span is the base sampler's")
	plain.End()

	// reliant's own starters open spans the sampler keeps.
	_, node := telemetry.StartNodeSpan(telemetry.WithRunScope(context.Background(), telemetry.RunScope{NodeID: "call_llm", OrgID: "org-1"}))
	assert.True(t, node.SpanContext().IsSampled())
	node.End()
}
