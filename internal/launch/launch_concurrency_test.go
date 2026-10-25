// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"google.golang.org/protobuf/types/known/structpb"
)

// seedLaunchUser gives userID a project with its main worktree in repo, the
// minimum a launch needs (launchFixture, for a user of the caller's choosing).
func seedLaunchUser(t *testing.T, repo *db.Repo, userID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	projectID := "launch-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Launch Project", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return projectID
}

// launchFor starts one chat for userID. It never touches t, so it is safe to
// call from a goroutine: the caller builds params on the test goroutine.
func launchFor(ctx context.Context, launcher *Launcher, params map[string]*structpb.Value, userID, projectID string) (*Result, error) {
	return launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: userID,
		ProjectID:   projectID,
		Workflow:    "builtin://agent",
		Params:      params,
		Messages:    userSeed("hello"),
	})
}

// activityEvents is chatID's chat_activity_changed events in userID's ledger.
func activityEvents(t *testing.T, repo *db.Repo, userID, chatID string) []db.UserUpdate {
	t.Helper()
	updates, err := repo.GetUserUpdatesSince(context.Background(), userID, 0, 1000)
	require.NoError(t, err)
	var out []db.UserUpdate
	for _, u := range updates {
		if u.UpdateType == db.UserUpdateChatActivityChanged && u.ChatID != nil && *u.ChatID == chatID {
			out = append(out, u)
		}
	}
	return out
}

// Concurrent StartChats for DIFFERENT users must all succeed. Each launch is
// one SERIALIZABLE transaction; it used to read the chats_with_activity view
// inside it (to announce the new chat's activity), which put predicate locks
// across workflows, threads, approvals and questions, so launches that touch
// nothing in common aborted one another with SQLSTATE 40001 until one ran
// out of retries ("internal: failed to create chat", three e2e stories in one
// release gate). The announcement now runs after the launch commits.
//
// Sized to stay inside the retry budget with margin, not to sit at its edge:
// on a fresh test database every launch still shares the few index pages of
// messages and context_windows, so some 40001 retries remain (the seed
// message's MAX()-based ordinal and seq). Measured on one pgtest server: at 16
// users the old code exhausted the budget on every run and the new code
// passed but took up to 6 of 6 retries; at 8 users the old code needed up to
// 5 and the new code never ran a transaction past a second. The deterministic
// proof that the emit left the launch's transaction is
// TestLaunchSucceedsWhenThePostCommitActivityEmitFails.
func TestConcurrentLaunchesForDistinctUsersAllSucceed(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: drives many concurrent DB-backed launches; skipped under -short")
	}
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

	const users = 8
	userIDs := make([]string, users)
	projectIDs := make([]string, users)
	for i := range userIDs {
		userIDs[i] = fmt.Sprintf("concurrent-launch-user-%d-%s", i, uuid.NewString()[:8])
		projectIDs[i] = seedLaunchUser(t, repo, userIDs[i])
	}

	params := mockModelParams(t)
	start := make(chan struct{})
	results := make([]*Result, users)
	errs := make([]error, users)
	var wg sync.WaitGroup
	for i := range userIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = launchFor(context.Background(), launcher, params, userIDs[i], projectIDs[i])
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "launch %d of %d (user %s)", i+1, users, userIDs[i])
	}
	for i, result := range results {
		require.NotNil(t, result.Chat)
		events := activityEvents(t, repo, userIDs[i], result.Chat.ID)
		assert.NotEmpty(t, events, "user %s's new chat had its activity announced after the launch committed", userIDs[i])
	}
}

// failActivityEventsSQL makes every chat_activity_changed insert fail, as a
// database refusing that one write would, and leaves every other update
// (chat_created, in the launch's own transaction) alone.
const failActivityEventsSQL = `
CREATE FUNCTION test_fail_activity_events() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.update_type = %d THEN
		RAISE EXCEPTION 'injected: chat_activity_changed refused';
	END IF;
	RETURN NEW;
END $$;
CREATE TRIGGER test_fail_activity_events BEFORE INSERT ON user_updates
	FOR EACH ROW EXECUTE FUNCTION test_fail_activity_events();
`

// The activity announcement follows the launch's commit, so it cannot undo
// it: when it fails, the chat, its thread and its chat_created announcement
// stand, the launch reports success, and the failure is the emit's alone
// (logged at ERROR). Clients still converge on the chat's activity from a
// list or get, which read it from the view.
func TestLaunchSucceedsWhenThePostCommitActivityEmitFails(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: DB-backed launch; skipped under -short")
	}
	t.Parallel()

	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})
	userID := "emit-failure-user-" + uuid.NewString()[:8]
	projectID := seedLaunchUser(t, repo, userID)
	injectActivityEventFailure(t, raw)

	result, err := launchFor(context.Background(), launcher, mockModelParams(t), userID, projectID)
	require.NoError(t, err, "a failed activity announcement must not fail the launch it follows")
	require.NotNil(t, result.Chat)

	chat, err := repo.GetChat(context.Background(), result.Chat.ID)
	require.NoError(t, err, "the chat committed before its announcement was attempted")
	assert.Equal(t, userID, chat.UserID)
	_, err = repo.GetWorkflow(context.Background(), result.WorkflowID)
	require.NoError(t, err, "and so did its root workflow")
	assert.Empty(t, activityEvents(t, repo, userID, result.Chat.ID), "the injected failure refused the activity event")

	updates, err := repo.GetUserUpdatesSince(context.Background(), userID, 0, 1000)
	require.NoError(t, err)
	var created bool
	for _, u := range updates {
		created = created || (u.UpdateType == db.UserUpdateChatCreated && u.EntityID == result.Chat.ID)
	}
	assert.True(t, created, "chat_created is part of the launch's own transaction")
}

func injectActivityEventFailure(t *testing.T, raw *sql.DB) {
	t.Helper()
	_, err := raw.ExecContext(context.Background(), fmt.Sprintf(failActivityEventsSQL, int(db.UserUpdateChatActivityChanged)))
	require.NoError(t, err)
}
