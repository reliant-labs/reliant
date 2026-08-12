// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strandedBackgroundSpawn(toolCallID, chatID, parentThreadID, childThreadID string, status db.WorkflowStatus) *db.StrandedBackgroundSpawn {
	return &db.StrandedBackgroundSpawn{
		ToolCallID:     toolCallID,
		ChatID:         chatID,
		ParentThreadID: &parentThreadID,
		ChildThreadID:  childThreadID,
		WorkflowStatus: status,
	}
}

// TestRepairStrandedBackgroundSpawns_EnqueuesCompletion covers the repair's
// happy path: a stranded row produces exactly one mailbox enqueue, addressed
// to the parent thread, carrying a Completion kind for a completed child,
// and the anomaly counter advances by one.
func TestRepairStrandedBackgroundSpawns_EnqueuesCompletion(t *testing.T) {
	before := anomalyCount("stranded_background_spawn_repaired")
	repo := newMockRepo()
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-1", "chat-1", "parent-thread-1", "child-thread-1", db.WorkflowStatusCompleted),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)
	assert.Equal(t, before+1, anomalyCount("stranded_background_spawn_repaired"))

	require.Len(t, repo.enqueuedAgentMessages, 1)
	msg := repo.enqueuedAgentMessages[0]
	assert.Equal(t, "parent-thread-1", msg.ToThreadID, "the report must be addressed to the PARENT's thread")
	assert.Equal(t, "child-thread-1", msg.FromThreadID)
	assert.Equal(t, core.AgentMessageKindCompletion, msg.Kind)
	require.NotNil(t, msg.ToolCallID)
	assert.Equal(t, "tc-1", *msg.ToolCallID)
}

// TestRepairStrandedBackgroundSpawns_MapsTerminalStatusToKind pins the
// status->kind mapping: cancelled and failed children must not be reported
// to the parent as if they completed successfully.
func TestRepairStrandedBackgroundSpawns_MapsTerminalStatusToKind(t *testing.T) {
	cases := []struct {
		name     string
		wfStatus db.WorkflowStatus
		wantKind core.AgentMessageKind
	}{
		{"completed", db.WorkflowStatusCompleted, core.AgentMessageKindCompletion},
		{"cancelled", db.WorkflowStatusCancelled, core.AgentMessageKindCancelled},
		{"failed", db.WorkflowStatusFailed, core.AgentMessageKindFailed},
		{"expired", reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_EXPIRED, core.AgentMessageKindFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockRepo()
			repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
				strandedBackgroundSpawn("tc-status", "chat-1", "parent-thread-1", "child-thread-1", tc.wfStatus),
			}
			reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

			_, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
			require.NoError(t, err)
			require.Len(t, repo.enqueuedAgentMessages, 1)
			assert.Equal(t, tc.wantKind, repo.enqueuedAgentMessages[0].Kind)
		})
	}
}

// TestRepairStrandedBackgroundSpawns_SkipsMissingParentThread covers the
// defensive branch: a row with no parent thread id has nowhere to deliver
// into, so the repair must skip it (and not panic dereferencing a nil
// pointer) rather than guess a recipient.
func TestRepairStrandedBackgroundSpawns_SkipsMissingParentThread(t *testing.T) {
	repo := newMockRepo()
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		{ToolCallID: "tc-no-parent", ChatID: "chat-1", ParentThreadID: nil, ChildThreadID: "child-thread-1", WorkflowStatus: db.WorkflowStatusCompleted},
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 0, repaired)
	assert.Empty(t, repo.enqueuedAgentMessages)
}

// TestRepairStrandedBackgroundSpawns_IdempotentAcrossPasses covers spec §11
// item 4's double-run requirement at the reconciler level: running the
// repair twice against a row that a concurrent writer already reported for
// (simulated by EnqueueAgentMessageIfAbsent returning inserted=false, the
// real behavior of the unique-constraint-backed query once a row exists)
// must not count a second anomaly or a second enqueue.
func TestRepairStrandedBackgroundSpawns_IdempotentAcrossPasses(t *testing.T) {
	before := anomalyCount("stranded_background_spawn_repaired")
	repo := newMockRepo()
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-race", "chat-1", "parent-thread-1", "child-thread-1", db.WorkflowStatusCompleted),
	}
	// First call inserts; every call after simulates the unique index
	// already holding a row for this tool_call_id.
	calls := 0
	repo.enqueueAgentMessageInsertsFn = func(_ *db.AgentMessage) (bool, error) {
		calls++
		return calls == 1, nil
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	stats := &passStats{}
	repaired1, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), stats)
	require.NoError(t, err)
	assert.Equal(t, 1, repaired1)

	// The row is still returned by ListStrandedBackgroundSpawnToolCalls in
	// this mock (a real DB would have stopped returning it once the mailbox
	// row exists, per the query's NOT EXISTS clause -- exercised at the DB
	// layer). This asserts the SECOND layer of protection: even if the list
	// query somehow offered the same row twice in one pass, or a stale read
	// let it through, the conditional insert refuses to double-count it.
	repaired2, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), stats)
	require.NoError(t, err)
	assert.Equal(t, 0, repaired2, "a row whose report already exists must not be counted as repaired again")

	assert.Equal(t, before+1, anomalyCount("stranded_background_spawn_repaired"),
		"exactly one anomaly must be recorded across both passes")
	assert.Len(t, repo.enqueuedAgentMessages, 1, "exactly one row must have been enqueued")
}

// TestReconcileRunningWorkflows_RepairsStrandedBackgroundSpawns confirms the
// sweep is actually wired into the background pass, not just callable in
// isolation.
func TestReconcileRunningWorkflows_RepairsStrandedBackgroundSpawns(t *testing.T) {
	repo := newMockRepo()
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-wired", "chat-1", "parent-thread-1", "child-thread-1", db.WorkflowStatusCompleted),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	require.Len(t, repo.enqueuedAgentMessages, 1)
	assert.Contains(t, repo.callOrder, "repair_stranded_background_spawns")
}
