package approval_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
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

// asToken is a context authenticated by the access token p, as the auth
// interceptor leaves it.
func asToken(p *fat.Principal) context.Context {
	return auth.WithMachineToken(asCaller(auth.CredentialAccessToken), p)
}

func principal(res *fat.Resource, scopes ...fat.Scope) *fat.Principal {
	return &fat.Principal{TokenID: "tok-1", ActingUserID: "test-user", Scopes: fat.SetOf(scopes...), Resource: res}
}

// An approval means a person decided it, or a token granted approvals:decide
// explicitly (reliant #2123): a credential an agent or a script can hold — the
// CLI login and API tokens, an OAuth client's token, or a context nothing
// classified — is refused before anything is read or written, on every RPC that
// resolves one, even for the chat's owner.
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

// The credential matrix of the decision rpcs, at the handler: the authority,
// whatever the interceptor in front of it did.
func TestDecisionsCredentialMatrix(t *testing.T) {
	t.Parallel()
	daemon := &fat.Resource{Kind: fat.ResourceDaemon, ID: "daemon-1"}
	cases := []struct {
		name   string
		ctx    context.Context
		reason string // "" = allowed
		scopes string // the scope_missing header
	}{
		{"session", asCaller(auth.CredentialSession), "", ""},
		{"shared key", asCaller(auth.CredentialSharedKey), "", ""},
		{"approvals:decide", asToken(principal(nil, fat.ScopeApprovalsDecide)), "", ""},
		{"approvals:decide with other scopes", asToken(principal(nil, fat.ScopeApprovalsDecide, fat.ScopeInboxManage, fat.ScopeRunsControl)), "", ""},
		{"reliant:api", asToken(principal(nil, fat.ScopeReliantAPI)), fat.ReasonScopeMissing, "approvals:decide"},
		{"wrong scope", asToken(principal(nil, fat.ScopeInboxManage, fat.ScopeQuestionsAnswer)), fat.ReasonScopeMissing, "approvals:decide"},
		{"daemon-bound reliant:api", asToken(principal(daemon, fat.ScopeReliantAPI)), auth.ReasonMachineCredentialCannotDecide, ""},
		{"daemon-bound approvals:decide", asToken(principal(daemon, fat.ScopeApprovalsDecide)), auth.ReasonMachineCredentialCannotDecide, ""},
		{"daemon PAT", asToken(principal(nil, fat.ScopeDaemonConnect, fat.ScopeApprovalsDecide)), auth.ReasonMachineCredentialCannotDecide, ""},
		{"wildcard daemon credential", asToken(&fat.Principal{ActingUserID: "test-user", AllScopes: true, Scopes: fat.SetOf(fat.ScopeApprovalsDecide, fat.ScopeReliantAPI)}), auth.ReasonMachineCredentialCannotDecide, ""},
		{"access token with no principal", asCaller(auth.CredentialAccessToken), auth.ReasonPersonRequired, ""},
		{"oauth client", asCaller(auth.CredentialOAuthClient), auth.ReasonPersonRequired, ""},
	}
	for _, tc := range cases {
		for _, d := range decisions {
			t.Run(tc.name+"/"+d.name, func(t *testing.T) {
				t.Parallel()
				repo := personRepo()
				svc := approval.NewTestApproval(t, approval.WithApprovalDeps(approval.Deps{Repo: repo}))

				err := d.call(svc, tc.ctx)

				if tc.reason == "" {
					require.NoError(t, err)
					require.NotZero(t, repo.CallCount("UpdateApprovalStatus"))
					return
				}
				require.Error(t, err)
				require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "err = %v", err)
				ce := svcerr.ToConnect(err)
				require.Equal(t, tc.reason, ce.Meta().Get(svcerr.ReasonHeader))
				require.Equal(t, tc.scopes, ce.Meta().Get(fat.ScopesHeader))
				require.Zero(t, repo.CallCount("GetApproval"), "nothing is read for a refused caller")
				require.Zero(t, repo.CallCount("UpdateApprovalStatus"), "nothing is written for a refused caller")
			})
		}
	}
}
