// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectpurge"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
)

// Deleting a project with a restore window (control-plane
// docs/design/unified-shell-and-projects.md §11.3, D18): scheduling hides the
// project and its chats and pauses its triggers; restoring undoes exactly
// that; the purge deletes it once the window has passed, once.

// deleteClock is the fixed "now" every test here reads: the purge and the
// claims take their time as an argument, so no assertion depends on the
// wall clock.
var deleteClock = time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)

// lockRaceBudget bounds a fixture's whole test: every query, and every wait
// for a racing write's result, ends with the fixture's context.
const lockRaceBudget = 2 * time.Minute

type deleteFixture struct {
	repo                       *Repo
	purges                     projectpurge.Service
	ctx                        context.Context
	owner, project, chat       string
	enabledTrigger, userPaused string
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	suffix := uuid.NewString()[:8]
	ctx, cancel := context.WithTimeout(t.Context(), lockRaceBudget)
	t.Cleanup(cancel)
	f := &deleteFixture{
		repo: repo, purges: projectpurge.New(projectpurge.Deps{DB: repo.ORM()}), ctx: ctx,
		owner: "owner-" + suffix, project: "proj-" + suffix, chat: "chat-" + suffix,
		enabledTrigger: "trig-on-" + suffix, userPaused: "trig-off-" + suffix,
	}
	now := deleteClock
	require.NoError(t, repo.CreateProject(f.ctx, &Project{ID: f.project, UserID: f.owner, Name: "doomed", Path: "/home/me/doomed-" + suffix,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, repo.CreateChat(f.ctx, &Chat{ID: f.chat, UserID: f.owner, ProjectID: f.project, Title: "a chat",
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	for _, tr := range []struct {
		id      string
		enabled bool
	}{{f.enabledTrigger, true}, {f.userPaused, false}} {
		require.NoError(t, triggertest.Create(f.ctx, repo, &core.Trigger{
			ID: tr.id, UserID: f.owner, ProjectID: f.project, Name: tr.id, Kind: core.TriggerKindSchedule, Enabled: tr.enabled,
			Workflow: "builtin://agent", Config: []byte(`{"interval":"1h"}`), CreatedAt: now, UpdatedAt: now,
		}))
	}
	return f
}

// decided is the fixture project as an authority decision reads it.
func (f *deleteFixture) decided() core.ProjectDecision {
	return core.ProjectDecision{OwnerUserID: f.owner}
}

func (f *deleteFixture) trigger(t *testing.T, id string) *core.Trigger {
	t.Helper()
	tr, err := store.GetTrigger(f.ctx, f.repo.ORM(), id)
	require.NoError(t, err)
	return tr
}

func (f *deleteFixture) listed(t *testing.T) (projects, chats bool) {
	t.Helper()
	ps, err := f.repo.ListProjects(f.ctx, ProjectFilters{UserID: f.owner})
	require.NoError(t, err)
	cs, err := f.repo.ListChats(f.ctx, ChatFilters{UserID: f.owner, Limit: 100})
	require.NoError(t, err)
	return len(ps) == 1, len(cs) == 1
}

func TestProjectDeleteScheduleHidesAndRestoreBringsBack(t *testing.T) {
	f := newDeleteFixture(t)
	after := deleteClock.Add(core.ProjectDeleteWindow)

	got, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after, f.decided())
	require.NoError(t, err)
	assert.False(t, got.AlreadyScheduled)
	assert.True(t, got.DeleteAfter.Equal(after))
	assert.Equal(t, []string{f.enabledTrigger}, got.PausedTriggerIDs, "only the enabled trigger is paused")

	// Hidden: no list shows it or its chat, and its owner's checked read is
	// NotFound.
	projects, chats := f.listed(t)
	assert.False(t, projects, "a scheduled project leaves the project list")
	assert.False(t, chats, "its chats leave the chat list")
	_, err = f.repo.GetProjectWithUserCheck(f.ctx, f.project, f.owner)
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
	deleted, err := f.purges.ListDeletedProjects(f.ctx, f.owner, nil, false)
	require.NoError(t, err)
	require.Len(t, deleted, 1)
	assert.Equal(t, f.owner, *deleted[0].DeletedBy)

	on := f.trigger(t, f.enabledTrigger)
	assert.False(t, on.Enabled)
	assert.Equal(t, core.TriggerPausedProjectDeleted, on.PausedReason)
	off := f.trigger(t, f.userPaused)
	assert.False(t, off.Enabled)
	assert.Empty(t, off.PausedReason, "a trigger a person paused keeps its own pause")

	// A second delete keeps the first date and changes nothing.
	again, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, "someone-else", after.Add(48*time.Hour), f.decided())
	require.NoError(t, err)
	assert.True(t, again.AlreadyScheduled)
	assert.True(t, again.DeleteAfter.Equal(after))
	assert.Empty(t, again.PausedTriggerIDs)

	resumed, err := f.purges.RestoreProject(f.ctx, f.project, f.decided())
	require.NoError(t, err)
	assert.Equal(t, []string{f.enabledTrigger}, resumed)
	projects, chats = f.listed(t)
	assert.True(t, projects)
	assert.True(t, chats)
	p, err := f.repo.GetProjectWithUserCheck(f.ctx, f.project, f.owner)
	require.NoError(t, err)
	assert.Nil(t, p.DeleteAfter)
	assert.Nil(t, p.DeletedBy)
	assert.True(t, f.trigger(t, f.enabledTrigger).Enabled, "restore turns the trigger the delete paused back on")
	assert.False(t, f.trigger(t, f.userPaused).Enabled, "and leaves the one a person paused alone")

	// Restoring again is a no-op.
	resumed, err = f.purges.RestoreProject(f.ctx, f.project, f.decided())
	require.NoError(t, err)
	assert.Empty(t, resumed)
}

func TestProjectPurgeRunsAfterTheWindowOnce(t *testing.T) {
	f := newDeleteFixture(t)
	after := deleteClock.Add(core.ProjectDeleteWindow)
	_, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after, f.decided())
	require.NoError(t, err)

	// Inside the window: nothing is due.
	res, err := f.purges.PurgeDueProjects(f.ctx, deleteClock, time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged)
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.NoError(t, err, "a project inside its window is kept")

	// After it: purged, with everything it owned.
	later := after.Add(time.Minute)
	res, err = f.purges.PurgeDueProjects(f.ctx, later, time.Hour, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged)
	assert.Zero(t, res.Failed)
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.Error(t, err, "the purged project is gone")
	_, err = store.GetTrigger(f.ctx, f.repo.ORM(), f.enabledTrigger)
	assert.ErrorIs(t, err, core.ErrTriggerNotFound)
	_, err = store.GetChat(f.ctx, f.repo.ORM(), f.chat)
	assert.Error(t, err, "its chat is gone")

	// Idempotent: a second pass finds nothing, and so does a restore.
	res, err = f.purges.PurgeDueProjects(f.ctx, later, time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged)
	_, err = f.purges.RestoreProject(f.ctx, f.project, f.decided())
	assert.ErrorIs(t, err, core.ErrProjectNotFound)
}

func TestProjectRestoreIsRefusedOncePurgeBegins(t *testing.T) {
	f := newDeleteFixture(t)
	after := deleteClock.Add(-time.Minute) // already due
	_, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after, f.decided())
	require.NoError(t, err)

	// A claim another replica holds: this one skips the project, and a
	// restore is refused, so it can never come back half-purged.
	_, claimed, err := claimPurge(f, deleteClock)
	require.NoError(t, err)
	require.True(t, claimed)
	res, err := f.purges.PurgeDueProjects(f.ctx, deleteClock.Add(30*time.Minute), time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "a project another purge holds is left to it")
	_, err = f.purges.RestoreProject(f.ctx, f.project, f.decided())
	assert.ErrorIs(t, err, core.ErrProjectPurgeStarted)
	deleted, err := f.purges.ListDeletedProjects(f.ctx, f.owner, nil, false)
	require.NoError(t, err)
	assert.Empty(t, deleted, "a project being purged is not offered for restore")
	purging, err := f.purges.ListDeletedProjects(f.ctx, f.owner, nil, true)
	require.NoError(t, err)
	require.Len(t, purging, 1, "its owner can still find it, to finish the purge")
	assert.NotNil(t, purging[0].PurgeStartedAt)
	others, err := f.purges.ListDeletedProjects(f.ctx, "someone-else", nil, true)
	require.NoError(t, err)
	assert.Empty(t, others, "nobody else is shown a purge in progress")

	// Its claim expires (the replica died): the next purge finishes it.
	res, err = f.purges.PurgeDueProjects(f.ctx, deleteClock.Add(2*time.Hour), time.Hour, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged)
}

// An immediate purge of a project already scheduled for a week from now that
// stops part way is resumed by the worker once its claim's lease runs out,
// not at the end of the window it was scheduled with (it can no longer be
// restored, so waiting would only keep half-deleted data).
func TestAnInterruptedImmediatePurgeResumesAfterItsLease(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, deleteClock.Add(core.ProjectDeleteWindow), f.decided())
	require.NoError(t, err)

	// The owner's PurgeProjectNow claims it, then stops before any batch.
	claimed, err := store.ClaimProjectPurgeNow(f.ctx, f.repo.ORM(), f.project, f.decided(), deleteClock)
	require.NoError(t, err)
	require.True(t, claimed)
	p, err := f.repo.GetProject(f.ctx, f.project)
	require.NoError(t, err)
	require.NotNil(t, p.DeleteAfter)
	assert.True(t, p.DeleteAfter.Equal(deleteClock), "the claim brings delete_after forward to now, got %v", p.DeleteAfter)

	res, err := f.purges.PurgeDueProjects(f.ctx, deleteClock.Add(30*time.Minute), time.Hour, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "the interrupted purge still holds its lease")

	res, err = f.purges.PurgeDueProjects(f.ctx, deleteClock.Add(2*time.Hour), time.Hour, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "the worker resumes it once the lease runs out, well inside the original window")
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.Error(t, err, "the project is gone")
}

// purgeNow is PurgeProjectNow's two halves run back to back: the claim the
// request makes, then the batches it leaves running.
func (f *deleteFixture) purgeNow(decided core.ProjectDecision) ([]string, error) {
	if err := f.purges.ClaimProjectPurgeNow(f.ctx, f.project, decided); err != nil {
		return nil, err
	}
	return f.purges.PurgeClaimedProject(f.ctx, f.project, decided.OwnerUserID)
}

func TestPurgeProjectNowDeletesAtOnce(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.purgeNow(core.ProjectDecision{OwnerUserID: "not-the-owner"})
	assert.ErrorIs(t, err, store.ErrOrgChanged, "a decision about another owner purges nothing")
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.NoError(t, err)
	triggers, err := f.purgeNow(f.decided())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{f.enabledTrigger, f.userPaused}, triggers)
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.Error(t, err)
}

// The claim alone hides the project and refuses its restore, before any batch
// runs; a purge whose batches then fail part way is resumed by the worker
// once the claim's lease has passed.
func TestAClaimedPurgeThatFailsIsResumedByTheWorker(t *testing.T) {
	f := newDeleteFixture(t)
	require.NoError(t, f.purges.ClaimProjectPurgeNow(f.ctx, f.project, f.decided()))
	projects, chats := f.listed(t)
	assert.False(t, projects, "a claimed project leaves the project list")
	assert.False(t, chats, "its chats leave the chat list")
	_, err := f.purges.RestoreProject(f.ctx, f.project, f.decided())
	assert.ErrorIs(t, err, core.ErrProjectPurgeStarted, "nothing can restore it once claimed")

	// The chat holds a message, which the thinning takes before the chat's
	// own batch; that batch then fails (a trigger of this test's database
	// refuses the chat's delete), so the purge stops part way.
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := f.repo.ORM().Bun().NewRaw(query, args...).Exec(f.ctx)
		require.NoError(t, err, query)
	}
	exec(`INSERT INTO threads (id, chat_id, origin, created_at) VALUES (?0, ?1, 'root', ?2)`, f.chat+"-thread", f.chat, deleteClock)
	exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at) VALUES (?0, ?1, 1, ?2)`, f.chat+"-cw", f.chat+"-thread", deleteClock)
	exec(`INSERT INTO messages (id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq)
		VALUES (?0, ?1, 1, ?2, ?3, 1, ?4, ?4, 1)`, f.chat+"-msg", f.chat, f.chat+"-thread", f.chat+"-cw", deleteClock)
	exec(`CREATE FUNCTION test_refuse_chat_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test: the chat batch fails'; END $$`)
	exec(`CREATE TRIGGER test_refuse_chat_delete BEFORE DELETE ON chats FOR EACH ROW EXECUTE FUNCTION test_refuse_chat_delete()`)
	_, err = f.purges.PurgeClaimedProject(f.ctx, f.project, f.owner)
	require.ErrorContains(t, err, "the chat batch fails", "the batches stop part way")
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.NoError(t, err, "what is left stays, hidden and claimed")
	_, err = store.GetChat(f.ctx, f.repo.ORM(), f.chat)
	require.NoError(t, err, "the chat's batch rolled back")
	var messages int
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM messages WHERE chat_id = ?`, f.chat).Scan(f.ctx, &messages))
	assert.Zero(t, messages, "the thinning before it committed")
	exec(`DROP TRIGGER test_refuse_chat_delete ON chats`)

	now := time.Now()
	res, err := f.purges.PurgeDueProjects(f.ctx, now, projectpurge.Lease, 50)
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "the failed purge's claim holds until its lease passes")
	res, err = f.purges.PurgeDueProjects(f.ctx, now.Add(projectpurge.Lease+time.Minute), projectpurge.Lease, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "the worker resumes it after the lease")
	_, err = f.repo.GetProject(f.ctx, f.project)
	require.Error(t, err, "the project is gone")
	_, err = store.GetChat(f.ctx, f.repo.ORM(), f.chat)
	assert.Error(t, err, "its chat is gone")
}

// The rows that name a project with no foreign key to it go with its purge,
// and those an older delete left behind are swept.
func TestProjectPurgeCleansRowsWithNoForeignKey(t *testing.T) {
	f := newDeleteFixture(t)
	seedRecovery := func(id, projectID string) {
		t.Helper()
		_, err := f.repo.ORM().Bun().NewRaw(`INSERT INTO worktree_recoveries
			(id, owner_user_id, source_worktree_id, project_id, daemon_id, capture_id, fence, activity_generation, observation_id, manifest_digest, idempotency_key)
			VALUES (?0, ?1, ?0, ?2, 'd', ?0, 'f', 0, 'o', 'm', ?0)`, id, f.owner, projectID).Exec(f.ctx)
		require.NoError(t, err)
		_, err = f.repo.ORM().Bun().NewRaw(`INSERT INTO worktree_recovery_checkouts
			(recovery_id, repo_id, rel, remote_url, recovery_ref, commit_oid, manifest_digest, descriptor_json)
			VALUES (?0, 'r', '.', 'https://example.com/x.git', ?0, 'c', 'm', '{}')`, id).Exec(f.ctx)
		require.NoError(t, err)
	}
	count := func(table, id string) int {
		t.Helper()
		col := "id"
		if table == "worktree_recovery_checkouts" {
			col = "recovery_id"
		}
		var n int
		require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM `+table+` WHERE `+col+` = ?`, id).Scan(f.ctx, &n))
		return n
	}
	mine := "recovery-" + uuid.NewString()[:8]
	orphan := "recovery-" + uuid.NewString()[:8]
	seedRecovery(mine, f.project)
	seedRecovery(orphan, "project-deleted-long-ago-"+uuid.NewString()[:8])

	_, err := f.purgeNow(f.decided())
	require.NoError(t, err)
	assert.Zero(t, count("worktree_recoveries", mine), "the purge removes the project's recoveries")
	assert.Zero(t, count("worktree_recovery_checkouts", mine))
	assert.Equal(t, 1, count("worktree_recoveries", orphan), "the purge touches only its own project's rows")

	n, err := store.SweepOrphanedProjectRows(f.ctx, f.repo.ORM(), 500)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	assert.Zero(t, count("worktree_recoveries", orphan), "the sweep removes what an older delete left")
	assert.Zero(t, count("worktree_recovery_checkouts", orphan))
}

func claimPurge(f *deleteFixture, now time.Time) (string, bool, error) {
	ids, err := store.ListDueProjectPurges(f.ctx, f.repo.ORM(), now, time.Hour, 10)
	if err != nil || len(ids) == 0 {
		return "", false, err
	}
	return store.ClaimProjectPurge(f.ctx, f.repo.ORM(), ids[0], now, time.Hour)
}

// awaitLockWait returns once another backend of this test's database waits
// on an ungranted lock (pg_locks; a row lock's wait is on the holder's
// transaction id, which names no database, so the backend's is read): the write a test started in a goroutine has
// reached the lock the transaction it races holds. It polls the server's own
// state, each poll a round trip, under the fixture's deadline; nothing waits
// a fixed duration, so no ordering rests on timing.
func awaitLockWait(t *testing.T, f *deleteFixture) {
	t.Helper()
	for {
		var waiting int
		err := f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM pg_locks l
JOIN pg_stat_activity a ON a.pid = l.pid
WHERE NOT l.granted AND a.datname = current_database() AND l.pid <> pg_backend_pid()`).Scan(f.ctx, &waiting)
		if f.ctx.Err() != nil {
			t.Fatal("the racing write never waited on the lock")
		}
		require.NoError(t, err)
		if waiting > 0 {
			return
		}
	}
}

// receive returns the next value on ch, or fails the test once ctx ends, so
// no wait for a racing write's result outlives the test's budget.
func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatalf("gave up waiting for a result: %v", context.Cause(ctx))
		var zero T
		return zero
	}
}

// result is what a write a test runs in a goroutine returned.
type result[T any] struct {
	v   T
	err error
}

// holdTx runs write in a transaction it keeps open until release is called,
// and reports write's error, then the commit's, on done. ready closes once
// write has run. release is idempotent and also runs at cleanup, so a test
// that fails before releasing never strands the transaction (or the writes
// queued behind its locks); the cleanup waits for the transaction to end.
func holdTx(t *testing.T, f *deleteFixture, write func(ctx context.Context) error) (ready <-chan struct{}, release func(), done <-chan error) {
	t.Helper()
	readyc, releasec, donec, exited := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(releasec) }) }
	go func() {
		defer close(exited)
		donec <- f.repo.RunTx(f.ctx, orm.ReadCommitted, func(ctx context.Context) error {
			err := write(ctx)
			close(readyc)
			<-releasec
			return err
		})
	}()
	t.Cleanup(func() {
		release()
		// The fixture's context has ended by now (it derives from
		// t.Context), so the transaction's remaining calls return at once.
		timer := time.NewTimer(lockRaceBudget)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			t.Errorf("the held transaction did not end within %s of its release", lockRaceBudget)
		}
	})
	return readyc, release, donec
}

func (f *deleteFixture) newTrigger(id string, enabled bool) *core.Trigger {
	return &core.Trigger{
		ID: id, UserID: f.owner, ProjectID: f.project, Name: id, Kind: core.TriggerKindSchedule, Enabled: enabled,
		Workflow: "builtin://agent", Config: []byte(`{"interval":"1h"}`), CreatedAt: deleteClock, UpdatedAt: deleteClock,
	}
}

// Scheduling a deletion and a trigger write that would leave an automation
// on in the project serialize on the project row (store/project_schedule.go,
// migration 20261118103211): whichever commits second sees the first. So a
// deleted project never keeps an enabled trigger.
func TestTriggerWritesSerializeWithScheduling(t *testing.T) {
	testkit.Slow(t, "four real-Postgres lock-serialization scenarios")
	after := deleteClock.Add(core.ProjectDeleteWindow)

	t.Run("a create that commits first is paused by the schedule", func(t *testing.T) {
		f := newDeleteFixture(t)
		created := "trig-new-" + uuid.NewString()[:8]
		ready, release, done := holdTx(t, f, func(ctx context.Context) error {
			return store.InsertTrigger(ctx, f.repo.ORM(), f.newTrigger(created, true))
		})
		receive(t, f.ctx, ready)
		scheduled := make(chan result[core.ProjectDeletion], 1)
		go func() {
			d, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after, f.decided())
			scheduled <- result[core.ProjectDeletion]{d, err}
		}()
		awaitLockWait(t, f)
		release()
		require.NoError(t, receive(t, f.ctx, done))
		got := receive(t, f.ctx, scheduled)
		require.NoError(t, got.err)
		assert.ElementsMatch(t, []string{f.enabledTrigger, created}, got.v.PausedTriggerIDs)
		tr := f.trigger(t, created)
		assert.False(t, tr.Enabled)
		assert.Equal(t, core.TriggerPausedProjectDeleted, tr.PausedReason)
	})

	t.Run("a create that comes after the schedule is refused", func(t *testing.T) {
		f := newDeleteFixture(t)
		ready, release, done := holdTx(t, f, func(ctx context.Context) error {
			_, err := store.ScheduleProjectDeletion(ctx, f.repo.ORM(), f.project, f.owner, after, f.decided())
			return err
		})
		receive(t, f.ctx, ready)
		created := "trig-new-" + uuid.NewString()[:8]
		creating := make(chan error, 1)
		go func() { creating <- triggertest.Create(f.ctx, f.repo, f.newTrigger(created, true)) }()
		awaitLockWait(t, f)
		release()
		require.NoError(t, receive(t, f.ctx, done))
		assert.ErrorIs(t, receive(t, f.ctx, creating), core.ErrProjectScheduledForDeletion)
		_, err := store.GetTrigger(f.ctx, f.repo.ORM(), created)
		assert.ErrorIs(t, err, core.ErrTriggerNotFound)
	})

	t.Run("an enable that commits first is paused by the schedule, and restored with it", func(t *testing.T) {
		f := newDeleteFixture(t)
		ready, release, done := holdTx(t, f, func(ctx context.Context) error {
			return store.SetTriggerEnabled(ctx, f.repo.ORM(), f.userPaused, true)
		})
		receive(t, f.ctx, ready)
		scheduled := make(chan result[core.ProjectDeletion], 1)
		go func() {
			d, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, after, f.decided())
			scheduled <- result[core.ProjectDeletion]{d, err}
		}()
		awaitLockWait(t, f)
		release()
		require.NoError(t, receive(t, f.ctx, done))
		got := receive(t, f.ctx, scheduled)
		require.NoError(t, got.err)
		assert.ElementsMatch(t, []string{f.enabledTrigger, f.userPaused}, got.v.PausedTriggerIDs)
		assert.False(t, f.trigger(t, f.userPaused).Enabled)

		resumed, err := f.purges.RestoreProject(f.ctx, f.project, f.decided())
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{f.enabledTrigger, f.userPaused}, resumed, "both were on when the delete paused them")
	})

	t.Run("an enable that comes after the schedule is refused, and restore leaves it off", func(t *testing.T) {
		f := newDeleteFixture(t)
		ready, release, done := holdTx(t, f, func(ctx context.Context) error {
			_, err := store.ScheduleProjectDeletion(ctx, f.repo.ORM(), f.project, f.owner, after, f.decided())
			return err
		})
		receive(t, f.ctx, ready)
		enabling := make(chan error, 1)
		go func() { enabling <- store.EnableTrigger(f.ctx, f.repo.ORM(), f.userPaused, true) }()
		awaitLockWait(t, f)
		release()
		require.NoError(t, receive(t, f.ctx, done))
		assert.ErrorIs(t, receive(t, f.ctx, enabling), core.ErrProjectScheduledForDeletion)
		assert.False(t, f.trigger(t, f.userPaused).Enabled)

		// An edit that would turn it on is refused too; one that leaves it
		// off is not.
		edit := f.trigger(t, f.userPaused)
		edit.Enabled = true
		assert.ErrorIs(t, triggertest.Update(f.ctx, f.repo, edit), core.ErrProjectScheduledForDeletion)
		edit.Enabled = false
		edit.Name = "renamed"
		require.NoError(t, triggertest.Update(f.ctx, f.repo, edit))

		resumed, err := f.purges.RestoreProject(f.ctx, f.project, f.decided())
		require.NoError(t, err)
		assert.Equal(t, []string{f.enabledTrigger}, resumed, "restore turns on only what the delete paused")
		assert.False(t, f.trigger(t, f.userPaused).Enabled)
		require.NoError(t, store.EnableTrigger(f.ctx, f.repo.ORM(), f.userPaused, true), "after the restore it can be turned on again")
	})
}

// Nothing new is added to a deleted project, whichever writer tries: a chat,
// a workspace, a run, a chat moved into it, a trigger. A run that already
// exists stays idempotent, and everything is admitted again after a restore.
func TestADeletedProjectAdmitsNothingNew(t *testing.T) {
	f := newDeleteFixture(t)
	other := "proj-other-" + uuid.NewString()[:8]
	require.NoError(t, f.repo.CreateProject(f.ctx, &Project{ID: other, UserID: f.owner, Name: "other", Path: "/home/me/" + other,
		CreatedAt: deleteClock, UpdatedAt: deleteClock, LastActive: deleteClock}))
	elsewhere := "chat-elsewhere-" + uuid.NewString()[:8]
	require.NoError(t, f.repo.CreateChat(f.ctx, &Chat{ID: elsewhere, UserID: f.owner, ProjectID: other, Title: "elsewhere",
		CreatedAt: deleteClock, UpdatedAt: deleteClock, LastActive: deleteClock}))
	existingRun := &Workflow{ID: "run-" + uuid.NewString()[:8], ChatID: f.chat, WorkflowName: "agent", Thread: "main", CreatedAt: deleteClock}
	require.NoError(t, f.repo.CreateWorkflow(f.ctx, existingRun))

	_, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, f.owner, deleteClock.Add(core.ProjectDeleteWindow), f.decided())
	require.NoError(t, err)

	writes := map[string]func() error{
		"a new chat": func() error {
			return f.repo.CreateChat(f.ctx, &Chat{ID: "chat-new-" + uuid.NewString()[:8], UserID: f.owner, ProjectID: f.project, Title: "new",
				CreatedAt: deleteClock, UpdatedAt: deleteClock, LastActive: deleteClock})
		},
		"a new workspace": func() error {
			return f.repo.CreateWorktree(f.ctx, &Worktree{ID: "wt-" + uuid.NewString()[:8], Name: "wt-" + uuid.NewString()[:8], Path: "/w",
				Branch: "b", ProjectID: f.project, CreatedAt: deleteClock, UpdatedAt: deleteClock, LastActive: deleteClock})
		},
		"a new run": func() error {
			return f.repo.CreateWorkflow(f.ctx, &Workflow{ID: "run-" + uuid.NewString()[:8], ChatID: f.chat, WorkflowName: "agent", Thread: "t2", CreatedAt: deleteClock})
		},
		"a chat moved into it": func() error {
			_, err := f.repo.MoveChatToProject(f.ctx, ChatMove{UserID: f.owner, ChatID: elsewhere, FromProjectID: other, ToProjectID: f.project})
			return err
		},
		"a new trigger": func() error {
			return triggertest.Create(f.ctx, f.repo, f.newTrigger("trig-new-"+uuid.NewString()[:8], false))
		},
	}
	for name, write := range writes {
		assert.ErrorIs(t, write(), core.ErrProjectScheduledForDeletion, "%s is refused", name)
	}
	require.NoError(t, f.repo.CreateWorkflow(f.ctx, existingRun), "a retried insert of a run that exists stays a no-op")

	_, err = f.purges.RestoreProject(f.ctx, f.project, f.decided())
	require.NoError(t, err)
	for name, write := range writes {
		assert.NoError(t, write(), "%s is admitted after the restore", name)
	}
}

// A delete, restore or immediate purge was authorized against the project as
// it was (authz.ProjectDelete: its owner and org). A project move that commits
// between that decision and the write changes the org the authority was
// decided in, so the write refuses (store.ErrOrgChanged) and changes nothing:
// an admin of the org the project left must not delete or restore it in the
// org it moved to. The move takes the owner's org lock and then the project
// row, as these writes do, so the two serialize.
func TestDeleteRestoreAndPurgeRefuseAProjectThatMovedOrg(t *testing.T) {
	const src, dst = "org-src", "org-dst"
	newMovable := func(t *testing.T) (*deleteFixture, core.ProjectDecision) {
		f := newDeleteFixture(t)
		_, err := f.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = ?0 WHERE id = ?1`, src, f.project).Exec(f.ctx)
		require.NoError(t, err)
		org := src
		return f, core.ProjectDecision{OwnerUserID: f.owner, OrgID: &org}
	}
	// moving holds a move's lock-and-restamp of the project open (the owner's
	// org lock, then the project row, as MoveProjectOrg takes them) until
	// release; the decided write is then started against it.
	moving := func(t *testing.T, f *deleteFixture) (<-chan struct{}, func(), <-chan error) {
		return holdTx(t, f, func(ctx context.Context) error {
			if err := store.LockUserOrgs(ctx, f.repo.ORM(), f.owner); err != nil {
				return err
			}
			_, err := f.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = ?0 WHERE id = ?1`, dst, f.project).Exec(ctx)
			return err
		})
	}
	race := func(t *testing.T, f *deleteFixture, write func() error) error {
		ready, release, done := moving(t, f)
		receive(t, f.ctx, ready)
		got := make(chan error, 1)
		go func() { got <- write() }()
		awaitLockWait(t, f)
		release()
		require.NoError(t, receive(t, f.ctx, done), "the move commits")
		return receive(t, f.ctx, got)
	}

	t.Run("a delete decided in the source org is refused once the move commits", func(t *testing.T) {
		f, decided := newMovable(t)
		err := race(t, f, func() error {
			_, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, "admin-of-src", deleteClock.Add(core.ProjectDeleteWindow), decided)
			return err
		})
		assert.ErrorIs(t, err, store.ErrOrgChanged)
		p, err := f.repo.GetProject(f.ctx, f.project)
		require.NoError(t, err)
		assert.Nil(t, p.DeleteAfter, "nothing was scheduled")
		assert.True(t, f.trigger(t, f.enabledTrigger).Enabled, "no trigger was paused")
	})

	t.Run("a restore decided in the source org is refused once the move commits", func(t *testing.T) {
		f, decided := newMovable(t)
		_, err := f.purges.ScheduleProjectDeletion(f.ctx, f.project, "admin-of-src", deleteClock.Add(core.ProjectDeleteWindow), decided)
		require.NoError(t, err)
		err = race(t, f, func() error {
			_, err := f.purges.RestoreProject(f.ctx, f.project, decided)
			return err
		})
		assert.ErrorIs(t, err, store.ErrOrgChanged)
		p, err := f.repo.GetProject(f.ctx, f.project)
		require.NoError(t, err)
		assert.NotNil(t, p.DeleteAfter, "still scheduled")
		assert.False(t, f.trigger(t, f.enabledTrigger).Enabled, "nothing was resumed")
	})

	t.Run("an immediate purge decided in the source org is refused", func(t *testing.T) {
		f, decided := newMovable(t)
		err := race(t, f, func() error {
			return f.purges.ClaimProjectPurgeNow(f.ctx, f.project, decided)
		})
		assert.ErrorIs(t, err, store.ErrOrgChanged)
		p, err := f.repo.GetProject(f.ctx, f.project)
		require.NoError(t, err, "nothing was purged")
		assert.Nil(t, p.PurgeStartedAt)
	})
}
