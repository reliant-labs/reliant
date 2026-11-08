// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
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

// A spawn that finished and REPORTED normally must still leave status 6.
//
// The detached goroutine enqueues the parent's report, but nothing on that
// path moves the tool call's row — and the stranded query filtered reported
// calls out, so the close below was unreachable for exactly the spawns that
// worked. Observed: toolu_013CJA3i on chat 8bb0a875, child stopped 10-02 with
// its kind=4 report delivered, row still "backgrounded" on 10-04.
//
// The report must not be written twice; only the row is closed.
func TestRepairStrandedBackgroundSpawns_ClosesAReportedSpawnWithoutReReporting(t *testing.T) {
	before := anomalyCount("stranded_background_spawn_repaired")
	repo := newMockRepo()
	repo.withBackgroundedToolCall("tc-reported", "chat-1")
	reported := strandedBackgroundSpawn("tc-reported", "chat-1", "parent-thread-1", "child-thread-1", db.Failed())
	reported.HasReport = true
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{reported}
	reconciler := NewReconciler(repo, &mockReconcilerTemporalClient{}, DefaultConfig())

	repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)

	assert.Empty(t, repo.enqueuedAgentMessages, "the parent was already told; it must not be told again")
	assert.Equal(t, 0, repaired, "closing an already-reported spawn is not a repair")
	assert.Equal(t, before, anomalyCount("stranded_background_spawn_repaired"))
	assert.Equal(t, core.ToolCallStatusCancelled, repo.toolCalls["tc-reported"].Status,
		"the row the UI reads must no longer say backgrounded")
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
// regression for the incident on chat 0dc5167e (dev-docs/incidents/
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
// (idx_agent_messages_one_terminal_report_per_chat_spawn) makes the second insert
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

// A stopped spawn is not stranded while the chat's execution that runs it is
// still open: SpawnStopper writes the child's row cancelled the moment it
// signals, and the spawn's goroutine enqueues its "cancelled" report only at
// its next turn boundary, seconds to minutes later. Every prod repair from
// 11-04 to 11-07 was that window (reliant #2394): stop signalled at
// 22:24:56, a "lost in transit (worker interruption)" placeholder written at
// 22:25:10.4, the goroutine's real report 40ms later.
func TestRepairStrandedBackgroundSpawns_LeavesAStoppedSpawnToItsOpenRun(t *testing.T) {
	before := anomalyCount("stranded_background_spawn_repaired")
	repo := newMockRepo()
	repo.chats["chat-1"] = &db.Chat{ID: "chat-1", WorkflowID: ptrTo("root-wf")}
	repo.withBackgroundedToolCall("tc-stopped", "chat-1")
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-stopped", "chat-1", "parent-thread-1", "child-thread-1", db.Cancelled()),
	}
	temporal := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"root-wf": {resp: makeRunningDescribeResp("run-1")},
	}}
	reconciler := NewReconciler(repo, temporal, DefaultConfig())

	repaired, err := reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 0, repaired)
	assert.Empty(t, repo.enqueuedAgentMessages,
		"the open run reports the stop itself; a placeholder here tells the parent a lie about a worker interruption")
	assert.Equal(t, core.ToolCallStatusBackgrounded, repo.toolCalls["tc-stopped"].Status,
		"the goroutine settles its own tool call")
	assert.Equal(t, before, anomalyCount("stranded_background_spawn_repaired"))

	// Once the run has closed, nothing else can report the stop: the repair
	// writes it, as before.
	temporal.describeResponses["root-wf"] = mockDescribeResponse{resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_COMPLETED)}
	repaired, err = reconciler.repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)
	require.Len(t, repo.enqueuedAgentMessages, 1)
	assert.Equal(t, core.AgentMessageKindCancelled, repo.enqueuedAgentMessages[0].Kind)
}

// Temporal that cannot be asked decides nothing: the stopped spawn is left
// for the next pass rather than given a report that may be false.
func TestRepairStrandedBackgroundSpawns_StoppedSpawnWaitsWhenTemporalCannotBeAsked(t *testing.T) {
	repo := newMockRepo()
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-stopped", "chat-1", "parent-thread-1", "child-thread-1", db.Cancelled()),
	}
	temporal := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"chat-1": {err: errors.New("connection refused")},
	}}

	repaired, err := NewReconciler(repo, temporal, DefaultConfig()).repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 0, repaired)
	assert.Empty(t, repo.enqueuedAgentMessages)
}

// A completed child is still repaired while its run is open: its goroutine
// writes the report BEFORE the terminal status, so a terminal completed child
// with no report really did lose it (a failed enqueue).
func TestRepairStrandedBackgroundSpawns_RepairsACompletedChildEvenWhileItsRunIsOpen(t *testing.T) {
	repo := newMockRepo()
	repo.chats["chat-1"] = &db.Chat{ID: "chat-1", WorkflowID: ptrTo("root-wf")}
	repo.strandedBackgroundSpawns = []*db.StrandedBackgroundSpawn{
		strandedBackgroundSpawn("tc-done", "chat-1", "parent-thread-1", "child-thread-1", db.Completed()),
	}
	temporal := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{
		"root-wf": {resp: makeRunningDescribeResp("run-1")},
	}}

	repaired, err := NewReconciler(repo, temporal, DefaultConfig()).repairStrandedBackgroundSpawns(context.Background(), &passStats{})
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)
	require.Len(t, repo.enqueuedAgentMessages, 1)
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

func ptrTo(s string) *string { return &s }
