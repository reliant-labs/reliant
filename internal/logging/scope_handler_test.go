// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

func scopedRecord(t *testing.T, ctx context.Context, args ...any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	slog.New(newScopeHandler(slog.NewJSONHandler(&buf, nil))).ErrorContext(ctx, "boom", args...)
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	return rec
}

func TestScopeHandlerStampsUserIDOfTheRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newScopeHandler(slog.NewJSONHandler(&buf, nil)))

	scoped := telemetry.WithRequestScope(context.Background())
	telemetry.BindUser(scoped, "user-9")
	logger.ErrorContext(scoped, "rpc failed")
	assert.Contains(t, buf.String(), `"user.id":"user-9"`)

	buf.Reset()
	logger.ErrorContext(context.Background(), "background")
	assert.NotContains(t, buf.String(), "user.id")

	buf.Reset()
	logger.ErrorContext(scoped, "explicit", "user.id", "other")
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("user.id")))
}

// A worker or reconciler record has no request: the run it happened in is what
// names its user and chat.
func TestScopeHandlerStampsTheRunOfAnActivity(t *testing.T) {
	ctx := telemetry.WithRunScope(context.Background(), telemetry.RunScope{
		UserID: "user-3", ChatID: "chat-1", ThreadID: "thread-2", WorkflowID: "wf-4",
	})
	rec := scopedRecord(t, ctx)
	assert.Equal(t, "user-3", rec["user.id"])
	assert.Equal(t, "chat-1", rec["reliant.chat.id"])
	assert.Equal(t, "thread-2", rec["thread.id"])
	assert.Equal(t, "wf-4", rec["workflow.id"])
}

func TestScopeHandlerStampsOnlyWhatIsKnown(t *testing.T) {
	rec := scopedRecord(t, telemetry.WithRunScope(context.Background(), telemetry.RunScope{ChatID: "chat-1"}))
	assert.Equal(t, "chat-1", rec["reliant.chat.id"])
	for _, k := range []string{"user.id", "thread.id", "workflow.id"} {
		assert.NotContains(t, rec, k, "an unknown id is absent, not empty")
	}
}

func TestScopeHandlerKeepsWhatTheCallSiteWrote(t *testing.T) {
	ctx := telemetry.WithRunScope(context.Background(), telemetry.RunScope{UserID: "user-3", ChatID: "chat-1"})
	rec := scopedRecord(t, ctx, "reliant.chat.id", "chat-explicit", "user.id", "user-explicit")
	assert.Equal(t, "chat-explicit", rec["reliant.chat.id"])
	assert.Equal(t, "user-explicit", rec["user.id"])
}

func TestScopeHandlerRequestUserWinsOverTheRunUser(t *testing.T) {
	ctx := telemetry.WithRequestScope(context.Background())
	telemetry.BindUser(ctx, "user-request")
	ctx = telemetry.WithRunScope(ctx, telemetry.RunScope{UserID: "user-run", ChatID: "chat-1"})
	rec := scopedRecord(t, ctx)
	assert.Equal(t, "user-request", rec["user.id"])
	assert.Equal(t, "chat-1", rec["reliant.chat.id"])
}

func TestWithRunScopeKeepsWhatAnOuterLayerBound(t *testing.T) {
	ctx := telemetry.WithRunScope(context.Background(), telemetry.RunScope{ChatID: "chat-1", UserID: "u"})
	ctx = telemetry.WithRunScope(ctx, telemetry.RunScope{ThreadID: "t-1"})
	got := telemetry.RunScopeFromContext(ctx)
	assert.Equal(t, telemetry.RunScope{UserID: "u", ChatID: "chat-1", ThreadID: "t-1"}, got)
}

// Every entry point installs the scope handler, not only the one a test drove:
// the worker and the API server are different Setup calls.
func TestEverySetupStampsTheRunScope(t *testing.T) {
	ctx := telemetry.WithRunScope(context.Background(), telemetry.RunScope{UserID: "user-3", ChatID: "chat-1"})
	for _, entry := range setupEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			isolateLogging(t)
			read := entry.setup(t, Settings{Format: "json"})
			ErrorContext(ctx, "run failed")
			recs := jsonLines(t, read())
			require.Len(t, recs, 1)
			assert.Equal(t, "user-3", recs[0]["user.id"])
			assert.Equal(t, "chat-1", recs[0]["reliant.chat.id"])
		})
	}
}
