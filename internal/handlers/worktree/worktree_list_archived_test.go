package worktree_test

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestListWorktrees_NeverAsksForArchivedRows(t *testing.T) {
	repo := ownedWorktreeRepo(t)
	var got db.WorktreeFilters
	repo.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
		got = f
		return []*db.Worktree{{ID: "w", ProjectID: ownProjectID}}, nil
	}
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: repo}))

	_, err := svc.ListWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListWorktreesRequest{ProjectId: ownProjectID}))
	require.NoError(t, err)
	assert.False(t, got.IncludeArchived)
	assert.False(t, got.ArchivedOnly)
	assert.False(t, got.MainOnly)
}

func TestListWorktrees_MainOnlyAsksForMain(t *testing.T) {
	repo := ownedWorktreeRepo(t)
	var got db.WorktreeFilters
	repo.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
		got = f
		return []*db.Worktree{{ID: mainWorktreeID, ProjectID: ownProjectID, IsMain: true}}, nil
	}
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: repo}))

	resp, err := svc.ListWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListWorktreesRequest{ProjectId: ownProjectID, MainOnly: true}))
	require.NoError(t, err)
	assert.True(t, got.MainOnly)
	require.Len(t, resp.Msg.Worktrees, 1)
	assert.True(t, resp.Msg.Worktrees[0].IsMain)
}

func TestListArchivedWorktrees_PagesAndAsksOnlyForArchived(t *testing.T) {
	repo := ownedWorktreeRepo(t)
	all := make([]*db.Worktree, 130)
	for i := range all {
		all[i] = &db.Worktree{ID: fmt.Sprintf("a-%03d", i), ProjectID: ownProjectID}
	}
	repo.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
		require.True(t, f.ArchivedOnly, "the archived RPC reads archived rows only")
		if f.Offset >= len(all) {
			return nil, nil
		}
		return all[f.Offset:min(f.Offset+f.Limit, len(all))], nil
	}
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: repo}))

	var ids []string
	token := ""
	pages := 0
	for {
		req := &reliantv1.ListArchivedWorktreesRequest{ProjectId: ownProjectID}
		if token != "" {
			req.PageToken = &token
		}
		resp, err := svc.ListArchivedWorktrees(worktree.AuthedContext(t), connect.NewRequest(req))
		require.NoError(t, err)
		pages++
		for _, w := range resp.Msg.Worktrees {
			ids = append(ids, w.Id)
		}
		if token = resp.Msg.NextPageToken; token == "" {
			break
		}
		require.Less(t, pages, 10)
	}
	assert.Equal(t, 2, pages)
	assert.Len(t, ids, 130)
}

func TestListArchivedWorktrees_RejectsForeignProjectAndBadToken(t *testing.T) {
	svc := newOwnedWorktreeService(t)

	_, err := svc.ListArchivedWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListArchivedWorktreesRequest{ProjectId: "p-2"}))
	require.Error(t, err)
	assert.NotEqual(t, connect.Code(0), connect.CodeOf(err))

	bad := "not-a-token!"
	_, err = svc.ListArchivedWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListArchivedWorktreesRequest{ProjectId: ownProjectID, PageToken: &bad}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = svc.ListArchivedWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListArchivedWorktreesRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
