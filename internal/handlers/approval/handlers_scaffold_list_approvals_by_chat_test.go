package approval_test

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	approval "github.com/reliant-labs/reliant/internal/handlers/approval"
)

// Ownership resolves through the approvals' chat; these rows pin the checks
// that run before any approval is read.
func TestListApprovalsByChat_Generated(t *testing.T) {
	t.Parallel()

	// chat-1 belongs to another user; no other chat exists.
	svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: &approval.MockRepository{
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			if id == "chat-1" {
				return &db.ChatRow{ID: id, UserID: "someone-else"}, nil
			}
			return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
		},
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListApprovalsByChatRequest, pb.ListApprovalsByChatResponse]{
		{
			Name:    "chat_id is required",
			Req:     connect.NewRequest(&pb.ListApprovalsByChatRequest{}),
			Ctx:     approval.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.ListApprovalsByChatRequest{ChatId: "chat-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an unknown chat is not found",
			Req:     connect.NewRequest(&pb.ListApprovalsByChatRequest{ChatId: "chat-2"}),
			Ctx:     approval.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's chat is not found",
			Req:     connect.NewRequest(&pb.ListApprovalsByChatRequest{ChatId: "chat-1"}),
			Ctx:     approval.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListApprovalsByChat)
}
