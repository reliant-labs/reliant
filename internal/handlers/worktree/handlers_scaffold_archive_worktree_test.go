package worktree_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

func TestArchiveWorktree_Generated(t *testing.T) {
	t.Parallel()

	svc := newOwnedWorktreeService(t)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ArchiveWorktreeRequest, pb.ArchiveWorktreeResponse]{
		{
			Name:    "a missing worktree_id is invalid",
			Req:     &pb.ArchiveWorktreeRequest{},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's worktree is not found",
			Req:     &pb.ArchiveWorktreeRequest{WorktreeId: otherWorktreeID},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "the project's main worktree cannot be archived",
			Req:     &pb.ArchiveWorktreeRequest{WorktreeId: mainWorktreeID},
			Ctx:     worktree.AuthedContext(t),
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.ArchiveWorktree)
}
