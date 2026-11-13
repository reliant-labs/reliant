// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Moving a project to another org (control-plane
// docs/design/org-project-move.md §1.2, §2, §3.2): the rows that move are
// restamped, the rows that stay are untouched, and the move refuses running
// work and serializes with every org-sensitive write of the owner.

const (
	moveOwner = "user-move-owner"
	moveSrc   = "org-src"
	moveDst   = "org-dst"
)

type moveFixture struct {
	t    *testing.T
	repo *Repo
	raw  *sql.DB
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	repo, raw, cleanup := setupTestRepo(t)
	t.Cleanup(cleanup)
	return &moveFixture{t: t, repo: repo, raw: raw}
}

func (f *moveFixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.raw.Exec(query, args...)
	require.NoError(f.t, err, query)
}

// project creates a project of the owner in org (nil: none yet).
func (f *moveFixture) project(id string, org *string) {
	f.t.Helper()
	now := time.Now().UTC()
	require.NoError(f.t, f.repo.CreateProject(context.Background(), &Project{
		ID: id, Name: id, Path: "/tmp/" + id, UserID: moveOwner, OrgID: org,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
}

// chat creates an idle chat of the project, with its root thread, in org.
func (f *moveFixture) chat(id, projectID string, org *string) {
	f.t.Helper()
	now := time.Now().UTC()
	require.NoError(f.t, f.repo.CreateChat(context.Background(), &Chat{
		ID: id, Title: id, ProjectID: projectID, UserID: moveOwner, OrgID: org,
		State: ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	createTestRootThread(f.t, f.repo, id)
}

// run inserts a run of the chat; it takes the chat's org on insert.
func (f *moveFixture) run(id, chatID string, status WorkflowStatus) {
	f.t.Helper()
	insertTestWorkflow(f.t, f.repo, id, chatID, "builtin://agent", status)
}

func (f *moveFixture) share(kind core.ShareKind, id string) {
	f.t.Helper()
	changed, err := f.repo.ApplyShareChange(context.Background(), core.ShareChange{
		Kind: kind, ResourceID: id, OrgID: moveSrc, OwnerUserID: moveOwner, ActorUserID: moveOwner, Action: core.ShareActionShare,
	})
	require.NoError(f.t, err)
	require.True(f.t, changed)
}

func (f *moveFixture) trigger(id, projectID string, enabled bool, connectionID *string) {
	f.t.Helper()
	tr := newTestTrigger(id, moveOwner, projectID, id)
	tr.Enabled = enabled
	tr.ConnectionID = connectionID
	tr.OrgID = strp(moveSrc)
	require.NoError(f.t, f.repo.CreateTrigger(context.Background(), tr))
}

// prepared records a move of projectID from src to dst and advances it to
// prepared, as control-plane's Prepare step would.
func (f *moveFixture) prepared(projectID string) *core.ProjectOrgMove {
	f.t.Helper()
	ctx := context.Background()
	m, created, err := f.repo.InsertProjectOrgMove(ctx, &core.ProjectOrgMove{
		ID: uuid.NewString(), ProjectID: projectID, OwnerUserID: moveOwner, ActorUserID: moveOwner,
		FromOrgID: moveSrc, ToOrgID: moveDst, RequestKey: uuid.NewString(),
	})
	require.NoError(f.t, err)
	require.True(f.t, created)
	m, err = f.repo.TransitionProjectOrgMove(ctx, m.ID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePreparing}, core.ProjectOrgMoveStatePrepared, "", nil)
	require.NoError(f.t, err)
	return m
}

func (f *moveFixture) org(table, id string) *string {
	f.t.Helper()
	var org sql.NullString
	require.NoError(f.t, f.raw.QueryRow(`SELECT org_id FROM `+table+` WHERE id = $1`, id).Scan(&org))
	if !org.Valid {
		return nil
	}
	return &org.String
}

func (f *moveFixture) visibility(table, id string) *string {
	f.t.Helper()
	var v sql.NullString
	require.NoError(f.t, f.raw.QueryRow(`SELECT visibility FROM `+table+` WHERE id = $1`, id).Scan(&v))
	if !v.Valid {
		return nil
	}
	return &v.String
}

func (f *moveFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.raw.QueryRow(query, args...).Scan(&n))
	return n
}

func strp(s string) *string { return &s }

// Every row §1.2 marks "moves" is restamped with the target org, shares are
// cleared with a share_events row each, triggers are paused and their org
// bindings cleared; every row it marks "stays", and every row of the owner's
// other project, is untouched.
func TestMoveProjectOrg_RestampsWhatMovesAndLeavesWhatStays(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	src := strp(moveSrc)

	// The project, shared, with three chats: one shared and idle with a
	// finished run, one archived with a failed run and a finished sub-run,
	// one that stopped on an error.
	f.project("p-move", src)
	f.chat("c-shared", "p-move", src)
	f.chat("c-archived", "p-move", src)
	f.chat("c-error", "p-move", src)
	f.exec(`UPDATE chats SET state = $1 WHERE id = 'c-archived'`, ChatStateArchived)
	f.run("r-shared", "c-shared", core.Completed())
	f.run("r-archived", "c-archived", core.Failed())
	f.run("r-archived-sub", "c-archived", core.Completed())
	f.run("r-error", "c-error", core.Failed())
	f.share(core.ShareKindProject, "p-move")
	f.share(core.ShareKindChat, "c-shared")

	// The owner's other project in the same org, with a shared chat, a run
	// and an enabled trigger: none of it moves.
	f.project("p-stay", src)
	f.chat("c-stay", "p-stay", src)
	f.run("r-stay", "c-stay", core.Completed())
	f.share(core.ShareKindChat, "c-stay")
	f.trigger("t-stay", "p-stay", true, nil)

	// Triggers: enabled on the source org's bot, enabled on the owner's own
	// connection, paused by a person, paused by the system.
	f.exec(`INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status)
		VALUES ('conn-org', 'org', $1, $2, 'slack', 'api_key', 'Source bot', 'active'),
		       ('conn-mine', 'user', $1, NULL, 'slack', 'api_key', 'Mine', 'active')`, moveOwner, moveSrc)
	f.trigger("t-org-bound", "p-move", true, strp("conn-org"))
	f.trigger("t-personal", "p-move", true, strp("conn-mine"))
	f.trigger("t-person-paused", "p-move", false, nil)
	f.trigger("t-system-paused", "p-move", true, nil)
	paused, err := f.repo.PauseTrigger(ctx, "t-system-paused", core.TriggerPausedCreatorLeftOrg)
	require.NoError(t, err)
	require.True(t, paused)

	// Rows that stay: the owner's saved workflow, a user update about the
	// project (a log stamped with the org it was written in), a plan.
	draft := newDraftForTest(moveOwner)
	require.NoError(t, f.repo.CreateWorkflowDraft(ctx, draft))
	f.exec(`UPDATE workflow_drafts SET org_id = $1 WHERE id = $2`, moveSrc, draft.ID)
	require.NoError(t, f.repo.CreateUserUpdate(ctx, &UserUpdate{
		UserID: moveOwner, ProjectID: strp("p-move"), UpdateType: UserUpdateProjectSettingsChanged,
		EntityType: EntityTypeProject, EntityID: "p-move", Data: []byte(`{}`),
	}))
	updatesBefore := f.count(`SELECT count(*) FROM user_updates WHERE user_id = $1 AND org_id = $2`, moveOwner, moveSrc)
	require.Positive(t, updatesBefore)
	shareEventsBefore := f.count(`SELECT count(*) FROM share_events`)

	move := f.prepared("p-move")
	res, err := f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err)
	require.False(t, res.AlreadyMoved)
	require.Nil(t, res.Blockers)

	// Moves.
	assert.Equal(t, strp(moveDst), f.org("projects", "p-move"))
	for _, id := range []string{"c-shared", "c-archived", "c-error"} {
		assert.Equal(t, strp(moveDst), f.org("chats", id), "chat %s", id)
		assert.Nil(t, f.visibility("chats", id), "chat %s", id)
	}
	for _, id := range []string{"r-shared", "r-archived", "r-archived-sub", "r-error"} {
		assert.Equal(t, strp(moveDst), f.org("workflows", id), "run %s", id)
	}
	assert.Nil(t, f.visibility("projects", "p-move"))
	assert.Equal(t, ChatStateArchived, f.chatState("c-archived"), "archived stays archived")

	triggers := map[string]*core.Trigger{}
	for _, id := range []string{"t-org-bound", "t-personal", "t-person-paused", "t-system-paused"} {
		tr, err := f.repo.GetTrigger(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, strp(moveDst), tr.OrgID, "trigger %s", id)
		assert.False(t, tr.Enabled, "trigger %s", id)
		triggers[id] = tr
	}
	assert.Equal(t, core.TriggerPausedOrgMove, triggers["t-org-bound"].PausedReason)
	assert.Nil(t, triggers["t-org-bound"].ConnectionID, "the source org's bot is not carried")
	assert.Equal(t, core.TriggerPausedOrgMove, triggers["t-personal"].PausedReason)
	assert.Equal(t, strp("conn-mine"), triggers["t-personal"].ConnectionID, "the owner's own connection stays")
	assert.Empty(t, triggers["t-person-paused"].PausedReason, "a person's pause is kept, not claimed by the move")
	assert.Equal(t, core.TriggerPausedCreatorLeftOrg, triggers["t-system-paused"].PausedReason)
	assert.Equal(t, 4, f.count(`SELECT count(*) FROM trigger_changes WHERE operation = $1 AND user_id = $2`, core.TriggerChangeOrgMoved, moveOwner))

	// The cleared shares, recorded in the org that lost them.
	events, err := f.repo.ListShareEvents(ctx, core.ShareKindProject, "p-move")
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, core.ShareActionMoved, events[0].Action)
	assert.Equal(t, moveSrc, events[0].OrgID)
	events, err = f.repo.ListShareEvents(ctx, core.ShareKindChat, "c-shared")
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, core.ShareActionMoved, events[0].Action)
	assert.Equal(t, moveSrc, events[0].OrgID)
	assert.Equal(t, moveOwner, events[0].ActorUserID)
	assert.Equal(t, shareEventsBefore+2, f.count(`SELECT count(*) FROM share_events`))
	// The org feed (org-sharing S4) reports each unshare in the source org,
	// so its members' shared lists drop the items, and says nothing to the
	// target's.
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM org_updates WHERE org_id = $1 AND update_type = $2 AND resource_id IN ('p-move', 'c-shared')`,
		moveSrc, int32(reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_UNSHARED)))
	assert.Zero(t, f.count(`SELECT count(*) FROM org_updates WHERE org_id = $1`, moveDst))

	// Stays.
	assert.Equal(t, src, f.org("projects", "p-stay"))
	assert.Equal(t, src, f.org("chats", "c-stay"))
	assert.Equal(t, strp(string(core.VisibilityOrg)), f.visibility("chats", "c-stay"))
	assert.Equal(t, src, f.org("workflows", "r-stay"))
	stay, err := f.repo.GetTrigger(ctx, "t-stay")
	require.NoError(t, err)
	assert.Equal(t, src, stay.OrgID)
	assert.True(t, stay.Enabled)
	assert.Equal(t, src, f.org("workflow_drafts", draft.ID), "saved workflows belong to no project")
	assert.Equal(t, updatesBefore, f.count(`SELECT count(*) FROM user_updates WHERE user_id = $1 AND org_id = $2`, moveOwner, moveSrc),
		"the user update log keeps the org each entry was stamped with")
	assert.Equal(t, 1, f.count(`SELECT count(*) FROM connections WHERE id = 'conn-org' AND org_id = $1`, moveSrc), "the source org's connection is not touched")

	// The ledger.
	got, err := f.repo.GetProjectOrgMove(ctx, move.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ProjectOrgMoveStateMoved, got.State)
	assert.Equal(t, core.ProjectOrgMoveCounts{
		Chats: 3, Runs: 4, Triggers: 4, TriggersPaused: 2, SharesCleared: 2, BindingsCleared: 1,
	}, got.Counts)

	// A retried commit is a no-op that reports the move it made.
	again, err := f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err)
	assert.True(t, again.AlreadyMoved)
	assert.Equal(t, got.Counts, again.Move.Counts)
	assert.Equal(t, 4, f.count(`SELECT count(*) FROM trigger_changes WHERE operation = $1`, core.TriggerChangeOrgMoved))
}

func (f *moveFixture) chatState(id string) ChatState {
	f.t.Helper()
	var s ChatState
	require.NoError(f.t, f.raw.QueryRow(`SELECT state FROM chats WHERE id = $1`, id).Scan(&s))
	return s
}

// A project the backfill has not reached is in the owner's default org: it
// moves from there, its chats and runs with it, and no other source org
// matches it.
func TestMoveProjectOrg_ANullOrgProjectIsInTheDefaultOrg(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.project("p-null", nil)
	f.chat("c-null", "p-null", nil)
	f.run("r-null", "c-null", core.Completed())

	move := f.prepared("p-null")
	_, err := f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID, DefaultOrgID: "org-elsewhere"})
	require.ErrorIs(t, err, ErrOrgChanged, "the default org is not the move's source")
	_, err = f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.ErrorIs(t, err, ErrOrgChanged, "no default org: an unassigned project is in no org")
	assert.Nil(t, f.org("projects", "p-null"))

	_, err = f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID, DefaultOrgID: moveSrc})
	require.NoError(t, err)
	assert.Equal(t, strp(moveDst), f.org("projects", "p-null"))
	assert.Equal(t, strp(moveDst), f.org("chats", "c-null"))
	assert.Equal(t, strp(moveDst), f.org("workflows", "r-null"))
}

// Running work refuses the move, names what runs, and writes nothing.
func TestMoveProjectOrg_RefusesUnlessQuiescent(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *moveFixture)
		check func(t *testing.T, b *core.ProjectOrgMoveBlockers)
	}{{
		name:  "a running chat",
		setup: func(f *moveFixture) { f.run("r-live", "c-1", core.Active()) },
		check: func(t *testing.T, b *core.ProjectOrgMoveBlockers) {
			require.Equal(t, 1, b.ChatsTotal)
			assert.Equal(t, "c-1", b.Chats[0].ChatID)
			assert.Equal(t, int32(reliantv1.ChatActivity_CHAT_ACTIVITY_RUNNING), b.Chats[0].Activity)
			assert.Equal(t, 1, b.LiveRuns)
		},
	}, {
		name: "a pending approval",
		setup: func(f *moveFixture) {
			f.run("r-gated", "c-1", core.Active())
			insertTestApproval(f.t, f.repo, "a-1", "c-1", 1)
		},
		check: func(t *testing.T, b *core.ProjectOrgMoveBlockers) {
			require.Equal(t, 1, b.ChatsTotal)
			assert.Equal(t, int32(reliantv1.ChatActivity_CHAT_ACTIVITY_AWAITING_INPUT), b.Chats[0].Activity)
		},
	}, {
		name:  "a paused run",
		setup: func(f *moveFixture) { f.run("r-paused", "c-1", core.Paused()) },
		check: func(t *testing.T, b *core.ProjectOrgMoveBlockers) {
			require.Equal(t, 1, b.ChatsTotal)
			assert.Equal(t, int32(reliantv1.ChatActivity_CHAT_ACTIVITY_PAUSED), b.Chats[0].Activity)
		},
	}, {
		name:  "a pending sub-run under an idle chat",
		setup: func(f *moveFixture) { f.run("r-sub", "c-1", core.Pending()) },
		check: func(t *testing.T, b *core.ProjectOrgMoveBlockers) {
			assert.Equal(t, 1, b.LiveRuns)
		},
	}, {
		name: "a running background process",
		setup: func(f *moveFixture) {
			require.NoError(f.t, f.repo.CreateBackgroundProcess(context.Background(), &BackgroundProcess{
				Command: "npm run dev", WorkingDir: "/tmp", ProjectID: strp("p-busy"), UserID: moveOwner,
				Status: reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_RUNNING,
			}))
		},
		check: func(t *testing.T, b *core.ProjectOrgMoveBlockers) {
			assert.Equal(t, 1, b.BackgroundProcesses)
			assert.Zero(t, b.ChatsTotal)
		},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t)
			ctx := context.Background()
			f.project("p-busy", strp(moveSrc))
			f.chat("c-1", "p-busy", strp(moveSrc))
			f.trigger("t-1", "p-busy", true, nil)
			tc.setup(f)
			move := f.prepared("p-busy")

			preview, err := f.repo.ProjectOrgMoveBlockers(ctx, "p-busy")
			require.NoError(t, err)
			require.False(t, preview.Empty())
			tc.check(t, preview)

			res, err := f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
			require.ErrorIs(t, err, core.ErrProjectNotQuiescent)
			require.NotNil(t, res)
			tc.check(t, res.Blockers)

			assert.Equal(t, strp(moveSrc), f.org("projects", "p-busy"))
			assert.Equal(t, strp(moveSrc), f.org("chats", "c-1"))
			tr, err := f.repo.GetTrigger(ctx, "t-1")
			require.NoError(t, err)
			assert.True(t, tr.Enabled, "a refused move pauses nothing")
			got, err := f.repo.GetProjectOrgMove(ctx, move.ID)
			require.NoError(t, err)
			assert.Equal(t, core.ProjectOrgMoveStatePrepared, got.State)
		})
	}
}

// A launch holding the owner's org lock, having decided on the source org,
// commits before the move: the move then sees its run and refuses. A write
// decided on the source org after the move refuses with ErrOrgChanged.
func TestMoveProjectOrg_SerializesWithTheOwnersOrgLock(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.project("p-race", strp(moveSrc))
	f.chat("c-race", "p-race", strp(moveSrc))
	move := f.prepared("p-race")

	locked, release := make(chan struct{}), make(chan struct{})
	launchDone := make(chan error, 1)
	go func() {
		launchDone <- f.repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			if err := f.repo.LockUserOrgs(ctx, moveOwner, OrgRead{Table: "projects", ID: "p-race", Org: strp(moveSrc)}); err != nil {
				return err
			}
			close(locked)
			<-release
			_, err := f.repo.ORM().Bun().NewRaw(`INSERT INTO workflows (id, chat_id, workflow_name, thread, state, created_at)
				VALUES ('r-race', 'c-race', 'builtin://agent', 'r-race', 2, NOW())`).Exec(ctx)
			return err
		})
	}()
	select {
	case <-locked:
	case err := <-launchDone:
		t.Fatalf("the launch ended before it held the lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the launch never took the org lock")
	}

	moveDone := make(chan error, 1)
	go func() {
		_, err := f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
		moveDone <- err
	}()
	require.Eventually(t, func() bool {
		var waiting int
		err := f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE l.locktype = 'advisory' AND NOT l.granted AND a.datname = current_database()`).Scan(ctx, &waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 5*time.Millisecond, "the move never queued on the org lock")
	select {
	case err := <-moveDone:
		t.Fatalf("the move ran while a launch held the owner's org lock: %v", err)
	default:
	}

	close(release)
	require.NoError(t, <-launchDone)
	require.ErrorIs(t, <-moveDone, core.ErrProjectNotQuiescent, "the launch committed first, so its run blocks the move")
	assert.Equal(t, strp(moveSrc), f.org("workflows", "r-race"))

	// Once the run ends, the move commits; a write that decided on the
	// source org before it is refused.
	f.exec(`UPDATE workflows SET state = 3, stop_reason = 1, completed_at = NOW() WHERE id = 'r-race'`)
	_, err := f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err)
	assert.Equal(t, strp(moveDst), f.org("workflows", "r-race"))
	err = f.repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return f.repo.LockUserOrgs(ctx, moveOwner, OrgRead{Table: "projects", ID: "p-race", Org: strp(moveSrc)})
	})
	require.True(t, errors.Is(err, ErrOrgChanged), "a decision on the source org after the move: %v", err)
	err = f.repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return f.repo.LockUserOrgs(ctx, moveOwner, OrgRead{Table: "chats", ID: "c-race", Org: strp(moveSrc)})
	})
	require.True(t, errors.Is(err, ErrOrgChanged), "a chat decided on in the source org after the move: %v", err)
}

// The ledger: a repeated request key is the same move, a project has one open
// move at a time, and each step follows only the states it may.
func TestProjectOrgMoves_Ledger(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	f.project("p-ledger", strp(moveSrc))
	req := &core.ProjectOrgMove{
		ID: uuid.NewString(), ProjectID: "p-ledger", OwnerUserID: moveOwner, ActorUserID: moveOwner,
		FromOrgID: moveSrc, ToOrgID: moveDst, RequestKey: "key-1",
	}
	first, created, err := f.repo.InsertProjectOrgMove(ctx, req)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, core.ProjectOrgMoveStatePreparing, first.State)

	retry := *req
	retry.ID = uuid.NewString()
	again, created, err := f.repo.InsertProjectOrgMove(ctx, &retry)
	require.NoError(t, err)
	assert.False(t, created, "the same request key is the same move")
	assert.Equal(t, first.ID, again.ID)

	other := *req
	other.ID, other.RequestKey = uuid.NewString(), "key-2"
	_, _, err = f.repo.InsertProjectOrgMove(ctx, &other)
	require.ErrorIs(t, err, core.ErrProjectOrgMoveInProgress)

	_, err = f.repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: first.ID})
	require.ErrorIs(t, err, core.ErrProjectOrgMoveState, "a move that is not prepared does not commit")
	_, err = f.repo.TransitionProjectOrgMove(ctx, first.ID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePrepared}, core.ProjectOrgMoveStateMoved, "", nil)
	require.ErrorIs(t, err, core.ErrProjectOrgMoveState)
	_, err = f.repo.TransitionProjectOrgMove(ctx, uuid.NewString(),
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePreparing}, core.ProjectOrgMoveStateAborting, "", nil)
	require.ErrorIs(t, err, core.ErrProjectOrgMoveNotFound)

	for _, to := range []core.ProjectOrgMoveState{core.ProjectOrgMoveStateAborting, core.ProjectOrgMoveStateAborted} {
		_, err = f.repo.TransitionProjectOrgMove(ctx, first.ID,
			[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePreparing, core.ProjectOrgMoveStateAborting}, to, "control-plane refused", nil)
		require.NoError(t, err)
	}
	next, created, err := f.repo.InsertProjectOrgMove(ctx, &other)
	require.NoError(t, err)
	assert.True(t, created, "an aborted move frees the project")
	assert.NotEqual(t, first.ID, next.ID)
}

// The widened share_events CHECK is validated after the migration chain, and
// admits 'moved'.
func TestShareEventsActionCheckAdmitsMoved(t *testing.T) {
	f := newMoveFixture(t)
	var validated bool
	require.NoError(t, f.raw.QueryRow(`SELECT convalidated FROM pg_constraint
		WHERE conname = 'share_events_action_check' AND conrelid = 'share_events'::regclass`).Scan(&validated))
	require.True(t, validated)
	f.exec(`INSERT INTO share_events (id, org_id, resource_kind, resource_id, owner_user_id, actor_user_id, action)
		VALUES ('se-moved', 'o', 'chat', 'c', 'u', 'u', 'moved')`)
	_, err := f.raw.Exec(`INSERT INTO share_events (id, org_id, resource_kind, resource_id, owner_user_id, actor_user_id, action)
		VALUES ('se-bogus', 'o', 'chat', 'c', 'u', 'u', 'bogus')`)
	require.Error(t, err)
}
