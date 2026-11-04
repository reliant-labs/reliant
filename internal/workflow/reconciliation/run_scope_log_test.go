// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// captureScopedLogs is captureLogs behind the process's scope handler, so a
// record carries what production would stamp on it.
func captureScopedLogs(t *testing.T) func() []recordedLog {
	t.Helper()
	var mu sync.Mutex
	var records []recordedLog
	previous := slog.Default()
	slog.SetDefault(slog.New(logging.NewScopeHandler(recordingHandler{mu: &mu, records: &records})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() []recordedLog {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedLog(nil), records...)
	}
}

// The reconciler's records were most of the error volume with no user: a sweep
// runs on behalf of a chat, and nothing in it said whose. A record logged while
// a workflow is reconciled carries the chat's owner and the run's ids without
// the call site naming them.
func TestReconciler_RecordsCarryTheChatOwnerAndRunIDs(t *testing.T) {
	logs := captureScopedLogs(t)
	repo := newMockRepo()
	repo.chats["chat-1"] = &db.Chat{ID: "chat-1", UserID: "user-77"}

	// Temporal has no record of the workflow: the repair logs at ERROR.
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}}, DefaultConfig())
	wf := runningWorkflow()
	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)

	var errs []recordedLog
	for _, r := range logs() {
		if r.level >= slog.LevelError {
			errs = append(errs, r)
		}
	}
	require.NotEmpty(t, errs, "the lost-workflow repair must log an error")
	for _, r := range errs {
		assert.Equal(t, "user-77", r.attrs["user.id"], r.message)
		assert.Equal(t, "chat-1", r.attrs["reliant.chat.id"], r.message)
		assert.Equal(t, "main", r.attrs["thread.id"], r.message)
		assert.Equal(t, "wf-1", r.attrs["workflow.id"], r.message)
	}
}

// A chat the store cannot read is not a reason to stop reconciling it: the
// records name the chat, without a user.
func TestReconciler_UnknownChatOwnerStillStampsTheChat(t *testing.T) {
	logs := captureScopedLogs(t)
	repo := newMockRepo() // no chats: GetChat answers not-found
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}}, DefaultConfig())
	result := reconciler.ReconcileWorkflow(context.Background(), runningWorkflow())
	require.NoError(t, result.Error)

	var seen bool
	for _, r := range logs() {
		if r.level >= slog.LevelError {
			seen = true
			assert.Equal(t, "chat-1", r.attrs["reliant.chat.id"])
			assert.NotContains(t, r.attrs, "user.id")
		}
	}
	require.True(t, seen)
}
