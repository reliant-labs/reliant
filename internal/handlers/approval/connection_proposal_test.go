// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	workflowpkg "github.com/reliant-labs/reliant/internal/workflow"
)

// fakeProposalConnections holds connections by id; Get answers the caller's
// own, GetOrg an org's.
type fakeProposalConnections map[string]*core.Connection

func (f fakeProposalConnections) Get(_ context.Context, userID, id string) (*core.Connection, error) {
	if c := f[id]; c != nil && c.Owner() == core.UserConnectionOwner(userID) {
		return c, nil
	}
	return nil, errors.New("not found")
}

func (f fakeProposalConnections) GetOrg(_ context.Context, _, orgID, id string) (*core.Connection, error) {
	if c := f[id]; c != nil && c.Owner() == core.OrgConnectionOwner(orgID) {
		return c, nil
	}
	return nil, errors.New("not found")
}

// orgRole is a membership whose caller has one role in every org.
type orgRole string

func (orgRole) IsMember(context.Context, string, string) (bool, bool, error) { return true, true, nil }
func (r orgRole) RoleFresh(context.Context, string, string) (string, bool, error) {
	return string(r), true, nil
}

// asOrgRole is the chat's owner, signed in as a person, with role in every
// org.
func asOrgRole(role string) context.Context {
	return authz.WithCredentialKind(authz.WithMembership(approvalTestCtx(), orgRole(role)), authz.CredentialKindPerson)
}

func proposalConnections() fakeProposalConnections {
	org := "org-1"
	return fakeProposalConnections{
		"mine":        {ID: "mine", OwnerKind: core.ConnectionOwnerUser, UserID: "test-user", IntegrationID: "slack", Name: "My Slack"},
		"mine-gmail":  {ID: "mine-gmail", OwnerKind: core.ConnectionOwnerUser, UserID: "test-user", IntegrationID: "gmail"},
		"theirs":      {ID: "theirs", OwnerKind: core.ConnectionOwnerUser, UserID: "someone-else", IntegrationID: "slack"},
		"org-slack":   {ID: "org-slack", OwnerKind: core.ConnectionOwnerOrg, UserID: "test-user", OrgID: &org, IntegrationID: "slack"},
		"other-org-1": {ID: "other-org-1", OwnerKind: core.ConnectionOwnerOrg, UserID: "test-user", OrgID: ptr("org-2"), IntegrationID: "slack"},
	}
}

func ptr(s string) *string { return &s }

func setupProposalService(t *testing.T) (*Service, *db.Repo, *mockTemporalSignalClient) {
	t.Helper()
	repo := db.NewTestRepo(t)
	mockClient := &mockTemporalSignalClient{}
	service := NewTestApproval(t, WithApprovalDeps(Deps{Repo: repo, Chats: store.NewChatExtStore(repo.ORM()), Signals: workflowpkg.NewPauseService(mockClient, repo), Connections: proposalConnections()}))
	return service, repo, mockClient
}

// createProposalApproval records a pending propose_connection card, as the
// ApprovalCreate activity does.
func createProposalApproval(t *testing.T, repo *db.Repo, chatID, workflowID string, p core.ConnectionProposal) string {
	t.Helper()
	meta, err := json.Marshal(map[string]any{"workflow_id": workflowID, "tool_call_id": "call-1", core.ApprovalMetadataConnectionProposal: p})
	require.NoError(t, err)
	metadata := string(meta)
	id := uuid.New().String()
	require.NoError(t, repo.CreateApproval(context.Background(), &db.Approval{
		ID: id, ChatID: chatID, ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL), EntityID: workflowID + ":" + id,
		Status: int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING), Title: "The agent asks you to connect Slack.", Metadata: &metadata,
		TemporalWorkflowID: workflowID, CreatedAt: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
	}))
	return id
}

func approveWith(service *Service, ctx context.Context, id, connectionID string) error {
	req := &reliantv1.ApproveRequest{RequestId: id}
	if connectionID != "" {
		req.ConnectionId = &connectionID
	}
	_, err := service.Approve(ctx, connect.NewRequest(req))
	return err
}

// The card's client reads the proposal: what to connect, whose, and why.
func TestConnectionProposalListed(t *testing.T) {
	service, repo, _ := setupProposalService(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	createProposalApproval(t, repo, chatID, workflowID, core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", Scope: core.ConnectionProposalOrg, OrgID: "org-1", Reason: "Post the report."})

	resp, err := service.ListApprovalsByChat(approvalTestCtx(), connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Approvals, 1)
	got := resp.Msg.Approvals[0].GetConnectionProposal()
	require.Equal(t, "slack", got.GetIntegrationId())
	require.Equal(t, "Slack", got.GetIntegrationName())
	require.Equal(t, reliantv1.ConnectionProposalScope_CONNECTION_PROPOSAL_SCOPE_ORG, got.GetScope())
	require.Equal(t, "org-1", got.GetOrgId())
	require.Equal(t, "Post the report.", got.GetReason())
}

// Approving with the connection the person just made records it and hands
// only its id to the run.
func TestConnectionProposalApprovedWithTheConnection(t *testing.T) {
	service, repo, mockClient := setupProposalService(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	id := createProposalApproval(t, repo, chatID, workflowID, core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", Scope: core.ConnectionProposalPersonal})

	require.NoError(t, approveWith(service, approvalTestCtx(), id, "mine"))
	signal, ok := mockClient.signalArg.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "approved", signal["status"])
	require.Equal(t, "mine", signal["connection_id"])

	approval, err := repo.GetApproval(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), approval.Status)
	require.Equal(t, "mine", approval.ApprovedConnectionID())
	require.NotNil(t, approval.ConnectionProposal(), "the proposal stays on the approval")
}

// A proposal is approved only by making the connection: Approve alone, or
// in a batch, leaves it pending, and a connection that is not the one the
// card asked for is refused.
func TestConnectionProposalNeedsItsConnection(t *testing.T) {
	service, repo, mockClient := setupProposalService(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	id := createProposalApproval(t, repo, chatID, workflowID, core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", Scope: core.ConnectionProposalPersonal})

	err := approveWith(service, approvalTestCtx(), id, "")
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	require.ErrorContains(t, err, "approve it by making the connection from its card")

	other := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	resp, err := service.BatchApprove(approvalTestCtx(), connect.NewRequest(&reliantv1.BatchApproveRequest{RequestIds: []string{id, other}}))
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.Msg.Approved, "the batch approves the routine approval only")

	for _, wrong := range []string{"mine-gmail", "theirs", "org-slack", "missing"} {
		err := approveWith(service, approvalTestCtx(), id, wrong)
		require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), wrong)
	}
	approval, err := repo.GetApproval(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING), approval.Status)
	require.Equal(t, 1, mockClient.signalCalls, "only the routine approval was signalled")
}

// An org-wide connection is the server's to refuse to a person who is not an
// owner or admin of the organization, whatever the client offered.
func TestConnectionProposalOrgConnectionNeedsAnOrgAdmin(t *testing.T) {
	service, repo, _ := setupProposalService(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	proposal := core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", OrgID: "org-1"}

	id := createProposalApproval(t, repo, chatID, workflowID, proposal)
	err := approveWith(service, asOrgRole("member"), id, "org-slack")
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
	require.ErrorContains(t, err, "only an owner or admin of the organization")

	err = approveWith(service, asOrgRole("admin"), id, "other-org-1")
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "another org's connection is not this chat's org's")

	require.NoError(t, approveWith(service, asOrgRole("admin"), id, "org-slack"))

	// A non-admin may still approve the same proposal with their own.
	id = createProposalApproval(t, repo, chatID, workflowID, proposal)
	require.NoError(t, approveWith(service, asOrgRole("member"), id, "mine"))
}

// Declining, or closing the dialog, is an ordinary denial the run answers.
func TestConnectionProposalDeclined(t *testing.T) {
	service, repo, mockClient := setupProposalService(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	id := createProposalApproval(t, repo, chatID, workflowID, core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack"})

	notCompleted := "not_completed"
	_, err := service.Deny(approvalTestCtx(), connect.NewRequest(&reliantv1.DenyRequest{RequestId: id, ActionTaken: &notCompleted}))
	require.NoError(t, err)
	signal, ok := mockClient.signalArg.(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "denied", signal["status"])
	require.Equal(t, "not_completed", signal["action_taken"])
	require.NotContains(t, signal, "connection_id")
}
