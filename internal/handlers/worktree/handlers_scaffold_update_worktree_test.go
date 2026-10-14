package worktree_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestUpdateWorktree_Generated(t *testing.T) {
	t.Parallel()

	store := ownedWorktreeRepo(t)
	var written *db.Worktree
	store.UpdateWorktreeFunc = func(_ context.Context, w *db.Worktree) error {
		written = w
		return nil
	}
	svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.Deps{Repo: store}))
	renamed := "renamed"

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateWorktreeRequest, pb.UpdateWorktreeResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     connect.NewRequest(&pb.UpdateWorktreeRequest{Name: &renamed}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     connect.NewRequest(&pb.UpdateWorktreeRequest{WorktreeId: otherWorktreeID, Name: &renamed}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a partial update changes only the fields it names",
			Req:  connect.NewRequest(&pb.UpdateWorktreeRequest{WorktreeId: ownWorktreeID, Name: &renamed}),
			Ctx:  worktree.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateWorktreeResponse]) {
				require.NotNil(t, written, "the update must be stored")
				assert.Equal(t, "renamed", written.Name)
				assert.Equal(t, "main", written.BaseBranch)
				assert.Equal(t, "renamed", resp.Msg.GetWorktree().GetName())
				assert.Equal(t, "feature", resp.Msg.GetWorktree().GetBranch())
			},
		},
	}, svc.UpdateWorktree)
}
