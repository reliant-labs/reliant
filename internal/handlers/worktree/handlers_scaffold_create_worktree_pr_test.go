package worktree_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestCreateWorktreePR_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedWorktreeService(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.CreateWorktreePRRequest, pb.CreateWorktreePRResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     &pb.CreateWorktreePRRequest{Title: "Add it"},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an empty title is invalid",
			Req:     &pb.CreateWorktreePRRequest{WorktreeId: ownWorktreeID},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     &pb.CreateWorktreePRRequest{WorktreeId: otherWorktreeID, Title: "Add it"},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.CreateWorktreePR)
}
