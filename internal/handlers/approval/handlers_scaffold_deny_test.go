package approval_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	approval "github.com/reliant-labs/reliant/internal/handlers/approval"
)

// approval_test.go covers denying against a real database; these rows pin
// the checks that run before anything is written.
func TestDeny_Generated(t *testing.T) {
	t.Parallel()

	// a-1 is pending in another user's chat; no other approval exists.
	svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: &approval.MockRepository{
		GetApprovalFunc: func(_ context.Context, id string) (*db.Approval, error) {
			if id == "a-1" {
				return &db.Approval{ID: id, ChatID: "chat-1", Status: int32(pb.ApprovalStatus_APPROVAL_STATUS_PENDING)}, nil
			}
			return nil, nil // the store answers a missing approval with (nil, nil)
		},
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: id, UserID: "someone-else"}, nil
		},
	}}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DenyRequest, pb.DenyResponse]{
		{
			Name:    "request_id is required",
			Req:     &pb.DenyRequest{},
			Ctx:     approval.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unknown approval is not found",
			Req:     &pb.DenyRequest{RequestId: "a-2"},
			Ctx:     approval.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's approval is not found",
			Req:     &pb.DenyRequest{RequestId: "a-1"},
			Ctx:     approval.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.Deny)
}
