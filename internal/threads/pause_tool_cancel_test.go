package threads

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// CancelChatToolCalls is pause's tool-cancel push -- the same mechanism
// InterruptThread uses, scoped to the whole chat instead of one thread. See
// specs/interrupt-pause-spec.md, item E.

func TestCancelChatToolCalls_CancelsInFlightCallsOnAllThreads(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	rootExecuting := "toolu_" + uuid.NewString()
	rootPending := "toolu_" + uuid.NewString()
	childExecuting := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, rootExecuting, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, rootPending, fx.chatID, fx.rootThreadID, "spawn_status", core.ToolCallStatusPending)
	insertInterruptToolCall(t, repo, childExecuting, fx.chatID, fx.childThreadID, "bash", core.ToolCallStatusExecuting)

	result, err := svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{
		UserID: fx.userID, ChatID: fx.chatID,
	})
	require.NoError(t, err)

	assert.Equal(t, 3, result.CancelledToolCalls,
		"pause scope is the whole chat -- every thread's in-flight work stops")
	assert.Empty(t, result.UndeliverableToolCalls)
	assert.ElementsMatch(t, []string{rootExecuting, rootPending, childExecuting}, canceler.cancelledIDs(),
		"unlike interrupt, pause must reach the spawned sub-agent's thread too")
}

func TestCancelChatToolCalls_LeavesOtherChatsAlone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	otherFx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	pausedChatCall := "toolu_" + uuid.NewString()
	otherChatCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, pausedChatCall, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, otherChatCall, otherFx.chatID, otherFx.rootThreadID, "bash", core.ToolCallStatusExecuting)

	result, err := svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{
		UserID: fx.userID, ChatID: fx.chatID,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, result.CancelledToolCalls)
	assert.Equal(t, []string{pausedChatCall}, canceler.cancelledIDs(),
		"pausing one chat must not touch another chat's in-flight tool calls")
}

func TestCancelChatToolCalls_DaemonDeliveryFailureIsNonFatal(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	reachable := "toolu_" + uuid.NewString()
	unreachable := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, reachable, fx.chatID, fx.rootThreadID, "view", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, unreachable, fx.chatID, fx.childThreadID, "bash", core.ToolCallStatusExecuting)
	canceler.failFor[unreachable] = true

	result, err := svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{
		UserID: fx.userID, ChatID: fx.chatID,
	})
	require.NoError(t, err, "a daemon delivery failure must not fail the pause")

	assert.Equal(t, 1, result.CancelledToolCalls)
	assert.Equal(t, []string{unreachable}, result.UndeliverableToolCalls)
}

func TestCancelChatToolCalls_NothingInFlightSucceeds(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	result, err := svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{
		UserID: fx.userID, ChatID: fx.chatID,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, result.CancelledToolCalls)
	assert.Empty(t, result.UndeliverableToolCalls)
	assert.Empty(t, canceler.cancelledIDs())
}

func TestCancelChatToolCalls_RejectsWrongOwner(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "owner-user")
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}))

	_, err := svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{
		UserID: "other-user", ChatID: fx.chatID,
	})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCancelChatToolCalls_RequiresChatAndUser(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo)

	_, err := svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{UserID: fx.userID})
	require.ErrorIs(t, err, ErrInvalidArgument)

	_, err = svc.CancelChatToolCalls(ctx, CancelChatToolCallsOpts{ChatID: fx.chatID})
	require.ErrorIs(t, err, ErrInvalidArgument)
}
