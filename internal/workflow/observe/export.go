// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"fmt"
	"regexp"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// codeShape is a string that may leave as telemetry: one token of letters,
// digits and _ . : -, at most 64 bytes. No space, slash, # or @, so no
// sentence, URL, repository name or email (design section 8.2). A heuristic
// that keeps accidents out, not a proof: a deliberate one-word leak passes it.
var codeShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

// SafeForExport is a stored value as it may leave as span or span-event
// telemetry: a bool or a number as it is, a code-shaped string as it is, and
// any other string as telemetry.ObserveWithheldFormat (its length, never its
// text). The full value stays in the stored row. This is the one place that
// decides what an author's expression may export; relaxing it for an org that
// opts into content capture is a change here alone.
func SafeForExport(v interface{}) attribute.Value {
	switch t := v.(type) {
	case bool:
		return attribute.BoolValue(t)
	case int64:
		return attribute.Int64Value(t)
	case float64:
		return attribute.Float64Value(t)
	case string:
		if codeShape.MatchString(t) {
			return attribute.StringValue(t)
		}
		return attribute.StringValue(fmt.Sprintf(telemetry.ObserveWithheldFormat, len(t)))
	default:
		s, _ := Storable(t).(string)
		return attribute.StringValue(fmt.Sprintf(telemetry.ObserveWithheldFormat, len(s)))
	}
}

// MetricAttributeValue is a value as it may label a metric: a bool, an
// integer or a code-shaped string, and anything else (a fraction, free text,
// a list) the literal telemetry.ObserveMetricOther, so a bad value cannot
// split a series (design section 6, the second cardinality layer).
func MetricAttributeValue(v interface{}) attribute.Value {
	switch t := Storable(v).(type) {
	case bool:
		return attribute.BoolValue(t)
	case int64:
		return attribute.Int64Value(t)
	case string:
		if codeShape.MatchString(t) {
			return attribute.StringValue(t)
		}
	}
	return attribute.StringValue(telemetry.ObserveMetricOther)
}

// Annotate puts the result on the node's span: each attribute as
// reliant.observe.<key>, each event as a span event with the author's name and
// keys, and how the evaluation went (telemetry.AttrNodeObserveErrors,
// AttrNodeObserveTruncated). Every value goes through SafeForExport; no error
// text is ever put on the span.
func Annotate(span trace.Span, r Result) {
	if span == nil || !span.IsRecording() {
		return
	}
	keys := make([]string, 0, len(r.Attributes))
	for k := range r.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]attribute.KeyValue, 0, len(keys)+2)
	for _, k := range keys {
		if v := r.Attributes[k]; v != nil {
			attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(telemetry.AttrObservePrefix + k), Value: SafeForExport(v)})
		}
	}
	if n := len(r.Errors); n > 0 {
		attrs = append(attrs, attribute.Int(telemetry.AttrNodeObserveErrors, n))
	}
	if r.Truncated {
		attrs = append(attrs, attribute.Bool(telemetry.AttrNodeObserveTruncated, true))
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	for _, ev := range r.Events {
		span.AddEvent(ev.Name, trace.WithAttributes(eventAttributes(ev.Attributes)...))
	}
}

func eventAttributes(m map[string]interface{}) []attribute.KeyValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		if v := m[k]; v != nil {
			out = append(out, attribute.KeyValue{Key: attribute.Key(k), Value: SafeForExport(v)})
		}
	}
	return out
}
