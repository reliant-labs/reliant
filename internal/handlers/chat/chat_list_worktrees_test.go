package chat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func seedListChatsWorktree(t *testing.T, ctx context.Context, repo *db.Repo, projectID, name string, isMain bool) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	w := &db.Worktree{
		ID: uuid.NewString(), Name: name, Path: "/ws/" + name, Branch: "br-" + name, BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain: isMain, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateWorktree(ctx, w))
	return w
}

func seedListChatsChat(t *testing.T, ctx context.Context, repo *db.Repo, projectID string, worktreeID *string, title string) string {
	t.Helper()
	now := time.Now().UTC()
	id := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: id, Title: title, ProjectID: projectID, WorktreeID: worktreeID, UserID: "test-user",
		State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return id
}

// The sidebar groups chats by workspace and must be able to do so without the
// project's full worktree list: ListChats carries each chat's workspace and the
// main workspace itself.
func TestListChats_CarriesEachChatsWorktreeSummary(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo}}

	projectID, mainID := newInvariantTestProject(t, ctx, repo, true)

	// Far more workspaces than any page: only two have chats. The response must
	// not grow with the rest.
	var feature, archived *db.Worktree
	for i := 0; i < 150; i++ {
		w := seedListChatsWorktree(t, ctx, repo, projectID, fmt.Sprintf("agent-%03d", i), false)
		switch i {
		case 7:
			feature = w
		case 8:
			archived = w
		}
	}
	require.NoError(t, repo.ArchiveWorktree(ctx, archived.ID))

	onMain := seedListChatsChat(t, ctx, repo, projectID, &mainID, "on main")
	noWorktree := seedListChatsChat(t, ctx, repo, projectID, nil, "no worktree")
	onFeature := seedListChatsChat(t, ctx, repo, projectID, &feature.ID, "on feature")
	onFeature2 := seedListChatsChat(t, ctx, repo, projectID, &feature.ID, "also on feature")
	onArchived := seedListChatsChat(t, ctx, repo, projectID, &archived.ID, "on archived worktree")

	resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Chats, 5)

	byID := map[string]*reliantv1.Chat{}
	for _, c := range resp.Msg.Chats {
		byID[c.Id] = c
	}

	t.Run("main worktree is carried even for a chat with no worktree", func(t *testing.T) {
		require.NotNil(t, resp.Msg.MainWorktree)
		assert.Equal(t, mainID, resp.Msg.MainWorktree.Id)
		assert.True(t, resp.Msg.MainWorktree.IsMain)
		assert.Equal(t, "main", resp.Msg.MainWorktree.Name)
		assert.Equal(t, "main", resp.Msg.MainWorktree.Branch)
		assert.Nil(t, byID[noWorktree].Worktree, "a chat with no worktree_id has no worktree summary")
	})

	t.Run("each chat carries its own worktree", func(t *testing.T) {
		require.NotNil(t, byID[onMain].Worktree)
		assert.Equal(t, mainID, byID[onMain].Worktree.Id)
		assert.True(t, byID[onMain].Worktree.IsMain)

		for _, id := range []string{onFeature, onFeature2} {
			w := byID[id].Worktree
			require.NotNil(t, w)
			assert.Equal(t, feature.ID, w.Id)
			assert.Equal(t, feature.Name, w.Name)
			assert.Equal(t, feature.Branch, w.Branch)
			assert.Equal(t, feature.Path, w.Path)
			assert.False(t, w.IsMain)
			assert.Equal(t, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE, w.Status)
			assert.Nil(t, w.DeletedAt)
		}
	})

	t.Run("a chat on an archived worktree still names it", func(t *testing.T) {
		w := byID[onArchived].Worktree
		require.NotNil(t, w, "an archived workspace must not hide the chat's workspace")
		assert.Equal(t, archived.ID, w.Id)
		require.NotNil(t, w.DeletedAt)
	})
}

func TestListChats_NoWorktreesYieldsNoSummaries(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo}}

	projectID, _ := newInvariantTestProject(t, ctx, repo, false)
	id := seedListChatsChat(t, ctx, repo, projectID, nil, "lonely")

	resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Chats, 1)
	assert.Equal(t, id, resp.Msg.Chats[0].Id)
	assert.Nil(t, resp.Msg.Chats[0].Worktree)
	assert.Nil(t, resp.Msg.MainWorktree, "a project with no main worktree row reports none")
}

func TestListChats_DoesNotLeakAnotherUsersMainWorktree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	owner := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	intruder := context.WithValue(context.Background(), auth.UserIDContextKey, "someone-else")
	svc := &Service{deps: Deps{Repo: repo}}

	projectID, _ := newInvariantTestProject(t, owner, repo, true)

	resp, err := svc.ListChats(intruder, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Chats)
	assert.Nil(t, resp.Msg.MainWorktree, "another user's project must not disclose its main workspace")
}
