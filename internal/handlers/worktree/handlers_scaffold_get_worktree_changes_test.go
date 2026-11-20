package worktree_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestGetWorktreeChanges_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedWorktreeService(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.GetWorktreeChangesRequest, pb.GetWorktreeChangesResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     &pb.GetWorktreeChangesRequest{},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     &pb.GetWorktreeChangesRequest{WorktreeId: otherWorktreeID},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetWorktreeChanges)
}
