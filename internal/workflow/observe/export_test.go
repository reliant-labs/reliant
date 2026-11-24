// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

func TestSafeForExport(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]attribute.Value{
		"no_cut":                     attribute.StringValue("no_cut"),
		"review:changes":             attribute.StringValue("review:changes"),
		"HIGH":                       attribute.StringValue("HIGH"),
		"chat-7f3a9c":                attribute.StringValue("chat-7f3a9c"),
		"reliant-labs/reliant#3478":  attribute.StringValue("[withheld:25b]"),
		"someone@example.com":        attribute.StringValue("[withheld:19b]"),
		"main has not moved":         attribute.StringValue("[withheld:18b]"),
		"https://example.com":        attribute.StringValue("[withheld:19b]"),
		"_leading_underscore":        attribute.StringValue("[withheld:19b]"),
		strings.Repeat("a", 65):      attribute.StringValue("[withheld:65b]"),
		"":                           attribute.StringValue("[withheld:0b]"),
		"line\nbreak":                attribute.StringValue("[withheld:10b]"),
		"value-with.dots_and:colons": attribute.StringValue("value-with.dots_and:colons"),
	} {
		assert.Equal(t, want, SafeForExport(in), "%q", in)
	}
	assert.Equal(t, attribute.BoolValue(true), SafeForExport(true))
	assert.Equal(t, attribute.Int64Value(14), SafeForExport(int64(14)))
	assert.Equal(t, attribute.Float64Value(0.5), SafeForExport(0.5))
}

func TestMetricAttributeValue(t *testing.T) {
	t.Parallel()
	assert.Equal(t, attribute.StringValue("no_cut"), MetricAttributeValue("no_cut"))
	assert.Equal(t, attribute.Int64Value(3), MetricAttributeValue(int64(3)))
	assert.Equal(t, attribute.BoolValue(false), MetricAttributeValue(false))
	for _, other := range []interface{}{0.5, "two words", "a/b#1", []interface{}{"x"}, nil} {
		assert.Equal(t, attribute.StringValue(telemetry.ObserveMetricOther), MetricAttributeValue(other), "%v", other)
	}
}

// The node span carries each attribute under reliant.observe.<key> and each
// event under the author's name, every value through SafeForExport, and how
// the evaluation went as a count; no free text, and no error text, ever
// reaches the span.
func TestAnnotate(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	_, span := tp.Tracer("test").Start(context.Background(), "workflow.node plan")

	const secretDetail = "rc.sh refused: token ghp_xxx expired for someone@example.com"
	res := fixedEval.Evaluate(nil, planScope())
	res.Attributes = map[string]interface{}{"trigger": "schedule", "detail": Storable(secretDetail), "count": int64(3), "missing": nil}
	res.Events = []Event{{Name: "decision", Attributes: map[string]interface{}{"name": "cut", "value": "no_cut", "reason": "main_unchanged", "pr": "a/b#1"}}}
	res.Errors = []Error{{Kind: telemetry.ObserveErrorEval, Message: "attributes.broken: no such key: " + secretDetail}}
	res.Truncated = true
	Annotate(span, res)
	span.End()

	spans := rec.Ended()
	require.Len(t, spans, 1)
	got := map[string]attribute.Value{}
	for _, kv := range spans[0].Attributes() {
		got[string(kv.Key)] = kv.Value
	}
	detail, _ := Storable(secretDetail).(string)
	assert.Equal(t, map[string]attribute.Value{
		"reliant.observe.trigger":          attribute.StringValue("schedule"),
		"reliant.observe.detail":           attribute.StringValue("[withheld:" + itoa(len(detail)) + "b]"),
		"reliant.observe.count":            attribute.Int64Value(3),
		telemetry.AttrNodeObserveErrors:    attribute.IntValue(1),
		telemetry.AttrNodeObserveTruncated: attribute.BoolValue(true),
	}, got)

	require.Len(t, spans[0].Events(), 1)
	ev := spans[0].Events()[0]
	assert.Equal(t, "decision", ev.Name)
	evAttrs := map[string]string{}
	for _, kv := range ev.Attributes {
		evAttrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, map[string]string{"name": "cut", "value": "no_cut", "reason": "main_unchanged", "pr": "[withheld:5b]"}, evAttrs)

	// Nothing of the free text, the error or the credential is on the span.
	dump, err := json.Marshal(struct {
		A, E, S interface{}
	}{spans[0].Attributes(), spans[0].Events(), spans[0].Status()})
	require.NoError(t, err)
	for _, secret := range []string{"ghp_", "someone@example.com", "expired", "no such key"} {
		assert.NotContains(t, string(dump), secret)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
