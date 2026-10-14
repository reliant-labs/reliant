package worktree_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestGetWorktreePR_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedWorktreeService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetWorktreePRRequest, pb.GetWorktreePRResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     connect.NewRequest(&pb.GetWorktreePRRequest{}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     connect.NewRequest(&pb.GetWorktreePRRequest{WorktreeId: otherWorktreeID}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetWorktreePR)
}
