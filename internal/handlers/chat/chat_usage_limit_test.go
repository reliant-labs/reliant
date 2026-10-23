package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// usageLimitTemporalClient is a live run that records the signals it is sent
// and the terminates it receives.
type usageLimitTemporalClient struct {
	terminateTestTemporalClient

	signals  []string
	payloads []interface{}
}

func (c *usageLimitTemporalClient) SignalWorkflow(_ context.Context, workflowID, _, name string, arg interface{}) error {
	c.signals = append(c.signals, workflowID+":"+name)
	c.payloads = append(c.payloads, arg)
	return nil
}

var _ client.Client = (*usageLimitTemporalClient)(nil)

// seedWaitingRun creates a chat whose live root run is parked until a usage
// limit resets: the shape the workflow's marker write leaves.
func seedWaitingRun(t *testing.T, repo *db.Repo, ctx context.Context) (chatID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	projectID := "test-project-limit-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Usage Limit", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID = uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "limited", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &chatID, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID,
		Status: db.Active(), CreatedAt: now,
	}))
	require.NoError(t, repo.RecordUsageLimitWait(ctx, chatID, chatID, "Anthropic", false, now.Add(time.Hour), now))
	return chatID
}

// "Retry now" sends the chat's live run a capacity wake — whichever of its
// threads is waiting, the root's or a sub-agent's, which the chat's activity
// does not show — and a chat whose run has ended is left alone.
func TestRetryUsageLimitWait_SignalsOnlyALiveRun(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	chatID := seedWaitingRun(t, repo, ctx)
	temporal := &usageLimitTemporalClient{}
	svc := &Service{deps: Deps{Repo: repo, Temporal: temporal}}

	resp, err := svc.RetryUsageLimitWait(ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Woken)
	assert.Equal(t, []string{chatID + ":" + v2.CapacityWakeSignalName}, temporal.signals)

	// Only a sub-agent waits now: the chat no longer reads as waiting, and the
	// run is still woken — the agent's wait is what the click is for.
	require.NoError(t, repo.ClearUsageLimitWait(ctx, chatID, chatID, time.Now()))
	require.NoError(t, repo.RecordUsageLimitWait(ctx, chatID, chatID+"-agent", "Anthropic", false, time.Now().Add(time.Hour), time.Now()))
	resp, err = svc.RetryUsageLimitWait(ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Woken, "a sub-agent's wait is woken from the chat")
	assert.Len(t, temporal.signals, 2)

	// The run is over: a late click is a no-op.
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, chatID, db.Completed()))
	resp, err = svc.RetryUsageLimitWait(ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Woken, "a run that has ended is left alone")
	assert.Len(t, temporal.signals, 2)

	// Someone else's chat is not found, not woken.
	other := context.WithValue(context.Background(), auth.UserIDContextKey, "someone-else")
	_, err = svc.RetryUsageLimitWait(other, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}

// Stop on a waiting run: TerminateWorkflow runs no workflow code, so the stop
// handler itself must release the marker, or the chat keeps reading "waiting"
// for a run that is gone.
func TestTerminateChat_ReleasesTheUsageLimitWait(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	chatID := seedWaitingRun(t, repo, ctx)
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	require.EqualValues(t, reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_USAGE_LIMIT, *chat.Activity)

	temporal := &usageLimitTemporalClient{}
	svc := &Service{
		deps: Deps{Repo: repo, Temporal: temporal},
		runs: runs.NewService(repo, temporal, nil),
	}
	_, err = svc.TerminateChat(ctx, connect.NewRequest(&reliantv1.TerminateChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	require.Equal(t, []string{chatID}, temporal.terminated)

	backoffs, err := repo.ProviderBackoffByChat(ctx, chatID)
	require.NoError(t, err)
	assert.False(t, backoffs[chatID].Waiting(), "the marker is released when the run is stopped")
	chat, err = repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.NotEqual(t, int(reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_USAGE_LIMIT), *chat.Activity)
	assert.Nil(t, chat.UsageLimitResumesAt)
}

// "Use <model> now" answers the wait with a fallback model. The model must be
// one the person can actually use, or the next turn would only fail; and a
// plain click sends no model at all.
func TestRetryUsageLimitWait_UseModel(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedWaitingRun(t, repo, ctx)

	temporal := &usageLimitPayloadClient{}
	svc := &Service{deps: Deps{Repo: repo, Temporal: temporal}}

	t.Run("a plain retry carries no model", func(t *testing.T) {
		_, err := svc.RetryUsageLimitWait(ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID}))
		require.NoError(t, err)
		require.Len(t, temporal.payloads, 1)
		assert.Equal(t, v2.CapacityWakeSignal{}, temporal.payloads[0])
	})

	t.Run("a model that names no provider is refused", func(t *testing.T) {
		bad := "gpt-5.6-sol"
		_, err := svc.RetryUsageLimitWait(ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID, UseModel: &bad}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Len(t, temporal.payloads, 1, "nothing was signalled")
	})

	t.Run("a model that does not exist is refused", func(t *testing.T) {
		bad := "no-such-model@codex"
		_, err := svc.RetryUsageLimitWait(ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: chatID, UseModel: &bad}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})

}

type usageLimitPayloadClient struct {
	terminateTestTemporalClient
	payloads []interface{}
}

func (c *usageLimitPayloadClient) SignalWorkflow(_ context.Context, _, _, _ string, arg interface{}) error {
	c.payloads = append(c.payloads, arg)
	return nil
}

// Turning cross-provider fallback on while the run is parked on a limit wakes
// it, so it re-decides under the new policy instead of sitting until the reset.
// Anything that cannot change the answer (choosing to wait, a chat that is not
// waiting) leaves the wait alone.
func TestUpdateChatProviderPolicy_WakesAWaitingRun(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())
	now := time.Now().UTC()
	require.NoError(t, repo.RecordUsageLimitWait(ctx, fx.chatID, fx.rootThreadID, "Anthropic", false, now.Add(time.Hour), now))

	retries := func() int {
		n := 0
		for _, sig := range temporal.signals {
			if sig.Name == v2.CapacityWakeSignalName {
				n++
			}
		}
		return n
	}

	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_WAIT, "")
	assert.Zero(t, retries(), "choosing to wait changes nothing, so the wait is left alone")

	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, codexOnlyModel)
	assert.Equal(t, 1, retries(), "opting in wakes the parked run to re-decide")

	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Completed()))
	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_WAIT, "")
	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, codexOnlyModel)
	assert.Equal(t, 1, retries(), "a run that has ended is not woken")
}
