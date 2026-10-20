// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// Every Setup entry point — the ones the API server, worker and gateway use,
// not only the daemon's — must stamp trace_id/span_id on a record logged with
// a span in its context. Prod logs from the three server roles carried no
// trace id at all while the trace handler was installed only on the daemon
// path, so no log line could be joined to its trace.
func TestEverySetupStampsTraceIDs(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	for _, entry := range setupEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			isolateLogging(t)
			read := entry.setup(t, Settings{Format: "json"})

			InfoContext(ctx, "info with span")
			WarnContext(ctx, "warn with span")
			ErrorContext(ctx, "error with span")
			Info("without span")

			recs := jsonLines(t, read())
			require.Len(t, recs, 4)
			for _, rec := range recs[:3] {
				assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", rec["trace_id"], "record %v", rec["msg"])
				assert.Equal(t, "0102030405060708", rec["span_id"], "record %v", rec["msg"])
			}
			assert.NotContains(t, recs[3], "trace_id")
		})
	}
}
