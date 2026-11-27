// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/grants/grantstest"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The seeded project internal/db's harness creates.
const seedProjectID = "test-project"

func grantApprovalService(t *testing.T) (*Service, *db.Repo, *grants.Service) {
	t.Helper()
	repo := db.NewTestRepo(t)
	g, err := grants.New(grants.Deps{Store: store.NewPipelineGrantExtStore(repo.ORM()), Cards: repo})
	require.NoError(t, err)
	// No run waits on a capability-grant card, so nothing signals or reads a
	// connection: the mocks fail any call that reaches them.
	svc := NewTestApproval(t, WithApprovalDeps(Deps{
		Logger: testkit.DiscardLogger(), Config: &config.Config{},
		Repo: repo, Chats: store.NewChatExtStore(repo.ORM()),
		Signals: &MockSignaler{}, Connections: &MockProposalConnections{}, Grants: g,
	}))
	return svc, repo, g
}

// grantCard raises a capability-grant card in chatID for f's activation.
func grantCard(t *testing.T, repo *db.Repo, chatID string, f grantstest.Fixture, caps ...core.PipelineCapability) *db.Approval {
	t.Helper()
	meta, err := json.Marshal(core.CapabilityGrantRequest{Kind: core.ApprovalKindPipelineCapabilityGrant,
		TriggerID: f.TriggerID, MachinePoolID: f.PoolID, Capabilities: caps})
	require.NoError(t, err)
	m := string(meta)
	a := &db.Approval{ID: uuid.NewString(), ChatID: chatID, ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL),
		EntityID: "pipeline_capability_grant:" + uuid.NewString(), Status: int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title: "Let the automation approve pull requests", Metadata: &m, CreatedAt: time.Now().UTC()}
	require.NoError(t, repo.CreateApproval(context.Background(), a))
	return a
}

func statusOf(t *testing.T, repo *db.Repo, id string) reliantv1.ApprovalStatus {
	t.Helper()
	a, err := repo.GetApproval(context.Background(), id)
	require.NoError(t, err)
	return reliantv1.ApprovalStatus(a.Status)
}

// Approving the card is what grants: the owner's approval writes the grant,
// recorded with the owner and the card, and the activation's run then holds
// the capability.
func TestApproveCapabilityGrantCardWritesTheGrant(t *testing.T) {
	svc, repo, g := grantApprovalService(t)
	ctx := approvalTestCtx()
	f := grantstest.Seed(t, repo, "test-user", "", seedProjectID)
	card := grantCard(t, repo, f.ChatID, f, grants.Approve)
	run := grants.Run{TriggerID: f.TriggerID, DaemonID: f.DaemonID}
	require.ErrorIs(t, g.Authorize(ctx, run, grants.Approve), grants.ErrNotAuthorized, "nothing is granted before the card is approved")

	_, err := svc.Approve(ctx, connect.NewRequest(&reliantv1.ApproveRequest{RequestId: card.ID}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED, statusOf(t, repo, card.ID))
	live, err := g.List(ctx, "test-user", f.TriggerID, false)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "test-user", live[0].UserID)
	assert.Equal(t, card.ID, live[0].ApprovalID)
	require.NoError(t, g.Authorize(ctx, run, grants.Approve))
}

// Denying the card grants nothing.
func TestDenyCapabilityGrantCardGrantsNothing(t *testing.T) {
	svc, repo, g := grantApprovalService(t)
	ctx := approvalTestCtx()
	f := grantstest.Seed(t, repo, "test-user", "", seedProjectID)
	card := grantCard(t, repo, f.ChatID, f, grants.Approve, grants.Land)
	_, err := svc.Deny(ctx, connect.NewRequest(&reliantv1.DenyRequest{RequestId: card.ID}))
	require.NoError(t, err)
	all, err := g.List(ctx, "test-user", "", true)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// A card approved by someone who does not own the activation and the pool
// grants nothing and stays pending: the grant and the approval commit
// together or not at all.
func TestApproveCapabilityGrantCardNeedsTheActivationOwner(t *testing.T) {
	svc, repo, g := grantApprovalService(t)
	ctx := approvalTestCtx() // test-user, who owns the chat
	f := grantstest.Seed(t, repo, "ada", "", seedProjectID)
	chatID, _ := createApprovalTestData(t, repo)
	card := grantCard(t, repo, chatID, f, grants.Approve)

	_, err := svc.Approve(ctx, connect.NewRequest(&reliantv1.ApproveRequest{RequestId: card.ID}))
	assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%v", err)
	assert.Equal(t, reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING, statusOf(t, repo, card.ID))
	all, err := g.List(ctx, "ada", "", true)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// A capability-grant card is approved on its own: a batch leaves it pending.
func TestBatchApproveLeavesACapabilityGrantCardPending(t *testing.T) {
	svc, repo, g := grantApprovalService(t)
	ctx := approvalTestCtx()
	f := grantstest.Seed(t, repo, "test-user", "", seedProjectID)
	card := grantCard(t, repo, f.ChatID, f, grants.Approve)
	other := grantCard(t, repo, f.ChatID, f, grants.Land)

	_, err := svc.BatchApprove(ctx, connect.NewRequest(&reliantv1.BatchApproveRequest{RequestIds: []string{card.ID, other.ID}}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "%v", err)
	assert.Equal(t, ReasonGrantApprovedAlone, func() string { r, _ := svcerr.ReasonOf(err); return r }())
	assert.Equal(t, reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING, statusOf(t, repo, card.ID))
	assert.Equal(t, reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING, statusOf(t, repo, other.ID))
	all, err := g.List(ctx, "test-user", "", true)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// Without the grants service a capability-grant card cannot be approved.
func TestApproveCapabilityGrantCardWithoutGrantsIsRefused(t *testing.T) {
	svc, repo := setupTestApprovalService(t)
	ctx := approvalTestCtx()
	f := grantstest.Seed(t, repo, "test-user", "", seedProjectID)
	card := grantCard(t, repo, f.ChatID, f, grants.Approve)
	_, err := svc.Approve(ctx, connect.NewRequest(&reliantv1.ApproveRequest{RequestId: card.ID}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "%v", err)
	assert.Equal(t, reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING, statusOf(t, repo, card.ID))
}
