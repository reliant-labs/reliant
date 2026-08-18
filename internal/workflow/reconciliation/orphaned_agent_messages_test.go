// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveOrphanedAgentMessages_ResolvesTerminalThreadBacklog covers the
// sweep's reason for existing: rows queued for a thread whose loop had
// already exited before the live resolution path existed. They are
// permanently undeliverable -- delivery happens only at a loop-step boundary
// and a terminal thread takes no steps -- so the sweep must resolve them and
// count one anomaly per row.
func TestResolveOrphanedAgentMessages_ResolvesTerminalThreadBacklog(t *testing.T) {
	before := anomalyCount("orphaned_agent_messages_resolved")
	repo := newMockRepo()
	repo.orphanedMailboxThreads = []string{"dead-thread-1"}
	repo.orphanedMailboxRows = map[string]int64{"dead-thread-1": 2}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	resolved, err := reconciler.resolveOrphanedAgentMessages(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 2, resolved, "both stranded rows must be resolved")
	assert.Equal(t, before+2, anomalyCount("orphaned_agent_messages_resolved"),
		"one anomaly per stranded message, not per thread")
	assert.Equal(t, []string{"dead-thread-1"}, repo.resolvedMailboxThreads)
}

// TestResolveOrphanedAgentMessages_LeavesLiveThreadsAlone is the assertion
// that keeps the sweep from becoming worse than the bug. A queued row for a
// thread that is still running is not stranded -- it is in flight, waiting
// for that agent's next boundary. Marking it undelivered would destroy a
// message that was about to arrive, and unlike a missed orphan (caught next
// pass) that loss is unrecoverable.
//
// The guard lives in the query's WHERE clause (terminal thread statuses
// only), so a live thread never appears in the list the sweep iterates. This
// pins the contract that the sweep resolves EXACTLY what it is handed and
// never widens its own scope.
func TestResolveOrphanedAgentMessages_LeavesLiveThreadsAlone(t *testing.T) {
	before := anomalyCount("orphaned_agent_messages_resolved")
	repo := newMockRepo()
	// The query returns only terminal-thread rows; a live thread's queue is
	// simply absent from it.
	repo.orphanedMailboxThreads = nil
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	resolved, err := reconciler.resolveOrphanedAgentMessages(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 0, resolved)
	assert.Empty(t, repo.resolvedMailboxThreads,
		"a live thread's mailbox must never be written to by the sweep")
	assert.Equal(t, before, anomalyCount("orphaned_agent_messages_resolved"))
}

// TestResolveOrphanedAgentMessages_IdempotentAcrossPasses covers the race
// between this sweep and the live resolution path in ThreadStatusActivity:
// both target the same rows, and the UPDATE matches status = 1 only, so the
// loser of that race moves zero rows. A zero-row resolve is the ordinary
// "someone already handled it" outcome, not an anomaly to report.
func TestResolveOrphanedAgentMessages_IdempotentAcrossPasses(t *testing.T) {
	before := anomalyCount("orphaned_agent_messages_resolved")
	repo := newMockRepo()
	repo.orphanedMailboxThreads = []string{"dead-thread-1"}
	repo.orphanedMailboxRows = map[string]int64{"dead-thread-1": 0}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	resolved, err := reconciler.resolveOrphanedAgentMessages(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 0, resolved)
	assert.Equal(t, before, anomalyCount("orphaned_agent_messages_resolved"),
		"a row someone else already resolved must not count as a repair")
}

// TestResolveOrphanedAgentMessages_ContinuesPastPerThreadFailure pins
// partial-progress behavior, matching repairStrandedSpawnToolCalls: one
// thread's failed resolve must not abandon the rest of the backlog.
func TestResolveOrphanedAgentMessages_ContinuesPastPerThreadFailure(t *testing.T) {
	repo := newMockRepo()
	repo.orphanedMailboxThreads = []string{"dead-thread-1", "dead-thread-2"}
	repo.orphanedMailboxResolveErr = errors.New("update failed")
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	resolved, err := reconciler.resolveOrphanedAgentMessages(context.Background(), &passStats{})
	require.NoError(t, err, "a per-thread failure is logged and skipped, not surfaced as a pass error")
	assert.Equal(t, 0, resolved)
}

// TestResolveOrphanedAgentMessages_SurfacesListFailure covers the read half:
// if the sweep cannot even see the backlog it must report that, so a pass
// does not silently look clean while orphans accumulate.
func TestResolveOrphanedAgentMessages_SurfacesListFailure(t *testing.T) {
	repo := newMockRepo()
	repo.orphanedMailboxErr = errors.New("query failed")
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, err := reconciler.resolveOrphanedAgentMessages(context.Background(), &passStats{})
	require.Error(t, err)
}

// TestReconcileRunningWorkflows_RunsOrphanedMailboxSweep pins the wiring: the
// sweep must actually run as part of a pass. A repair nothing invokes is a
// repair that never happens.
func TestReconcileRunningWorkflows_RunsOrphanedMailboxSweep(t *testing.T) {
	repo := newMockRepo()
	repo.workflowsByStatus[db.Active()] = nil
	repo.workflowsByStatus[db.Paused()] = nil
	repo.orphanedMailboxThreads = []string{"dead-thread-1"}
	repo.orphanedMailboxRows = map[string]int64{"dead-thread-1": 1}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	assert.Empty(t, errs)
	assert.Equal(t, []string{"dead-thread-1"}, repo.resolvedMailboxThreads,
		"the pass must invoke the orphaned-mailbox sweep")
}
