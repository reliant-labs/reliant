package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// seedChatRun creates a chat of userID whose root run has status, and returns
// the chat's (and root workflow's) id.
func seedChatRun(t *testing.T, repo *db.Repo, ctx context.Context, userID string, status db.WorkflowStatus) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	projectID := "test-project-wake-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Capacity Wake", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, Title: "blocked", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &chatID, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID,
		Status: status, CreatedAt: now,
	}))
	return chatID
}

// Adding (or re-authing, or un-pausing) an account wakes every chat of the
// user that a provider may have blocked — a sub-agent parked on a usage limit
// (which the chat's activity does not show), a paused root, a sub-agent run
// that stopped on a failure — and no other: not a chat with nothing blocked,
// not a run that has ended, not another user's chat.
func TestWakeUserChats_FansOutToTheUsersBlockedChats(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	const user = "test-user-wake"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
	now := time.Now().UTC()

	agentWaiting := seedChatRun(t, repo, ctx, user, db.Active())
	require.NoError(t, repo.RecordUsageLimitWait(ctx, agentWaiting, agentWaiting+"-agent", "Anthropic", false, now.Add(time.Hour), now))

	rootPaused := seedChatRun(t, repo, ctx, user, db.Paused())

	agentStopped := seedChatRun(t, repo, ctx, user, db.Active())
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: agentStopped + "-agent", ParentID: &agentStopped, ChatID: agentStopped, WorkflowName: "builtin://agent",
		Thread: agentStopped + "-agent", Status: db.Failed(), CreatedAt: now,
	}))

	_ = seedChatRun(t, repo, ctx, user, db.Active()) // nothing blocked

	ended := seedChatRun(t, repo, ctx, user, db.Completed())
	require.NoError(t, repo.RecordUsageLimitWait(ctx, ended, ended, "Anthropic", false, now.Add(time.Hour), now))

	other := "test-user-someone-else"
	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, other)
	othersChat := seedChatRun(t, repo, otherCtx, other, db.Active())
	require.NoError(t, repo.RecordUsageLimitWait(otherCtx, othersChat, othersChat, "Anthropic", false, now.Add(time.Hour), now))

	temporal := &usageLimitTemporalClient{}
	svc := &Service{deps: Deps{Repo: repo, Temporal: temporal}}

	woken := svc.WakeUserChats(ctx, user, "an account was added")
	assert.Equal(t, 3, woken)
	assert.ElementsMatch(t, []string{
		agentWaiting + ":" + v2.CapacityWakeSignalName,
		rootPaused + ":" + v2.CapacityWakeSignalName,
		agentStopped + ":" + v2.CapacityWakeSignalName,
	}, temporal.signals)
}

// Resume on a run that is executing — its threads parked on a usage limit or
// stopped on a provider failure, the root itself not paused — is the capacity
// wake alone. signal.resume would find no pause to release, be held, and
// release the next pause the run takes on its own.
func TestResumeChat_LiveRunIsWokenNotResumed(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedWaitingRun(t, repo, ctx)

	temporal := &usageLimitTemporalClient{}
	svc := &Service{deps: Deps{Repo: repo, Temporal: temporal}, runs: runs.NewService(repo, temporal, nil)}

	resp, err := svc.ResumeChat(ctx, connect.NewRequest(&reliantv1.ResumeChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.Equal(t, []string{chatID + ":" + v2.CapacityWakeSignalName}, temporal.signals,
		"only the capacity wake: every blocked thread goes on, and no resume is left held")
	assert.Equal(t, v2.CapacityWakeSignal{AllStopped: true}, temporal.payloads[0],
		"Resume brings back every stopped sub-agent, not only those a provider stopped")
}

// Resume on a paused run resumes it, and then wakes it: the root's pause is
// released by the resume, and every sub-agent a provider stopped goes on with
// it — no message to each.
func TestResumeChat_PausedRunResumesThenWakesEveryThread(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedChatRun(t, repo, ctx, "test-user", db.Paused())

	temporal := &usageLimitTemporalClient{}
	svc := &Service{deps: Deps{Repo: repo, Temporal: temporal}, runs: runs.NewService(repo, temporal, succeedingPauseController{})}

	resp, err := svc.ResumeChat(ctx, connect.NewRequest(&reliantv1.ResumeChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.Equal(t, []string{chatID + ":" + v2.CapacityWakeSignalName}, temporal.signals,
		"after the resume (the pause controller's), the whole chat is woken")
	assert.Equal(t, v2.CapacityWakeSignal{AllStopped: true}, temporal.payloads[0])
}
