// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"fmt"
	"testing"

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
		strandedBackgroundSpawn("tc-1", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
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
		{"completed", db.Completed(), core.AgentMessageKindCompletion},
		{"cancelled", db.Cancelled(), core.AgentMessageKindCancelled},
		{"failed", db.Failed(), core.AgentMessageKindFailed},
		// The "expired" case that used to sit here is gone with the status:
		// nothing ever wrote EXPIRED, so it could not reach this mapping.
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
		{ToolCallID: "tc-no-parent", ChatID: "chat-1", ParentThreadID: nil, ChildThreadID: "child-thread-1", WorkflowStatus: db.Completed()},
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
		strandedBackgroundSpawn("tc-race", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
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

// TestRepairStrandedBackgroundSpawns_DeadParentGetsUndeliveredReport is the
// regression for the incident on chat 0dc5167e (docs/incidents/
// 2026-08-12-spawn-history-cap.md, Gap 2): Temporal terminated the parent for
// exceeding its history-count cap, which killed both of its detached spawn
// goroutines mid-flight, and this repair then wrote two reports addressed to
// that dead parent at status = 1 (queued). Delivery only ever happens in
// CallLLM's mailbox drain, so a thread that has already exited never calls
// the LLM again and both rows were
// undeliverable the moment they were written — verified on the live DB as
// still status=1, delivered_at NULL.
//
// A report for a dead recipient must therefore be recorded UNDELIVERED
// (status 3), the same vocabulary MarkQueuedAgentMessagesUndeliveredForThread
// already uses for exactly this fact. It must still be WRITTEN: the row is
// the only surviving record that the sub-agent finished at all.
func TestRepairStrandedBackgroundSpawns_DeadParentGetsUndeliveredReport(t *testing.T) {
	repo := newMockRepo()
	repo.withThread("parent-thread-1", core.ThreadStatusFailed)
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-dead-parent", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)

	require.Len(t, repo.enqueuedAgentMessages, 1, "the report must still be written — the row is the only record the sub-agent finished")
	msg := repo.enqueuedAgentMessages[0]
	assert.Equal(t, core.AgentMessageStatusUndelivered, msg.Status,
		"a report addressed to an already-terminal thread can never be drained, so it must not claim delivery is pending")
	assert.Equal(t, "parent-thread-1", msg.ToThreadID)
	assert.NotContains(t, msg.Body, "spawn_status(",
		"a dead agent cannot run spawn_status; the body must read as a record, not an instruction")
}

// TestRepairStrandedBackgroundSpawns_LiveParentStaysQueued is the other half
// of the same rule, and the one that keeps the fix from over-reaching: a
// parent that is genuinely still running WILL reach another loop boundary, so
// its report must stay queued and actually get delivered. Marking a live
// thread's report undelivered would destroy a message that was about to
// arrive — the unrecoverable direction.
func TestRepairStrandedBackgroundSpawns_LiveParentStaysQueued(t *testing.T) {
	repo := newMockRepo()
	repo.withThread("parent-thread-1", core.ThreadStatusRunning)
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-live-parent", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)

	require.Len(t, repo.enqueuedAgentMessages, 1)
	assert.Equal(t, core.AgentMessageStatusQueued, repo.enqueuedAgentMessages[0].Status)
	assert.Contains(t, repo.enqueuedAgentMessages[0].Body, "spawn_status(",
		"a live parent can still act on this, so the body keeps its instruction")
}

// TestRepairStrandedBackgroundSpawns_UnreadableParentThreadStaysQueued pins
// the fail-closed direction of the liveness check. A thread row that cannot
// be read — transient DB error, or a row that is simply missing — is not
// evidence the agent is dead, and guessing "dead" would silently bury a
// deliverable report. Queued is recoverable: resolveOrphanedAgentMessages
// sweeps it later if the thread really was terminal.
func TestRepairStrandedBackgroundSpawns_UnreadableParentThreadStaysQueued(t *testing.T) {
	repo := newMockRepo() // no threads registered → GetThread errors
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-unknown-parent", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)

	require.Len(t, repo.enqueuedAgentMessages, 1)
	assert.Equal(t, core.AgentMessageStatusQueued, repo.enqueuedAgentMessages[0].Status)
}

// TestRepairStrandedBackgroundSpawns_ClosesBackgroundedToolCall is the second
// defect from the same incident: the repair never moved tool_calls.status off
// 6 (backgrounded), so both spawn calls on chat 0dc5167e still read as
// in-flight in the UI long after their children were terminal.
//
// Cancelled, not failed, mirroring terminalDrainDetachedSpawns
// (runtime/workflow.go) — the live path that handles the same shape. The call
// will never produce a result of its own; that is not the same claim as "the
// spawn ran and errored", which is what failed would assert about a child
// that may well have completed.
func TestRepairStrandedBackgroundSpawns_ClosesBackgroundedToolCall(t *testing.T) {
	repo := newMockRepo()
	repo.withThread("parent-thread-1", core.ThreadStatusFailed)
	repo.withBackgroundedToolCall("tc-still-backgrounded", "chat-1")
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-still-backgrounded", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)

	require.Len(t, repo.upsertedToolCalls, 1, "the stranded call must be moved off backgrounded")
	assert.Equal(t, core.ToolCallStatusCancelled, repo.upsertedToolCalls[0].Status)
	require.NotNil(t, repo.upsertedToolCalls[0].CompletedAt)
	assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-still-backgrounded"].Status,
		"the persisted row — what the UI reads — must no longer say backgrounded")
}

// TestRepairStrandedBackgroundSpawns_ClosesToolCallWhenReportAlreadyExists
// covers the case that produced the observed live state: the mailbox report
// already exists (a concurrent pass, or the detached goroutine's own enqueue
// landing late), so EnqueueAgentMessageIfAbsent reports inserted=false. The
// tool call is still stuck at backgrounded and this pass is still the only
// thing that will ever move it, so an early `continue` on inserted=false
// leaves it in flight forever.
func TestRepairStrandedBackgroundSpawns_ClosesToolCallWhenReportAlreadyExists(t *testing.T) {
	repo := newMockRepo()
	repo.withThread("parent-thread-1", core.ThreadStatusRunning)
	repo.withBackgroundedToolCall("tc-already-reported", "chat-1")
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-already-reported", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	repo.enqueueAgentMessageInsertsFn = func(*db.AgentMessage) (bool, error) { return false, nil }
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 0, repaired, "someone else's report is not this pass's repair")
	assert.Empty(t, repo.enqueuedAgentMessages)

	assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-already-reported"].Status,
		"the call must be closed even when another writer owned the report")
}

// TestRepairStrandedBackgroundSpawns_UndeliverableIsIdempotent runs the
// undeliverable path twice. The unique index
// (idx_agent_messages_one_terminal_report_per_spawn) makes the second insert
// a no-op, and UpsertToolCallStatus refuses to walk an already-terminal row
// backwards, so a second pass must add no rows and count no second anomaly.
// This is what makes the fix safe against ordering with the sibling
// thread-status-cascade work, rather than dependent on it.
func TestRepairStrandedBackgroundSpawns_UndeliverableIsIdempotent(t *testing.T) {
	before := anomalyCount("stranded_background_spawn_undeliverable")
	repo := newMockRepo()
	repo.withThread("parent-thread-1", core.ThreadStatusCompleted)
	repo.withBackgroundedToolCall("tc-idem", "chat-1")
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-idem", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	calls := 0
	repo.enqueueAgentMessageInsertsFn = func(*db.AgentMessage) (bool, error) {
		calls++
		return calls == 1, nil
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	stats := &passStats{}
	first, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), stats)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	second, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), stats)
	require.NoError(t, err)
	assert.Equal(t, 0, second)

	assert.Len(t, repo.enqueuedAgentMessages, 1)
	assert.Equal(t, before+1, anomalyCount("stranded_background_spawn_undeliverable"),
		"exactly one anomaly across both passes")
	assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-idem"].Status)
}

// rootRunRepo is mockRepo plus one chat whose root workflow has a given
// status — just enough for rootAwaitingResume to read. Kept local: the shared
// mock has no GetWorkflow, and every other test relies on GetChat failing so
// the guard stays out of the way.
type rootRunRepo struct {
	*mockRepo
	chatID string
	root   *db.Workflow
}

func (r *rootRunRepo) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	if id == r.chatID {
		rootID := r.root.ID
		return &db.Chat{ID: id, WorkflowID: &rootID}, nil
	}
	return r.mockRepo.GetChat(ctx, id)
}

func (r *rootRunRepo) GetWorkflow(_ context.Context, id string) (*db.Workflow, error) {
	if id == r.root.ID {
		return r.root, nil
	}
	return nil, fmt.Errorf("workflow not found: %s", id)
}

// GetWorkflowCheckpoint follows the real contract for "none": (nil, nil).
func (r *rootRunRepo) GetWorkflowCheckpoint(_ context.Context, _ string) (*db.WorkflowCheckpoint, error) {
	return nil, nil
}

// TestRepairStrandedBackgroundSpawns_SkipsWhileRootRunIsLive is the other half
// of the chat e6c09159 incident. The root was killed as wedged, the reap
// stamped its background spawns' workflow rows failed, and the user's resume
// reset-and-replayed the root back to ACTIVE. Twenty-five seconds later this
// sweep saw terminal children with no report, fabricated one for each, and
// took both spawns' single terminal-report slots — while replay was
// re-executing those very spawns, whose real results then had nowhere to go.
//
// A background spawn runs inside the root's own execution, so while the root
// is live the spawn is running or about to be re-run and will report for
// itself. The sweep must leave it alone. Paused and pending roots are live
// for the same reason.
func TestRepairStrandedBackgroundSpawns_SkipsWhileRootRunIsLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		root db.WorkflowStatus
	}{
		{"root reset-and-resumed to active", db.Active()},
		{"root paused", db.Paused()},
		{"root pending", db.Pending()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newMockRepo()
			base.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
				strandedBackgroundSpawn("tc-live-root", "chat-live-root", "root-thread", "child-thread", db.Failed()),
			}
			repo := &rootRunRepo{
				mockRepo: base,
				chatID:   "chat-live-root",
				root:     &db.Workflow{ID: "root-thread", ChatID: "chat-live-root", Status: tc.root},
			}
			reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

			repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
			require.NoError(t, err)
			assert.Equal(t, 0, repaired)
			assert.Empty(t, base.enqueuedAgentMessages,
				"a spawn under a live root will report for itself; a stand-in would take its one report slot")
		})
	}
}

// TestRepairStrandedBackgroundSpawns_RepairsUnderEndedRoot pins that the live-
// root skip did not swallow the sweep's real job: a root that ENDED and cannot
// resume (completed here, so there is no checkpoint to relaunch from) left
// spawns that will never report, and they must still get one.
func TestRepairStrandedBackgroundSpawns_RepairsUnderEndedRoot(t *testing.T) {
	base := newMockRepo()
	base.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-ended-root", "chat-ended-root", "root-thread", "child-thread", db.Completed()),
	}
	repo := &rootRunRepo{
		mockRepo: base,
		chatID:   "chat-ended-root",
		root:     &db.Workflow{ID: "root-thread", ChatID: "chat-ended-root", Status: db.Completed()},
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	require.Len(t, base.enqueuedAgentMessages, 1)
}

// TestReconcileRunningWorkflows_RepairsStrandedBackgroundSpawns confirms the
// sweep is actually wired into the background pass, not just callable in
// isolation.
func TestReconcileRunningWorkflows_RepairsStrandedBackgroundSpawns(t *testing.T) {
	repo := newMockRepo()
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-wired", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	require.Len(t, repo.enqueuedAgentMessages, 1)
	assert.Contains(t, repo.callOrder, "repair_stranded_background_spawns")
}
