// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orgMoveFixture is a move fixture whose user's default org is def, with two
// projects on one machine and an idle chat in the first.
func orgMoveFixture(t *testing.T, def string) (f *moveFixture, from, to, chatID string) {
	t.Helper()
	f = newMoveFixture(t)
	f.service.deps.Orgs = &MockOrgResolver{
		DefaultOrgFunc: func(context.Context, string) (string, error) { return def, nil },
	}
	from, fromMain := f.project(moveUser, "billing")
	to, _ = f.project(moveUser, "properties")
	laptop := f.machine(moveUser, "laptop")
	f.install(from, laptop, "/home/me/billing")
	f.install(to, laptop, "/home/me/properties")
	chatID = f.chat(from, fromMain, &laptop)
	return f, from, to, chatID
}

func (f *moveFixture) setOrg(table, id, org string) {
	f.t.Helper()
	_, err := f.repo.ORM().Bun().NewRaw("UPDATE "+table+" SET org_id = ? WHERE id = ?", org, id).Exec(f.ctx)
	require.NoError(f.t, err)
}

func (f *moveFixture) projectOrg(id string) *string {
	f.t.Helper()
	p, err := f.repo.GetProject(f.ctx, id)
	require.NoError(f.t, err)
	return p.OrgID
}

// A chat with no org yet is its owner's default org's: it cannot reach
// another org by moving into one of its projects before the backfill.
func TestMoveChatToProject_UnassignedChatStaysInTheDefaultOrg(t *testing.T) {
	t.Parallel()
	f, from, to, chatID := orgMoveFixture(t, "home")
	f.setOrg("projects", to, "acme")

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	requireCode(t, err, connect.CodeFailedPrecondition)
	row := f.row(chatID)
	assert.Equal(t, from, row.ProjectID)
	assert.Nil(t, row.OrgID, "a refused move stamps nothing")
}

// An org chat cannot move into a project with no org yet that the backfill
// would stamp with another org.
func TestMoveChatToProject_OrgChatRefusesAnUnassignedProjectOfAnotherOrg(t *testing.T) {
	t.Parallel()
	f, from, to, chatID := orgMoveFixture(t, "home")
	f.setOrg("chats", chatID, "acme")

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	requireCode(t, err, connect.CodeFailedPrecondition)
	assert.Equal(t, from, f.row(chatID).ProjectID)
	assert.Nil(t, f.projectOrg(to), "a refused move stamps nothing")
}

// When both sides are the same org once unassigned rows count as the default,
// the move stamps the unassigned side with it, so no later backfill can split
// them.
func TestMoveChatToProject_StampsTheUnassignedSide(t *testing.T) {
	t.Parallel()
	f, _, to, chatID := orgMoveFixture(t, "acme")
	f.setOrg("chats", chatID, "acme")

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	require.NoError(t, err)
	assert.Equal(t, to, f.row(chatID).ProjectID)
	require.NotNil(t, f.projectOrg(to))
	assert.Equal(t, "acme", *f.projectOrg(to))

	g, _, to2, legacy := orgMoveFixture(t, "home")
	_, err = g.move(&reliantv1.MoveChatToProjectRequest{ChatId: legacy, ProjectId: to2})
	require.NoError(t, err)
	row := g.row(legacy)
	require.NotNil(t, row.OrgID)
	assert.Equal(t, "home", *row.OrgID)
	require.NotNil(t, g.projectOrg(to2))
	assert.Equal(t, "home", *g.projectOrg(to2))
}

// The org check is part of the move's write: a target stamped with another
// org between the handler's read and the write (a concurrent backfill under a
// changed default) refuses the move instead of splitting chat and project.
func TestMoveChatToProject_OrgCheckedAtTheWrite(t *testing.T) {
	t.Parallel()
	f, from, to, chatID := orgMoveFixture(t, "home")
	toMain, err := f.service.launcher().ResolveChatWorktreeID(f.ctx, to, nil)
	require.NoError(t, err)
	f.setOrg("projects", to, "acme") // after the read, before the write

	home := "home"
	moved, err := f.repo.MoveChatToProject(f.ctx, db.ChatMove{
		ChatID: chatID, FromProjectID: from, ToProjectID: to, WorktreeID: *toMain, OrgID: &home,
	})
	require.NoError(t, err)
	assert.False(t, moved)
	row := f.row(chatID)
	assert.Equal(t, from, row.ProjectID)
	assert.Nil(t, row.OrgID)
}
