package worktree_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestCommitWorktree_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedWorktreeService(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CommitWorktreeRequest, pb.CommitWorktreeResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     connect.NewRequest(&pb.CommitWorktreeRequest{Message: "fix"}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an empty commit message is invalid",
			Req:     connect.NewRequest(&pb.CommitWorktreeRequest{WorktreeId: ownWorktreeID}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     connect.NewRequest(&pb.CommitWorktreeRequest{WorktreeId: otherWorktreeID, Message: "fix"}),
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.CommitWorktree)
}
