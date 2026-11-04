// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func (m *mockRepo) ListStaleApprovals(_ context.Context, limit int) ([]db.StaleApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.staleApprovals) > limit {
		return m.staleApprovals[:limit], nil
	}
	return m.staleApprovals, nil
}

func (m *mockRepo) ResolvePendingApproval(_ context.Context, id string, _ int32, reason, _ string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.approvalAlreadyClosed[id] {
		return false, nil
	}
	if m.closedApprovals == nil {
		m.closedApprovals = map[string]string{}
	}
	m.closedApprovals[id] = reason
	return true, nil
}

func (m *mockRepo) ListUnannouncedApprovalResolutions(_ context.Context, limit int) ([]*db.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*db.Approval
	for _, a := range m.unannounced {
		announced := false
		for _, id := range m.announcedApprovals {
			announced = announced || id == a.ID
		}
		if !announced && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *mockRepo) AnnounceApprovalResolution(_ context.Context, a *db.Approval) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.announcedApprovals = append(m.announcedApprovals, a.ID)
	return nil
}

// A pending approval whose run ended is closed with a reason that says why,
// and one a decision beat to it is not counted as a repair.
func TestResolveStaleApprovals_ClosesWithReason(t *testing.T) {
	before := anomalyCount(anomalyStaleApprovalClosed)
	repo := newMockRepo()
	repo.staleApprovals = []db.StaleApproval{
		{ID: "a1", ChatID: "c1", Why: "the run waiting on it was cancelled"},
		{ID: "a2", ChatID: "c1", Why: "its chat was archived"},
	}
	repo.approvalAlreadyClosed = map[string]bool{"a2": true}
	r := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	closed, err := r.resolveStaleApprovals(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 1, closed)
	assert.Equal(t, "This request can no longer be answered: the run waiting on it was cancelled. Nothing was approved.", repo.closedApprovals["a1"])
	assert.NotContains(t, repo.closedApprovals, "a2")
	assert.Equal(t, before+1, anomalyCount(anomalyStaleApprovalClosed))
}

// The feed repair announces every closed approval the feed still calls
// pending, past one batch, and then stops running for the process.
func TestAnnounceApprovalResolutions_RunsToCompletionOnce(t *testing.T) {
	repo := newMockRepo()
	for i := 0; i < staleApprovalBatch+3; i++ {
		repo.unannounced = append(repo.unannounced, &db.Approval{ID: fmt.Sprintf("a%d", i), ChatID: "c1"})
	}
	r := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	require.NoError(t, r.announceApprovalResolutions(context.Background(), &passStats{}))
	assert.Len(t, repo.announcedApprovals, staleApprovalBatch+3)
	assert.True(t, r.approvalFeedRepaired.Load())

	repo.unannounced = append(repo.unannounced, &db.Approval{ID: "late", ChatID: "c1"})
	require.NoError(t, r.announceApprovalResolutions(context.Background(), &passStats{}))
	assert.NotContains(t, repo.announcedApprovals, "late", "the repair is a once-per-process backfill")
}
