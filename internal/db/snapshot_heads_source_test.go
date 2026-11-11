// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The snapshot's chat_snapshot.update_heads span says where the heads came
// from: the materialized table, or the log for a chat whose heads the stream
// counter cannot vouch for. That attribute is how the rollout is watched (how
// many opens still pay the log read), so it must name the right source.
func TestGetLatestNonMessageUpdatesPerEntity_RecordsItsSource(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	sourceOf := func(chatID string) string {
		t.Helper()
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		spanCtx, span := tp.Tracer("test").Start(ctx, "chat_snapshot.update_heads")
		_, err := repo.GetLatestNonMessageUpdatesPerEntity(spanCtx, chatID)
		require.NoError(t, err)
		span.End()
		ended := rec.Ended()
		require.Len(t, ended, 1)
		for _, kv := range ended[0].Attributes() {
			if kv.Key == "chat_snapshot.update_heads.source" {
				return kv.Value.AsString()
			}
		}
		t.Fatalf("no chat_snapshot.update_heads.source on the span: %v", ended[0].Attributes())
		return ""
	}

	chatID := seedChatForUpdates(t, repo, ctx)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "ap-1", `{}`))
	require.Equal(t, "heads_table", sourceOf(chatID))

	// A counter the previous release advanced: the heads are not vouched for.
	_, err := raw.ExecContext(ctx,
		`UPDATE update_stream_counters SET last_assigned_seq = last_assigned_seq + 1 WHERE stream_kind = 'chat' AND stream_id = $1`, chatID)
	require.NoError(t, err)
	require.Equal(t, "log_fallback", sourceOf(chatID))
}
