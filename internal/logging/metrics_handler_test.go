// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCounter installs a fresh dead-end counter and restores the previous
// one, so tests do not leak state into each other or into a running process.
func newTestCounter(t *testing.T) *prometheus.CounterVec {
	t.Helper()

	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "test_dead_end_errors_total"},
		[]string{"level", "package", "message"},
	)

	previous := deadEndCounter
	SetDeadEndErrorCounter(counter)
	t.Cleanup(func() { SetDeadEndErrorCounter(previous) })

	return counter
}

func handleMessage(t *testing.T, msg string) error {
	t.Helper()

	handler := newMetricsHandler(slog.NewTextHandler(io.Discard, nil))
	record := slog.NewRecord(time.Time{}, slog.LevelWarn, msg, 0)
	return handler.Handle(context.Background(), record)
}

// A message longer than the cardinality clamp must be cut on a rune boundary.
// Slicing bytes splits the em dash in "…real interrupt — failing the turn…"
// mid-character, and Prometheus PANICS on a label value that is not valid
// UTF-8 — taking down the process from inside a log call.
func TestMetricsHandler_LongMultiByteMessageDoesNotPanic(t *testing.T) {
	counter := newTestCounter(t)

	msg := "[CallLLM] Stream cancelled by a failed heartbeat RPC, not by a real interrupt — " +
		"failing the turn so it is retried rather than reported as a completed turn"
	require.Greater(t, len(msg), maxMessageLabelRunes, "message must exceed the clamp to exercise truncation")

	assert.NotPanics(t, func() {
		require.NoError(t, handleMessage(t, msg))
	})

	label := onlyMessageLabel(t, counter)
	assert.True(t, utf8.ValidString(label), "label value must stay valid UTF-8: %q", label)
	assert.LessOrEqual(t, utf8.RuneCountInString(label), maxMessageLabelRunes)
	assert.True(t, strings.HasPrefix(msg, label), "label must be a prefix of the message")
}

// A message that is already invalid UTF-8 — raw bytes from command output, a
// filename, a provider payload — must not reach the counter unsanitized.
func TestMetricsHandler_InvalidUTF8MessageDoesNotPanic(t *testing.T) {
	counter := newTestCounter(t)

	assert.NotPanics(t, func() {
		require.NoError(t, handleMessage(t, "shell failed: \xff\xfe bad bytes"))
	})

	assert.True(t, utf8.ValidString(onlyMessageLabel(t, counter)))
}

func TestMetricsHandler_ShortMessagePassesThrough(t *testing.T) {
	counter := newTestCounter(t)

	require.NoError(t, handleMessage(t, "stream closed early"))

	assert.Equal(t, "stream closed early", onlyMessageLabel(t, counter))
}

// onlyMessageLabel returns the message label of the single series recorded on
// counter, failing the test if the handler recorded anything else.
func onlyMessageLabel(t *testing.T, counter *prometheus.CounterVec) string {
	t.Helper()

	metrics := make(chan prometheus.Metric, 8)
	counter.Collect(metrics)
	close(metrics)

	var labels []string
	for metric := range metrics {
		var written dto.Metric
		require.NoError(t, metric.Write(&written))
		for _, pair := range written.GetLabel() {
			if pair.GetName() == "message" {
				labels = append(labels, pair.GetValue())
			}
		}
	}

	require.Len(t, labels, 1, "expected exactly one recorded series")
	return labels[0]
}
