package inbox_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db/core"
	inbox "github.com/reliant-labs/reliant/internal/handlers/inbox"
)

// worktreesweep's forget_test.go covers Forget against a real database; these
// rows pin the checks that run before anything is written.
func TestForgetStorageWorktree_Generated(t *testing.T) {
	t.Parallel()

	// The caller holds no such workspace.
	svc := inbox.NewTestInbox(t, inbox.WithInboxDeps(inbox.Deps{
		Repo: &inbox.MockRepository{
			ListHeldWorktreesForUserFunc: func(context.Context, string) ([]*core.HeldWorktree, error) { return nil, nil },
		},
	}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ForgetStorageWorktreeRequest, pb.ForgetStorageWorktreeResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     &pb.ForgetStorageWorktreeRequest{WorktreeId: "wt-1"},
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "worktree_id is required",
			Req:     &pb.ForgetStorageWorktreeRequest{},
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a workspace the caller does not hold is not found",
			Req:     &pb.ForgetStorageWorktreeRequest{WorktreeId: "wt-1"},
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ForgetStorageWorktree)
}
