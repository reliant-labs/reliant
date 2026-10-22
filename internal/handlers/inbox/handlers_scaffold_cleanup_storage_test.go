package inbox_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	inbox "github.com/reliant-labs/reliant/internal/handlers/inbox"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// inbox_storage_test.go covers the clean-up against a real database; these
// rows pin the checks that run before any machine is contacted.
func TestCleanupStorage_Generated(t *testing.T) {
	t.Parallel()

	req := func() *connect.Request[pb.CleanupStorageRequest] {
		return connect.NewRequest(&pb.CleanupStorageRequest{DaemonId: "d-1", WorktreeIds: []string{"wt-1"}})
	}

	// Without a sweeper (no daemon router) the clean-up is unavailable.
	unwired := inbox.NewTestInbox(t)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CleanupStorageRequest, pb.CleanupStorageResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     req(),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "daemon_id and worktree_ids are required",
			Req:     connect.NewRequest(&pb.CleanupStorageRequest{DaemonId: "d-1"}),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "without a sweeper the clean-up is unavailable",
			Req:     req(),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, unwired.CleanupStorage)

	// The machine belongs to someone else. The sweeper is never reached, so
	// it needs neither a store nor a sender.
	wired := inbox.NewTestInbox(t, inbox.WithInboxDeps(inbox.Deps{
		Repo: &inbox.MockRepository{
			GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
				return &db.Daemon{ID: id, UserID: "someone-else"}, nil
			},
		},
		Sweeper: worktreesweep.New(nil, nil),
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CleanupStorageRequest, pb.CleanupStorageResponse]{
		{
			Name:    "another user's machine is not found",
			Req:     req(),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, wired.CleanupStorage)
}
