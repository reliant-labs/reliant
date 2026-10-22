package worktree_test

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

// pagedWorktreeRepo serves total worktrees in the order the store returns
// them, honouring Limit and Offset the way the SQL does.
func pagedWorktreeRepo(t *testing.T, total int) *worktree.StubRepo {
	repo := ownedWorktreeRepo(t)
	all := make([]*db.Worktree, total)
	for i := range all {
		all[i] = &db.Worktree{ID: fmt.Sprintf("w-%03d", i), ProjectID: ownProjectID, Name: fmt.Sprintf("wt-%d", i)}
	}
	repo.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
		if f.Offset >= len(all) {
			return nil, nil
		}
		end := min(f.Offset+f.Limit, len(all))
		return all[f.Offset:end], nil
	}
	return repo
}

func listAllWorktrees(t *testing.T, svc *worktree.Service, limit int32) (ids []string, pages int) {
	t.Helper()
	token := ""
	for {
		req := &reliantv1.ListWorktreesRequest{ProjectId: ownProjectID, Limit: limit}
		if token != "" {
			req.PageToken = &token
		}
		resp, err := svc.ListWorktrees(worktree.AuthedContext(t), connect.NewRequest(req))
		require.NoError(t, err)
		pages++
		for _, w := range resp.Msg.Worktrees {
			ids = append(ids, w.Id)
		}
		token = resp.Msg.NextPageToken
		if token == "" {
			return ids, pages
		}
		require.Less(t, pages, 100, "pagination did not terminate")
	}
}

func TestListWorktrees_PagesThroughMoreThanOnePage(t *testing.T) {
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: pagedWorktreeRepo(t, 250)}))

	ids, pages := listAllWorktrees(t, svc, 0)
	assert.Equal(t, 3, pages, "default page size is 100")
	require.Len(t, ids, 250)
	seen := map[string]bool{}
	for _, id := range ids {
		require.False(t, seen[id], "duplicate %s", id)
		seen[id] = true
	}
}

func TestListWorktrees_ExactPageBoundaryHasNoTrailingToken(t *testing.T) {
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: pagedWorktreeRepo(t, 100)}))

	resp, err := svc.ListWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListWorktreesRequest{ProjectId: ownProjectID}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.Worktrees, 100)
	assert.Empty(t, resp.Msg.NextPageToken)
}

func TestListWorktrees_RejectsMalformedPageToken(t *testing.T) {
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: pagedWorktreeRepo(t, 5)}))

	bad := "not-a-token!"
	_, err := svc.ListWorktrees(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.ListWorktreesRequest{ProjectId: ownProjectID, PageToken: &bad}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}
