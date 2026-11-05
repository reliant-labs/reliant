package approval_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	approval "github.com/reliant-labs/reliant/internal/handlers/approval"
)

// personRepo is a repository holding one pending approval in a chat the
// test user owns, recording what the handler reads and writes.
func personRepo() *approval.MockRepository {
	return &approval.MockRepository{
		GetApprovalFunc: func(_ context.Context, id string) (*db.Approval, error) {
			return &db.Approval{ID: id, ChatID: "chat-1", Status: int32(pb.ApprovalStatus_APPROVAL_STATUS_PENDING),
				ApprovalType: int32(pb.ApprovalType_APPROVAL_TYPE_TOOL_CALL)}, nil
		},
		GetChatRowFunc: func(_ context.Context, id string) (*db.ChatRow, error) {
			return &db.ChatRow{ID: id, UserID: "test-user"}, nil
		},
		RunTxFunc: func(ctx context.Context, _ orm.TxPolicy, f func(context.Context) error) error { return f(ctx) },
		UpdateApprovalStatusFunc: func(context.Context, string, int32, *string, *string, *string) error {
			return nil
		},
		CreateChatUpdateFunc: func(context.Context, string, pb.ChatUpdateType, string, string) error { return nil },
	}
}

// decisions are the four RPCs that resolve an approval.
var decisions = []struct {
	name string
	call func(*approval.Service, context.Context) error
}{
	{"Approve", func(s *approval.Service, ctx context.Context) error {
		_, err := s.Approve(ctx, connect.NewRequest(&pb.ApproveRequest{RequestId: "a-1"}))
		return err
	}},
	{"Deny", func(s *approval.Service, ctx context.Context) error {
		_, err := s.Deny(ctx, connect.NewRequest(&pb.DenyRequest{RequestId: "a-1"}))
		return err
	}},
	{"BatchApprove", func(s *approval.Service, ctx context.Context) error {
		_, err := s.BatchApprove(ctx, connect.NewRequest(&pb.BatchApproveRequest{RequestIds: []string{"a-1", "a-2"}}))
		return err
	}},
	{"BatchDeny", func(s *approval.Service, ctx context.Context) error {
		_, err := s.BatchDeny(ctx, connect.NewRequest(&pb.BatchDenyRequest{RequestIds: []string{"a-1", "a-2"}}))
		return err
	}},
}

func asCaller(c auth.Credential) context.Context {
	return auth.WithCredential(auth.WithUser(context.Background(), "test-user", "authenticated", ""), c)
}

// An approval means a person decided it (reliant #2123): a credential an agent
// or a script can hold — the CLI login and API tokens, an OAuth client's
// token, or a context nothing classified — is refused before anything is read
// or written, on every RPC that resolves one, even for the chat's owner.
func TestDecisionsRefuseCredentialsSoftwareHolds(t *testing.T) {
	t.Parallel()
	for _, cred := range []auth.Credential{auth.CredentialAccessToken, auth.CredentialOAuthClient, auth.CredentialUnknown} {
		for _, d := range decisions {
			t.Run(d.name+" with "+cred.String(), func(t *testing.T) {
				t.Parallel()
				repo := personRepo()
				svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: repo}))

				err := d.call(svc, asCaller(cred))

				require.Error(t, err)
				require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "err = %v", err)
				ce := svcerr.ToConnect(err)
				require.Equal(t, auth.ReasonPersonRequired, ce.Meta().Get(svcerr.ReasonHeader))
				require.Contains(t, ce.Message(), "Reliant app")
				require.Zero(t, repo.CallCount("GetApproval"), "nothing is read for a refused caller")
				require.Zero(t, repo.CallCount("UpdateApprovalStatus"), "nothing is written for a refused caller")
			})
		}
	}
}

// A person signed in to the app decides approvals as before, and so does the
// self-host shared key, which cannot tell a person from an agent at all.
func TestDecisionsAcceptAPerson(t *testing.T) {
	t.Parallel()
	for _, cred := range []auth.Credential{auth.CredentialSession, auth.CredentialSharedKey} {
		for _, d := range decisions {
			t.Run(d.name+" with "+cred.String(), func(t *testing.T) {
				t.Parallel()
				repo := personRepo()
				svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: repo}))

				require.NoError(t, d.call(svc, asCaller(cred)))
				require.NotZero(t, repo.CallCount("UpdateApprovalStatus"))
			})
		}
	}
}

// Listing approvals is not deciding one: the CLI's `workflow follow` and
// `supervise` read them with the CLI login.
func TestListApprovalsAcceptsAnAccessToken(t *testing.T) {
	t.Parallel()
	repo := personRepo()
	repo.ListPendingApprovalsByChatFunc = func(context.Context, string) ([]*db.Approval, error) { return nil, nil }
	svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: repo}))

	_, err := svc.ListApprovalsByChat(asCaller(auth.CredentialAccessToken),
		connect.NewRequest(&pb.ListApprovalsByChatRequest{ChatId: "chat-1"}))
	require.NoError(t, err)
}
