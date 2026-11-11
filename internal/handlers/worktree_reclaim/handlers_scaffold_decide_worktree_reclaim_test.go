// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The request-shape rows for DecideWorktreeReclaim: what it refuses before it
// reads anything. The rows that need a machine (ownership, the stored report,
// whether it is online, the daemon's reply) are in
// rpc_decide_worktree_reclaim_test.go, one service per test.
//
// Library reference: github.com/reliant-labs/forge/pkg/tdd
package worktree_reclaim_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"

	worktree_reclaim "github.com/reliant-labs/reliant/internal/handlers/worktree_reclaim"
)

func TestDecideWorktreeReclaim_Generated(t *testing.T) {
	t.Parallel()

	// The stub repository answers nothing, so a row that got past the
	// request checks would fail on it rather than pass by accident.
	svc := worktree_reclaim.NewTestWorktreeReclaim(t)
	authed := auth.WithUser(context.Background(), "u-1", "user", "u@example.com")
	one := []*pb.WorktreeReclaimDecision{{Key: "k", Fingerprint: "f", Approve: true}}
	many := make([]*pb.WorktreeReclaimDecision, 51)
	for i := range many {
		many[i] = &pb.WorktreeReclaimDecision{Key: "k", Fingerprint: "f"}
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DecideWorktreeReclaimRequest, pb.DecideWorktreeReclaimResponse]{
		{
			Name:    "unauthenticated",
			Req:     connect.NewRequest(&pb.DecideWorktreeReclaimRequest{DaemonId: "d-1", Decisions: one}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "no_daemon",
			Req:     connect.NewRequest(&pb.DecideWorktreeReclaimRequest{Decisions: one}),
			Ctx:     authed,
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "no_decisions",
			Req:     connect.NewRequest(&pb.DecideWorktreeReclaimRequest{DaemonId: "d-1"}),
			Ctx:     authed,
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "too_many_decisions",
			Req:     connect.NewRequest(&pb.DecideWorktreeReclaimRequest{DaemonId: "d-1", Decisions: many}),
			Ctx:     authed,
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.DecideWorktreeReclaim)
}
