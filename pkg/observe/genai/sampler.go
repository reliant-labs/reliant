package genai

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// IsAISpan reports whether a span that starts with attrs is an AI span: it
// carries gen_ai.operation.name or any forge.ai.* attribute (a workflow node
// carries no operation, but a run's tree needs it).
func IsAISpan(attrs []attribute.KeyValue) bool {
	for _, kv := range attrs {
		if kv.Key == AttrOperationName || strings.HasPrefix(string(kv.Key), "forge.ai.") {
			return true
		}
	}
	return false
}

// KeepSampler returns a sampler that records and samples every AI span
// (IsAISpan over the span's start attributes) and leaves every other span to
// base. Token totals are sums over AI spans, so a sampled-away call is a
// missing cost: they are never sampled.
//
// A kept AI span under an unsampled parent is exported without that parent; it
// still counts in every total, and its own children (the client's HTTP spans,
// under a parent-based base) follow it.
func KeepSampler(base sdktrace.Sampler) sdktrace.Sampler {
	return keepSampler{base: base}
}

type keepSampler struct{ base sdktrace.Sampler }

func (s keepSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if IsAISpan(p.Attributes) {
		return sdktrace.SamplingResult{
			Decision:   sdktrace.RecordAndSample,
			Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState(),
		}
	}
	return s.base.ShouldSample(p)
}

func (s keepSampler) Description() string {
	return "KeepGenAI{" + s.base.Description() + "}"
}
