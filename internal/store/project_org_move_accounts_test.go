// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The provider-account clause of moving a project to another org
// (control-plane docs/design/org-provider-accounts.md §6, "Project moved", and
// the P8 row of §14): the moved chats' threads drop every sticky account
// another org funds, so the target's scope chooses afresh; the person's own
// accounts are kept; and the triggers' unattended funding is re-stamped for
// the target inside the move (P11 fills the seam in).

const (
	mvOwner = "user-mv-owner"
	mvSrc   = "org-mv-src"
	mvDst   = "org-mv-dst"
)

func (f *credFixture) mvProject(id string) {
	f.t.Helper()
	now := credT0
	org := mvSrc
	require.NoError(f.t, f.env.Repo.CreateProject(context.Background(), &db.Project{
		ID: id, Name: id, Path: "/tmp/" + id, UserID: mvOwner, OrgID: &org,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
}

// mvChat creates an idle chat of the project in the source org, with its
// root thread (the chat's id).
func (f *credFixture) mvChat(id, projectID string) {
	f.t.Helper()
	ctx := context.Background()
	now := credT0
	org := mvSrc
	require.NoError(f.t, f.env.Repo.CreateChat(ctx, &db.Chat{
		ID: id, Title: id, ProjectID: projectID, UserID: mvOwner, OrgID: &org,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := f.env.Repo.CreateThread(ctx, &db.Thread{ID: id, ChatID: id, CreatedAt: now})
	require.NoError(f.t, err)
}

func (f *credFixture) mvTrigger(id, projectID string) {
	f.t.Helper()
	config, err := json.Marshal(core.ScheduleConfig{Cron: []string{"0 9 * * 1-5"}, Timezone: "UTC", Overlap: core.ScheduleOverlapSkip})
	require.NoError(f.t, err)
	now := credT0
	org := mvSrc
	require.NoError(f.t, f.env.Repo.CreateTrigger(context.Background(), &core.Trigger{
		ID: id, UserID: mvOwner, ProjectID: projectID, Name: id, Kind: core.TriggerKindSchedule,
		Enabled: true, Workflow: "nightly", Config: config, OrgID: &org, CreatedAt: now, UpdatedAt: now,
	}))
}

func (f *credFixture) assign(threadID, provider, accountID string) {
	f.t.Helper()
	f.exec(`INSERT INTO thread_provider_accounts (thread_id, provider, account_id, assigned_at, reason)
		VALUES ($1, $2, $3, now(), 'initial')`, threadID, provider, accountID)
}

func (f *credFixture) assigned(threadID string) map[string]string {
	f.t.Helper()
	rows, err := f.env.SQL.QueryContext(context.Background(),
		`SELECT provider, account_id FROM thread_provider_accounts WHERE thread_id = $1`, threadID)
	require.NoError(f.t, err)
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var p, a string
		require.NoError(f.t, rows.Scan(&p, &a))
		out[p] = a
	}
	require.NoError(f.t, rows.Err())
	return out
}

// mvPrepared records a move of projectID from the source to the target and
// advances it to prepared, as control-plane's Prepare step would.
func (f *credFixture) mvPrepared(projectID string) *core.ProjectOrgMove {
	f.t.Helper()
	ctx := context.Background()
	m, created, err := f.env.Repo.InsertProjectOrgMove(ctx, &core.ProjectOrgMove{
		ID: uuid.NewString(), ProjectID: projectID, OwnerUserID: mvOwner, ActorUserID: mvOwner,
		FromOrgID: mvSrc, ToOrgID: mvDst, RequestKey: uuid.NewString(),
	})
	require.NoError(f.t, err)
	require.True(f.t, created)
	m, err = f.env.Repo.TransitionProjectOrgMove(ctx, m.ID,
		[]core.ProjectOrgMoveState{core.ProjectOrgMoveStatePreparing}, core.ProjectOrgMoveStatePrepared, "", nil)
	require.NoError(f.t, err)
	return m
}

// A moved chat's next call resolves in the target's scope: the chat is the
// target's, and none of its threads is still pinned to an account the source
// org funds (its own key, a member's seat or managed key attributed to it).
// The person's own accounts, and a target-org account, stay assigned; the
// owner's chat in a project that did not move keeps its assignment.
func TestMoveProjectOrgClearsAssignmentsAnotherOrgFunds(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.mvProject("p-move")
	f.mvChat("c-move", "p-move")
	f.mvChat("c-move-sub", "p-move")
	f.mvProject("p-stay")
	f.mvChat("c-stay", "p-stay")

	f.orgKey("k-src", mvSrc)
	f.orgKey("k-dst", mvDst)
	f.personal("seat-src", mvOwner, mvSrc, "workspace")
	f.personal("managed-src", mvOwner, mvSrc, "managed")
	f.personal("mine", mvOwner, "", "")
	f.assign("c-move", "anthropic", "k-src")
	f.assign("c-move", "claude", "seat-src")
	f.assign("c-move", "reliant", "managed-src")
	f.assign("c-move", "openrouter", "k-dst")
	f.assign("c-move", "openai", "mine")
	f.assign("c-move-sub", "anthropic", "k-src")
	f.assign("c-stay", "anthropic", "k-src")

	move := f.mvPrepared("p-move")
	_, err := f.env.Repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"openrouter": "k-dst", "openai": "mine"}, f.assigned("c-move"),
		"only the person's own account and the target's are kept")
	assert.Empty(t, f.assigned("c-move-sub"), "every thread of every moved chat is cleared")
	assert.Equal(t, map[string]string{"anthropic": "k-src"}, f.assigned("c-stay"), "a chat that did not move keeps its account")

	chat, err := f.env.Repo.GetChat(ctx, "c-move")
	require.NoError(t, err)
	require.NotNil(t, chat.OrgID)
	assert.Equal(t, mvDst, *chat.OrgID, "the next call is scoped to the target org (chatScope reads the row)")
	assert.Equal(t, 5, f.count(`SELECT count(*) FROM provider_accounts`), "accounts themselves never move or go")
}

// The move calls the trigger-funding seam once, inside its transaction, after
// every trigger of the project is the target's and paused; an error from it
// rolls the whole move back. P11 replaces the seam's body with the re-stamp
// of triggers.unattended_funding.
func TestMoveProjectOrgCallsTheTriggerFundingSeam(t *testing.T) {
	f := newCredFixture(t)
	ctx := context.Background()
	f.mvProject("p-move")
	f.mvChat("c-move", "p-move")
	f.mvTrigger("t-1", "p-move")
	f.mvTrigger("t-2", "p-move")
	f.mvProject("p-stay")
	f.mvTrigger("t-stay", "p-stay")

	type call struct {
		inTx            bool
		moveID, to      string
		commit          string
		moved, enabled  int
		otherProjectSrc int
	}
	var calls []call
	fail := errors.New("seam refused")
	var refuse bool
	store.SetRestampMovedTriggerFunding(t, func(ctx context.Context, odb orm.Context, c core.ProjectOrgMoveCommit, move *core.ProjectOrgMove) error {
		got := call{inTx: orm.InTx(ctx), moveID: move.ID, to: move.ToOrgID, commit: c.MoveID}
		var counts []struct {
			Moved   int `bun:"moved"`
			Enabled int `bun:"enabled"`
			Other   int `bun:"other"`
		}
		if err := odb.Bun().NewRaw(`SELECT
			(SELECT count(*) FROM triggers WHERE project_id = ?0 AND org_id = ?1) AS moved,
			(SELECT count(*) FROM triggers WHERE project_id = ?0 AND enabled) AS enabled,
			(SELECT count(*) FROM triggers WHERE project_id = 'p-stay' AND org_id = ?2) AS other`,
			move.ProjectID, move.ToOrgID, mvSrc).Scan(ctx, &counts); err != nil {
			return err
		}
		got.moved, got.enabled, got.otherProjectSrc = counts[0].Moved, counts[0].Enabled, counts[0].Other
		calls = append(calls, got)
		if refuse {
			return fail
		}
		return nil
	})

	// Refused: nothing of the move is written.
	refuse = true
	move := f.mvPrepared("p-move")
	_, err := f.env.Repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.ErrorIs(t, err, fail)
	assert.Equal(t, 0, f.count(`SELECT count(*) FROM projects WHERE id = 'p-move' AND org_id = $1`, mvDst))
	assert.Equal(t, 2, f.count(`SELECT count(*) FROM triggers WHERE project_id = 'p-move' AND enabled AND org_id = $1`, mvSrc))
	got, err := f.env.Repo.GetProjectOrgMove(ctx, move.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ProjectOrgMoveStatePrepared, got.State, "the move may be retried")

	// Accepted: the move commits.
	refuse = false
	_, err = f.env.Repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err)
	want := call{inTx: true, moveID: move.ID, to: mvDst, commit: move.ID, moved: 2, enabled: 0, otherProjectSrc: 1}
	assert.Equal(t, []call{want, want}, calls,
		"called inside the transaction, with every trigger of the project already the target's and paused")

	// A retried commit of a committed move writes nothing and does not stamp again.
	_, err = f.env.Repo.MoveProjectOrg(ctx, core.ProjectOrgMoveCommit{MoveID: move.ID})
	require.NoError(t, err)
	assert.Len(t, calls, 2)
}
