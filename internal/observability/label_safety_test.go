// Copyright (c) 2025 Reliant Labs
package observability

import (
	"testing"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The invalid byte here is a truncated em dash — the exact shape that killed
// the temporal worker when the slog handler byte-sliced a log message.
const truncatedEmDash = "cancelled by heartbeat \xe2\x80"

// Prometheus panics rather than erroring on an invalid label value, so an
// unsanitized value takes down the process from inside instrumentation. The
// wrappers must absorb that at the metric boundary, for every call site.
func TestSafeCounterVec_InvalidUTF8DoesNotPanic(t *testing.T) {
	counter := newCounterVec(
		prometheus.CounterOpts{Name: "test_counter_total"},
		[]string{"reason"},
	)

	assert.NotPanics(t, func() {
		counter.WithLabelValues(truncatedEmDash).Inc()
	})
	assert.NotPanics(t, func() {
		counter.With(prometheus.Labels{"reason": truncatedEmDash}).Inc()
	})

	for _, value := range collectLabel(t, counter, "reason") {
		assert.True(t, utf8.ValidString(value), "recorded label must be valid UTF-8: %q", value)
	}
}

func TestSafeHistogramVec_InvalidUTF8DoesNotPanic(t *testing.T) {
	histogram := newHistogramVec(
		prometheus.HistogramOpts{Name: "test_duration_seconds"},
		[]string{"provider"},
	)

	assert.NotPanics(t, func() {
		histogram.WithLabelValues(truncatedEmDash).Observe(0.1)
	})
	assert.NotPanics(t, func() {
		histogram.With(prometheus.Labels{"provider": truncatedEmDash}).Observe(0.1)
	})

	for _, value := range collectLabel(t, histogram, "provider") {
		assert.True(t, utf8.ValidString(value), "recorded label must be valid UTF-8: %q", value)
	}
}

// Valid values must reach Prometheus byte-for-byte — sanitizing must not
// quietly rewrite ordinary labels, including non-ASCII ones that are fine.
func TestValidUTF8Values_PassesValidInputThrough(t *testing.T) {
	values := []string{"anthropic", "café — ok", "success"}

	got := validUTF8Values(values)

	assert.Equal(t, values, got)
	assert.Equal(t, &values[0], &got[0], "valid input must not be copied on the hot path")
}

func TestValidUTF8Values_StripsOnlyMalformedBytes(t *testing.T) {
	got := validUTF8Values([]string{"ok", truncatedEmDash})

	assert.Equal(t, []string{"ok", "cancelled by heartbeat "}, got)
}

func TestValidUTF8Labels_PassesValidInputThrough(t *testing.T) {
	labels := prometheus.Labels{"provider": "anthropic", "status": "success"}

	assert.Equal(t, labels, validUTF8Labels(labels))
	assert.Equal(t,
		prometheus.Labels{"provider": "cancelled by heartbeat "},
		validUTF8Labels(prometheus.Labels{"provider": truncatedEmDash}),
	)
}

// collectLabel returns the given label's value from every series on c.
func collectLabel(t *testing.T, collector prometheus.Collector, name string) []string {
	t.Helper()

	metrics := make(chan prometheus.Metric, 16)
	collector.Collect(metrics)
	close(metrics)

	var values []string
	for metric := range metrics {
		var written dto.Metric
		require.NoError(t, metric.Write(&written))
		for _, pair := range written.GetLabel() {
			if pair.GetName() == name {
				values = append(values, pair.GetValue())
			}
		}
	}
	require.NotEmpty(t, values, "expected at least one recorded series")
	return values
}
