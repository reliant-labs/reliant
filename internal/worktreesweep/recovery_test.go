// Copyright (c) 2026 Reliant Labs
package worktreesweep

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

type reconciliationStore struct {
	RecoveryStore
	rows     []*core.WorktreeRecovery
	canceled []string
}

func (s *reconciliationStore) ListWorktreeRecoveries(context.Context, string, string) ([]*core.WorktreeRecovery, error) {
	return s.rows, nil
}
func (s *reconciliationStore) CancelWorktreeRecovery(_ context.Context, _, id, _ string) error {
	s.canceled = append(s.canceled, id)
	return nil
}

func TestRecoveryReconcilesCrashedPreauthorizationClaim(t *testing.T) {
	now := time.Now()
	expired := now.Add(-2 * time.Minute)
	live := now.Add(time.Minute)
	store := &reconciliationStore{rows: []*core.WorktreeRecovery{
		{ID: "crashed", Phase: "verified", UpdatedAt: now.Add(-6 * time.Minute)},
		{ID: "publishing", Phase: "publishing", UpdatedAt: now.Add(-6 * time.Minute)},
		{ID: "recent", Phase: "verified", UpdatedAt: now.Add(-time.Minute)},
		{ID: "live", Phase: "delete_authorized", AuthorizedUntil: &live},
		{ID: "expired", Phase: "delete_authorized", AuthorizedUntil: &expired},
		{ID: "restore", Phase: "verified", Operation: "restoring", UpdatedAt: now.Add(-time.Hour)},
	}}
	s := New(nil, nil).WithGitRecovery(store, nil, false)
	s.now = func() time.Time { return now }
	s.reconcileRecoveryJournals(context.Background(), "owner")
	require.Equal(t, []string{"crashed", "publishing", "expired"}, store.canceled)
}

func TestGitRecoveryRolloutDefaultDoesNotSend(t *testing.T) {
	s := New(nil, nil).WithGitRecovery(nil, nil, false)
	require.False(t, s.recoverHeld(context.Background(), target{}, nil, worktreereclaim.Worktree{State: worktreereclaim.StateArchived}))
	// Missing dependencies can never turn an enable bit into a destructive path.
	s.WithGitRecovery(nil, nil, true)
	require.False(t, s.recoverHeld(context.Background(), target{}, nil, worktreereclaim.Worktree{State: worktreereclaim.StateArchived}))
}
