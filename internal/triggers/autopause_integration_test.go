// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/store"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/projectaccess"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
	"github.com/reliant-labs/reliant/internal/threads"
)

// These drive the auto-pause through the REAL launcher and store: the
// launcher's project check is where a removed creator was refused on every
// fire, and the store is where the pause and its history are written.

// newOrgFireFixture is newFireFixture with the project in projectOrg and the
// trigger in triggerOrg (nil: none, a trigger written before orgs), and an
// AutoPause wired.
func newOrgFireFixture(t *testing.T, projectOrg, triggerOrg *string) *fireFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	userID := "autopause-user-" + uuid.NewString()
	projectID := "autopause-project-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, OrgID: projectOrg, Name: "Org Project", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: t.TempDir(), Branch: "main", BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.UserID, tr.ProjectID, tr.DaemonID, tr.OrgID = userID, projectID, daemonID, triggerOrg
		tr.Presets = nil
		tr.Params = map[string]any{"model": map[string]any{"id": "mock"}}
	})
	require.NoError(t, triggertest.Create(ctx, repo, trigger))

	starter := &flakyStarter{}
	launcher := launch.New(launch.Deps{Store: repo, Chats: store.NewChatsWithActivityExtStore(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Projects: projectaccess.NewFromORM(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Temporal: starter, Runs: noopRunRecorder{}, TaskQueue: "test-queue"})
	firer := NewFirer(NewStores(repo), launcher).WithAutoPause(NewAutoPause(store.NewTriggerWritesExtStore(repo.ORM()), nil))
	return &fireFixture{repo: repo, starter: starter, firer: firer, trigger: trigger}
}

func (f *fireFixture) stored(t *testing.T) *core.Trigger {
	t.Helper()
	stored, err := store.GetTrigger(context.Background(), f.repo.ORM(), f.trigger.ID)
	require.NoError(t, err)
	return stored
}

func (f *fireFixture) events(t *testing.T) []*core.TriggerEvent {
	t.Helper()
	rows, _, err := store.ListTriggerEventsPage(context.Background(), f.repo.ORM(), core.TriggerEventFilters{UserID: f.trigger.UserID, TriggerID: f.trigger.ID, Limit: 50})
	require.NoError(t, err)
	events := make([]*core.TriggerEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, row.Event)
	}
	return events
}

func (f *fireFixture) changes(t *testing.T, op string) int {
	t.Helper()
	changes, err := store.TriggerChanges(context.Background(), f.repo.ORM(), f.trigger.UserID, f.trigger.ID)
	require.NoError(t, err)
	n := 0
	for _, c := range changes {
		if c.Operation == op {
			n++
		}
	}
	return n
}

// A removed member's trigger pauses on its first refused fire: one failed
// event, the pause on the row and in its history, and nothing launched. It
// does not fire again until the creator rejoins and resumes it, and then it
// launches.
func TestAutoPause_RemovedCreatorsTriggerPausesAndResumesAfterRejoining(t *testing.T) {
	acme := "acme"
	for name, triggerOrg := range map[string]*string{
		// The fire's own check: the trigger is acme's.
		"an org's trigger": &acme,
		// The launcher's check: a trigger written before orgs, in a
		// project that is acme's.
		"a trigger with no org of its own": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newOrgFireFixture(t, &acme, triggerOrg)
			membership := memberOf("home")
			ctx := authz.WithMembership(context.Background(), membership)

			_, err := f.firer.Fire(ctx, f.fireAt(2))
			requireNonRetryable(t, err)

			stored := f.stored(t)
			assert.False(t, stored.Enabled)
			assert.Equal(t, core.TriggerPausedCreatorLeftOrg, stored.PausedReason)
			events := f.events(t)
			require.Len(t, events, 1)
			assert.Equal(t, core.TriggerEventFailed, events[0].Outcome)
			assert.Equal(t, creatorLeftOrgDetail, events[0].OutcomeDetail)
			assert.Equal(t, 1, f.changes(t, "pause"), "the pause is in the trigger's history")

			// The next scheduled fire launches nothing and pauses nothing.
			out, err := f.firer.Fire(ctx, f.fireAt(3))
			require.NoError(t, err)
			assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
			assert.Equal(t, 1, countOutcome(f.events(t), core.TriggerEventFailed), "exactly one failure")
			assert.Equal(t, 1, f.changes(t, "pause"))
			assert.Zero(t, f.starter.startCount())

			// Rejoined, the creator resumes it: the reason is cleared and it
			// launches.
			membership.set(false, "acme", "home")
			require.NoError(t, store.EnableTrigger(ctx, f.repo.ORM(), f.trigger.ID, true))
			stored = f.stored(t)
			assert.True(t, stored.Enabled)
			assert.Empty(t, stored.PausedReason)
			out, err = f.firer.Fire(ctx, f.fireAt(4))
			require.NoError(t, err)
			assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
			assert.Equal(t, 1, f.starter.startCount())
		})
	}
}

// A membership outage fails the fire closed and retryably, whichever check
// meets it, and never pauses: the trigger stays enabled, and nothing is
// recorded for a fire that may yet launch.
func TestAutoPause_AMembershipOutageDoesNotPause(t *testing.T) {
	acme := "acme"
	for name, triggerOrg := range map[string]*string{
		"an org's trigger":                 &acme,
		"a trigger with no org of its own": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newOrgFireFixture(t, &acme, triggerOrg)
			membership := memberOf("acme")
			membership.set(true)
			ctx := authz.WithMembership(context.Background(), membership)

			_, err := f.firer.Fire(ctx, f.fireAt(2))
			requireRetryable(t, err)
			stored := f.stored(t)
			assert.True(t, stored.Enabled)
			assert.Empty(t, stored.PausedReason)
			assert.Empty(t, f.events(t))
			assert.Zero(t, f.changes(t, "pause"))

			// The retry, once control-plane answers, launches.
			membership.set(false, "acme")
			out, err := f.firer.Fire(ctx, f.fireAt(2))
			require.NoError(t, err)
			assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
		})
	}
}
