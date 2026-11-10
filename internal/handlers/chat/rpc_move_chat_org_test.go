// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moveOrgSource: moveUser is a live member of acme and home; home is the
// default.
type moveOrgSource struct{}

func (moveOrgSource) ResolveOrgMembership(_ context.Context, _, orgID string) (accesstokenclient.OrgMembership, error) {
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "home", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "home"}, nil
}

// withOrgs makes the fixture's deployment one with orgs.
func (f *moveFixture) withOrgs() *moveFixture {
	resolver := orgs.New(moveOrgSource{})
	f.service.deps.Orgs = resolver
	return f
}

// orgProject is project with its org set (nil: none yet).
func (f *moveFixture) orgProject(name string, orgID *string) (projectID, mainWorktreeID string) {
	f.t.Helper()
	projectID = "move-" + uuid.NewString()
	require.NoError(f.t, f.repo.CreateProject(f.ctx, &db.Project{
		ID: projectID, UserID: moveUser, OrgID: orgID, Name: name, Path: "/created/" + name,
		IsGitRepo: true, CreatedAt: f.now, UpdatedAt: f.now, LastActive: f.now,
	}))
	return projectID, f.worktree(projectID, "main", "/created/"+name, true)
}

// A chat with no org yet belongs to its project's org (the backfill stamps it
// so), not to the caller's default: it cannot leave that org for a project
// with no org, which the default would claim, and a move within the org
// stamps the chat with it.
func TestMoveChatToProject_UnassignedChatIsItsProjectsOrg(t *testing.T) {
	t.Parallel()
	acme := "acme"

	f := newMoveFixture(t).withOrgs()
	from, fromMain := f.orgProject("acme-billing", &acme)
	legacy, _ := f.orgProject("legacy", nil)
	chatID := f.chat(from, fromMain, nil)
	require.Nil(t, f.row(chatID).OrgID)

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: legacy})
	requireCode(t, err, connect.CodeFailedPrecondition)
	assert.Contains(t, err.Error(), "another organization")
	assert.Equal(t, from, f.row(chatID).ProjectID)

	to, _ := f.orgProject("acme-properties", &acme)
	_, err = f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	require.NoError(t, err)
	row := f.row(chatID)
	assert.Equal(t, to, row.ProjectID)
	require.NotNil(t, row.OrgID, "the move stamps the org it was decided in")
	assert.Equal(t, acme, *row.OrgID)

	// Both sides with no org resolve to the default: the move stays inside it.
	f2 := newMoveFixture(t).withOrgs()
	a, aMain := f2.orgProject("legacy-a", nil)
	b, _ := f2.orgProject("legacy-b", nil)
	legacyChat := f2.chat(a, aMain, nil)
	_, err = f2.move(&reliantv1.MoveChatToProjectRequest{ChatId: legacyChat, ProjectId: b})
	require.NoError(t, err)
	assert.Equal(t, b, f2.row(legacyChat).ProjectID)
}

// "Neither side has an org yet" is a decision the move statement checks like
// any other: when one side is stamped between the handler's read and the
// write, the move is refused instead of skipping the org check.
func TestMoveChatToProject_BothUnassignedIsCheckedToo(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	from, fromMain := f.orgProject("legacy-a", nil)
	to, toMain := f.orgProject("legacy-b", nil)
	chatID := f.chat(from, fromMain, nil)
	// The decision with nothing assigned: no org, no default needed.
	decided := db.ChatMove{ChatID: chatID, FromProjectID: from, ToProjectID: to, WorktreeID: toMain}

	_, err := f.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = 'acme' WHERE id = ?`, from).Exec(f.ctx)
	require.NoError(t, err)
	moved, err := f.repo.MoveChatToProject(f.ctx, decided)
	require.NoError(t, err)
	assert.False(t, moved, "the source was stamped with an org the target does not have")
	assert.Equal(t, from, f.row(chatID).ProjectID)

	// Stamped alike, the same decision moves it, and the chat takes the org.
	_, err = f.repo.ORM().Bun().NewRaw(`UPDATE projects SET org_id = 'acme' WHERE id = ?`, to).Exec(f.ctx)
	require.NoError(t, err)
	moved, err = f.repo.MoveChatToProject(f.ctx, decided)
	require.NoError(t, err)
	assert.True(t, moved)
	row := f.row(chatID)
	assert.Equal(t, to, row.ProjectID)
	require.NotNil(t, row.OrgID)
	assert.Equal(t, "acme", *row.OrgID)
}

// The org decision is re-checked by the move's own statement: a backfill
// that stamps the chat with another org between the handler's read and the
// write makes the move a no-op, never a move across orgs.
func TestMoveChatToProject_StampBetweenReadAndWriteWins(t *testing.T) {
	t.Parallel()
	home, acme := "home", "acme"
	f := newMoveFixture(t).withOrgs()
	from, fromMain := f.orgProject("legacy", nil)
	to, toMain := f.orgProject("home-properties", &home)
	chatID, undisturbed := f.chat(from, fromMain, nil), f.chat(from, fromMain, nil)

	// The handler decided "home": no org, no project org, so the default.
	decided := func(id string) db.ChatMove {
		return db.ChatMove{
			ChatID: id, FromProjectID: from, ToProjectID: to, WorktreeID: toMain,
			OrgID: &home, DefaultOrgID: &home,
		}
	}

	// Undisturbed, the decision moves the chat and stamps its org.
	moved, err := f.repo.MoveChatToProject(f.ctx, decided(undisturbed))
	require.NoError(t, err)
	assert.True(t, moved)
	row := f.row(undisturbed)
	assert.Equal(t, to, row.ProjectID)
	require.NotNil(t, row.OrgID)
	assert.Equal(t, home, *row.OrgID)

	// A backfill stamps the other chat (through its project) with acme
	// between the decision and the write.
	require.NoError(t, f.repo.AssignUnassignedOrg(f.ctx, moveUser, acme))
	require.Equal(t, &acme, f.row(chatID).OrgID)

	moved, err = f.repo.MoveChatToProject(f.ctx, decided(chatID))
	require.NoError(t, err)
	assert.False(t, moved, "the chat's org changed under the move")
	assert.Equal(t, from, f.row(chatID).ProjectID)
}
