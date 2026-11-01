// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// latestApprovalFeedStatus is the status the chat feed's newest update for an
// approval names: what a client rebuilding the chat's approvals reads.
func latestApprovalFeedStatus(t *testing.T, repo *db.Repo, chatID, approvalID string) string {
	t.Helper()
	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(context.Background(), chatID)
	require.NoError(t, err)
	for _, u := range updates {
		if u.EntityID != approvalID || u.UpdateType != db.UpdateTypeApproval {
			continue
		}
		var data struct {
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(u.Data, &data))
		return data.Status
	}
	return ""
}

// notActionableErr asserts err is the "no longer pending" refusal and returns
// the sentence the person sees.
func notActionableErr(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	ce := svcerr.ToConnect(err)
	assert.Equal(t, connect.CodeFailedPrecondition, ce.Code())
	assert.Equal(t, ReasonApprovalNotPending, ce.Meta().Get(svcerr.ReasonHeader))
	return ce.Message()
}

// An approval whose wait timed out is closed as EXPIRED with its reason, the
// close is announced on the chat feed (so no client keeps an Approve button
// for it), and a late Approve says what happened instead of a bare
// "not pending".
func TestApproval_ExpiredIsAnnouncedAndExplained(t *testing.T) {
	service, repo := setupTestApprovalService(t)
	ctx := context.Background()
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	closed, err := repo.ResolvePendingApproval(ctx, approvalID,
		int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED), "no one answered within 1h", "timeout")
	require.NoError(t, err)
	require.True(t, closed)

	a, err := repo.GetApproval(ctx, approvalID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED), a.Status)
	require.NotNil(t, a.DenialReason)
	assert.Equal(t, "no one answered within 1h", *a.DenialReason)
	require.NotNil(t, a.ResolvedAt)
	assert.Equal(t, "expired", latestApprovalFeedStatus(t, repo, chatID, approvalID))

	// A second close finds it no longer pending and changes nothing.
	closed, err = repo.ResolvePendingApproval(ctx, approvalID,
		int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_CANCELLED), "run ended", "run_ended")
	require.NoError(t, err)
	assert.False(t, closed)

	_, err = service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{RequestId: approvalID}))
	assert.Equal(t, "This request expired: no one answered within 1h", notActionableErr(t, err))

	_, err = service.BatchApprove(approvalTestCtx(), connect.NewRequest(&reliantv1.BatchApproveRequest{RequestIds: []string{approvalID}}))
	assert.Equal(t, "This request expired: no one answered within 1h", notActionableErr(t, err))
}

// A person's decision that landed first is kept: closing an approved
// approval as expired does nothing.
func TestResolvePendingApproval_KeepsADecision(t *testing.T) {
	_, repo := setupTestApprovalService(t)
	ctx := context.Background()
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED))

	closed, err := repo.ResolvePendingApproval(ctx, approvalID,
		int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED), "timed out", "timeout")
	require.NoError(t, err)
	assert.False(t, closed)
	a, err := repo.GetApproval(ctx, approvalID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), a.Status)
}

// A row timed out before EXPIRED existed (DENIED, action "timeout") is
// explained as expired, not as a denial nobody made.
func TestApprove_LegacyTimedOutDenialSaysExpired(t *testing.T) {
	service, repo := setupTestApprovalService(t)
	ctx := context.Background()
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	timeout := "timeout"
	require.NoError(t, repo.UpdateApprovalStatus(ctx, approvalID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), nil, &timeout, nil))

	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{RequestId: approvalID}))
	assert.Contains(t, notActionableErr(t, err), "This request expired")
}

// "Approve all" over a chat's approvals skips one that was resolved since the
// panel read it, and approves the rest, instead of failing the whole batch.
func TestBatchApprove_SkipsResolvedApprovals(t *testing.T) {
	service, repo := setupTestApprovalService(t)
	ctx := context.Background()
	chatID, workflowID := createApprovalTestData(t, repo)
	live := createTestApproval(t, repo, chatID, workflowID+"-a", int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	stale := createTestApproval(t, repo, chatID, workflowID+"-b", int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	_, err := repo.ResolvePendingApproval(ctx, stale, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_CANCELLED), "the run waiting on it finished", "run_ended")
	require.NoError(t, err)

	resp, err := service.BatchApprove(approvalTestCtx(), connect.NewRequest(&reliantv1.BatchApproveRequest{RequestIds: []string{stale, live}}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), resp.Msg.Approved)
	assert.Contains(t, resp.Msg.Message, "1 already resolved")

	a, err := repo.GetApproval(ctx, live)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), a.Status)
	s, err := repo.GetApproval(ctx, stale)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_CANCELLED), s.Status, "a skipped approval keeps its close")
}

// A pending approval in an archived chat is stale: nothing will act on it.
func TestListStaleApprovals_ArchivedChat(t *testing.T) {
	_, repo := setupTestApprovalService(t)
	ctx := context.Background()
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	stale, err := repo.ListStaleApprovals(ctx, 100)
	require.NoError(t, err)
	assert.NotContains(t, staleIDs(stale), approvalID, "a live run's approval is not stale")

	require.NoError(t, repo.UpdateChatState(ctx, chatID, db.ChatStateArchived, "test"))
	stale, err = repo.ListStaleApprovals(ctx, 100)
	require.NoError(t, err)
	require.Contains(t, staleIDs(stale), approvalID)
	for _, s := range stale {
		if s.ID == approvalID {
			assert.Equal(t, "its chat was archived", s.Why)
		}
	}
}

func staleIDs(stale []db.StaleApproval) []string {
	ids := make([]string, len(stale))
	for i, s := range stale {
		ids[i] = s.ID
	}
	return ids
}

// A row closed without an announcement (what ApprovalResolve did before it
// announced) is found, and announcing it fixes the feed.
func TestListUnannouncedApprovalResolutions(t *testing.T) {
	_, repo := setupTestApprovalService(t)
	ctx := context.Background()
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeApproval, approvalID, `{"update_type":"approval","id":"`+approvalID+`","status":"pending"}`))
	timeout := "timeout"
	require.NoError(t, repo.UpdateApprovalStatus(ctx, approvalID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), nil, &timeout, nil))
	require.Equal(t, "pending", latestApprovalFeedStatus(t, repo, chatID, approvalID))

	rows, err := repo.ListUnannouncedApprovalResolutions(ctx, 100)
	require.NoError(t, err)
	var found *db.Approval
	for _, r := range rows {
		if r.ID == approvalID {
			found = r
		}
	}
	require.NotNil(t, found)

	require.NoError(t, repo.AnnounceApprovalResolution(ctx, found))
	assert.Equal(t, "expired", latestApprovalFeedStatus(t, repo, chatID, approvalID), "a legacy timeout is announced as what it was")
	rows, err = repo.ListUnannouncedApprovalResolutions(ctx, 100)
	require.NoError(t, err)
	for _, r := range rows {
		assert.NotEqual(t, approvalID, r.ID)
	}
}
