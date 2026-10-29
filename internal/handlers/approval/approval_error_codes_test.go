package approval_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	approval "github.com/reliant-labs/reliant/internal/handlers/approval"
)

// readFailures is what a repository call can fail with, and the code the
// client reads for each: a classified error keeps its own code, and an
// opaque one is Internal with the handler's summary and none of its text.
var readFailures = []struct {
	name string
	err  error
	want connect.Code
}{
	{"a store miss is NotFound", svcerr.NotFound("chat"), connect.CodeNotFound},
	{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
	{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}

// The approval reads (the approval, its chat's ownership) and the resolving
// write each pass a classified repository error through and redact an opaque
// one.
func TestApprovalRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	pending := func(_ context.Context, id string) (*db.Approval, error) {
		return &db.Approval{ID: id, ChatID: "chat-1", Status: int32(pb.ApprovalStatus_APPROVAL_STATUS_PENDING)}, nil
	}
	own := func(_ context.Context, id string) (*db.ChatRow, error) {
		return &db.ChatRow{ID: id, UserID: "test-user"}, nil
	}
	for _, tc := range readFailures {
		t.Run("Approve, reading the approval: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: &approval.MockRepository{
				GetApprovalFunc: func(context.Context, string) (*db.Approval, error) { return nil, tc.err },
			}}))
			_, err := svc.Approve(approval.AuthedContext(t), connect.NewRequest(&pb.ApproveRequest{RequestId: "a-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ListApprovalsByChat, reading the chat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: &approval.MockRepository{
				GetChatRowFunc: func(context.Context, string) (*db.ChatRow, error) { return nil, tc.err },
			}}))
			_, err := svc.ListApprovalsByChat(approval.AuthedContext(t), connect.NewRequest(&pb.ListApprovalsByChatRequest{ChatId: "chat-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("Deny, writing the resolution: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: &approval.MockRepository{
				GetApprovalFunc: pending,
				GetChatRowFunc:  own,
				RunTxFunc: func(context.Context, orm.TxPolicy, func(context.Context) error) error {
					return tc.err
				},
			}}))
			_, err := svc.Deny(approval.AuthedContext(t), connect.NewRequest(&pb.DenyRequest{RequestId: "a-1"}))
			requireClientReads(t, err, tc.want)
		})
	}
}
