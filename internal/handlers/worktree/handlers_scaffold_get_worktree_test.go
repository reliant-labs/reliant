package worktree_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestGetWorktree_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedWorktreeService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetWorktreeRequest, pb.GetWorktreeResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     connect.NewRequest(&pb.GetWorktreeRequest{}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unknown worktree is not found",
			Req:     connect.NewRequest(&pb.GetWorktreeRequest{WorktreeId: unknownID}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     connect.NewRequest(&pb.GetWorktreeRequest{WorktreeId: otherWorktreeID}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's worktree",
			Req:  connect.NewRequest(&pb.GetWorktreeRequest{WorktreeId: ownWorktreeID}),
			Ctx:  worktree.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.GetWorktreeResponse]) {
				got := resp.Msg.GetWorktree()
				assert.Equal(t, ownWorktreeID, got.GetId())
				assert.Equal(t, ownProjectID, got.GetProjectId())
				assert.Equal(t, "feature", got.GetBranch())
				assert.Equal(t, pb.WorktreeStatus_WORKTREE_STATUS_ACTIVE, got.GetStatus())
				assert.False(t, got.GetIsMain())
			},
		},
	}, svc.GetWorktree)
}
