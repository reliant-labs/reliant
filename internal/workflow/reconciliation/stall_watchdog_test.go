// Copyright (c) 2026 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	activitypb "go.temporal.io/api/activity/v1"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	wfobserve "github.com/reliant-labs/reliant/internal/workflow/observe"
	wfruntime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// fakeTerminator records the executions the watchdog stops, and answers
// with err.
type fakeTerminator struct {
	mu    sync.Mutex
	calls [][3]string
	err   error
}

func (f *fakeTerminator) TerminateExecution(_ context.Context, chatID, workflowID, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [3]string{chatID, workflowID, runID})
	return f.err
}

// fakeStallRecorder keeps the durable records, converging on a repeat of the
// same (workflow, step, node path, source) as the unique index does.
type fakeStallRecorder struct {
	mu   sync.Mutex
	rows []*db.NodeObservation
	seen map[string]bool
}

func (f *fakeStallRecorder) RecordNodeObservation(_ context.Context, obs *db.NodeObservation, _ int) (db.NodeObservationWrite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := obs.WorkflowID + "|" + obs.StepID + "|" + obs.NodePath + "|" + obs.Source
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[key] {
		return db.NodeObservationDuplicate, nil
	}
	f.seen[key] = true
	f.rows = append(f.rows, obs)
	return db.NodeObservationStored, nil
}

// countingQuestions counts the watchdog's user-input reads.
type countingQuestions struct {
	*mockRepo
	calls int
}

func (c *countingQuestions) ListPendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error) {
	c.calls++
	return c.mockRepo.ListPendingQuestionsByChat(ctx, chatID)
}

type stallHarness struct {
	r         *Reconciler
	repo      *mockRepo
	questions *countingQuestions
	temporal  *mockReconcilerTemporalClient
	runs      *fakeTerminator
	records   *fakeStallRecorder
	wf        *db.Workflow
	now       time.Time
}

const (
	stallChat = "chat-stall"
	stallWF   = "wf-stall"
	stallRun  = "run-1"
)

func newStallHarness(t *testing.T, mode StallWatchdogMode) *stallHarness {
	t.Helper()
	return newStallHarnessRecordingTo(t, mode, nil)
}

// newStallHarnessRecordingTo is newStallHarness whose records go to rec (nil:
// the harness's fakeStallRecorder).
func newStallHarnessRecordingTo(t *testing.T, mode StallWatchdogMode, rec StallRecorder) *stallHarness {
	t.Helper()
	repo := newMockRepo()
	running := int(reliantv1.ChatActivity_CHAT_ACTIVITY_RUNNING)
	repo.chats[stallChat] = &db.Chat{ID: stallChat, UserID: "u-1", Activity: &running}
	tc := &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}}
	tc.setPollersActive(true)
	h := &stallHarness{
		repo:      repo,
		questions: &countingQuestions{mockRepo: repo},
		temporal:  tc,
		runs:      &fakeTerminator{},
		records:   &fakeStallRecorder{},
		wf:        &db.Workflow{ID: stallWF, ChatID: stallChat, Thread: "main", WorkflowName: "pr-fix", Status: db.Active()},
		now:       time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC),
	}
	cfg := DefaultConfig()
	cfg.StallWatchdog = mode
	if rec == nil {
		rec = h.records
	}
	h.r = New(Deps{
		Store: repo, RunEvents: repo, Chats: repo, ToolCalls: repo, Workflows: repo, Daemons: repo,
		Questions: h.questions, Temporal: tc, Config: cfg, Mailbox: repo, Orphans: repo,
		Runs: h.runs, Observations: rec,
	})
	h.r.clock = func() time.Time { return h.now }
	h.describe(stallRun, 42)
	h.started(t, stallRun, freshInput())
	return h
}

// freshInput is the start input of a fresh run of the workflow: no resume,
// nothing carried.
func freshInput() wfruntime.WorkflowInput {
	return wfruntime.WorkflowInput{ChatID: stallChat, WorkflowName: "pr-fix", ExecContext: &wfruntime.ExecutionContext{Thread: "main"}}
}

// startedEvent is the start event of an execution started with input,
// encoded as the Temporal client encodes it.
func startedEvent(t *testing.T, input wfruntime.WorkflowInput) *historypb.HistoryEvent {
	t.Helper()
	payloads, err := rtemporal.NewFlexibleDataConverter().ToPayloads(input)
	require.NoError(t, err)
	return &historypb.HistoryEvent{
		EventId:   1,
		EventType: enums.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Input: payloads},
		},
	}
}

// started sets the history of execution runID: its start event, for input,
// then the events given.
func (h *stallHarness) started(t *testing.T, runID string, input wfruntime.WorkflowInput, then ...*historypb.HistoryEvent) {
	t.Helper()
	h.temporal.mu.Lock()
	defer h.temporal.mu.Unlock()
	if h.temporal.historyByRun == nil {
		h.temporal.historyByRun = map[string][]*historypb.HistoryEvent{}
	}
	h.temporal.historyByRun[runID] = append([]*historypb.HistoryEvent{startedEvent(t, input)}, then...)
}

// describe sets what Temporal reports for the run: running, at historyLength
// events, with nothing pending.
func (h *stallHarness) describe(runID string, historyLength int64) *workflowservice.DescribeWorkflowExecutionResponse {
	resp := makeRunningDescribeResp(runID)
	resp.WorkflowExecutionInfo.HistoryLength = historyLength
	h.temporal.mu.Lock()
	h.temporal.describeResponses[stallWF] = mockDescribeResponse{resp: resp}
	h.temporal.mu.Unlock()
	return resp
}

// pass runs one reconcile of the run at now.
func (h *stallHarness) pass(t *testing.T) *ReconciliationResult {
	t.Helper()
	return h.r.ReconcileWorkflow(context.Background(), h.wf)
}

// stallAfter runs a pass that starts the streak, then one d later.
func (h *stallHarness) stallAfter(t *testing.T, d time.Duration) *ReconciliationResult {
	t.Helper()
	h.pass(t)
	h.now = h.now.Add(d)
	return h.pass(t)
}

func TestParseStallWatchdogMode(t *testing.T) {
	for in, want := range map[string]StallWatchdogMode{"": StallWatchdogOff, "off": StallWatchdogOff, " report ": StallWatchdogReport, "enforce": StallWatchdogEnforce} {
		got, err := ParseStallWatchdogMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"on", "true", "Enforce", "audit"} {
		got, err := ParseStallWatchdogMode(bad)
		require.Error(t, err, bad)
		assert.Equal(t, StallWatchdogOff, got, "an unknown mode never turns the watchdog on")
	}
}

func TestStallWatchdog_DefaultsOff(t *testing.T) {
	assert.Equal(t, StallWatchdogMode(""), DefaultConfig().StallWatchdog)
	withConfig := func(cfg *ReconcilerConfig) *Reconciler {
		repo := newMockRepo()
		return New(Deps{
			Store: repo, RunEvents: repo, Chats: repo, ToolCalls: repo, Workflows: repo, Daemons: repo,
			Questions: repo, Mailbox: repo, Orphans: repo, Config: cfg,
			Temporal: &mockReconcilerTemporalClient{describeResponses: map[string]mockDescribeResponse{}},
		})
	}
	r := withConfig(DefaultConfig())
	assert.Equal(t, StallWatchdogOff, r.stallMode)

	// An unknown value stays off; enforce with nothing to stop runs through
	// reports instead of failing open or panicking.
	r = withConfig(&ReconcilerConfig{StallWatchdog: "kill"})
	assert.Equal(t, StallWatchdogOff, r.stallMode)
	r = withConfig(&ReconcilerConfig{StallWatchdog: StallWatchdogEnforce})
	assert.Equal(t, StallWatchdogReport, r.stallMode)
}

// Off, a run that would be stalled costs nothing: no streak, no census, no
// chat, question, poller or history read, no record and no stop.
func TestStallWatchdog_OffDoesNoWork(t *testing.T) {
	h := newStallHarness(t, StallWatchdogOff)
	before := anomalyCount(anomalyStallDetected)
	res := h.stallAfter(t, 24*time.Hour)

	assert.False(t, res.StallTerminated)
	assert.Empty(t, h.r.stallObservations, "off keeps no streak")
	assert.Zero(t, h.questions.calls, "off reads no user-input wait")
	assert.Zero(t, h.temporal.describeTaskQueueCnt, "off checks no pollers")
	assert.Zero(t, h.temporal.getWorkflowHistoryCnt, "off reads no history")
	assert.Empty(t, h.runs.calls)
	assert.Empty(t, h.records.rows)
	assert.Equal(t, before, anomalyCount(anomalyStallDetected))

	state, err := h.r.getTemporalWorkflowState(context.Background(), stallWF)
	require.NoError(t, err)
	assert.Nil(t, state.PendingActivities, "off takes no pending-activity census")
}

func TestStallWatchdog_ReportRecordsAndChangesNothing(t *testing.T) {
	h := newStallHarness(t, StallWatchdogReport)
	before := anomalyCount(anomalyStallDetected)

	// Within the idle ceiling: nothing.
	h.stallAfter(t, DefaultStallMaxIdle-time.Minute)
	assert.Empty(t, h.records.rows)

	h.now = h.now.Add(2 * time.Minute)
	res := h.pass(t)
	assert.False(t, res.StallTerminated)
	assert.Empty(t, h.runs.calls, "report stops nothing")
	assert.Empty(t, h.repo.savedMessages, "report tells the chat nothing")
	assert.Empty(t, h.repo.updatedStatuses, "report writes no status")
	require.Len(t, h.records.rows, 1)
	row := h.records.rows[0]
	assert.Equal(t, db.NodeObservationSourceReconciler, row.Source)
	assert.Equal(t, stallRecordStepID, row.StepID)
	assert.Equal(t, stallWF, row.WorkflowID)
	assert.Equal(t, stallRun, row.RunID, "a fresh run's id is its Temporal run id, as its node observations carry")
	assert.Equal(t, stallChat, row.ChatID)
	var attrs map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Attributes), &attrs))
	assert.Equal(t, "detected", attrs["reliant.stall.outcome"])
	assert.Equal(t, "stalled: no node in flight for 2h1m0s", attrs["reliant.stall.reason"])
	assert.Equal(t, stallRun, attrs["reliant.stall.temporal_run_id"])
	assert.Equal(t, before+1, anomalyCount(anomalyStallDetected))

	// Idempotent: later passes of the same streak report nothing more.
	for i := 0; i < 3; i++ {
		h.now = h.now.Add(time.Hour)
		h.pass(t)
	}
	assert.Len(t, h.records.rows, 1)
	assert.Equal(t, before+1, anomalyCount(anomalyStallDetected))
}

func TestStallWatchdog_EnforceStopsTheExactExecutionOnce(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	before := anomalyCount(anomalyStallTerminated)

	res := h.stallAfter(t, 3*time.Hour)
	require.True(t, res.StallTerminated)
	require.Equal(t, [][3]string{{stallChat, stallWF, stallRun}}, h.runs.calls, "stops exactly the execution the streak watched")
	require.Len(t, h.repo.savedMessages, 1)
	assert.Contains(t, h.repo.savedMessages[0].content, "stalled: no node in flight for 3h0m0s")
	require.Len(t, h.records.rows, 1)
	assert.Contains(t, h.records.rows[0].Attributes, `"reliant.stall.outcome":"terminated"`)
	assert.Equal(t, before+1, anomalyCount(anomalyStallTerminated))

	// Temporal still says running on the next pass (the cancel is on its
	// way): the same streak is never stopped twice.
	h.now = h.now.Add(time.Minute)
	assert.False(t, h.pass(t).StallTerminated)
	assert.Len(t, h.runs.calls, 1)
	assert.Len(t, h.records.rows, 1)
}

// A newer execution replaced the stalled one (a person's turn, a later
// chain): TerminateExecution refuses it, and nothing is recorded or said.
func TestStallWatchdog_EnforceLeavesANewerExecutionAlone(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	h.runs.err = runs.ErrExecutionNotCurrent

	res := h.stallAfter(t, 3*time.Hour)
	assert.False(t, res.StallTerminated)
	require.Len(t, h.runs.calls, 1)
	assert.Equal(t, stallRun, h.runs.calls[0][2])
	assert.Empty(t, h.records.rows)
	assert.Empty(t, h.repo.savedMessages)
	assert.Empty(t, h.r.stallObservations, "the streak is dropped")

	// The newer execution starts its own streak; it is not judged by the
	// old one's idle time.
	h.runs.err = nil
	h.describe("run-2", 7)
	h.now = h.now.Add(time.Minute)
	assert.False(t, h.pass(t).StallTerminated)
	assert.Len(t, h.runs.calls, 1)
}

func TestStallWatchdog_EnforceRetriesAFailedStop(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	h.runs.err = errors.New("temporal unavailable")
	assert.False(t, h.stallAfter(t, 3*time.Hour).StallTerminated)
	assert.Empty(t, h.records.rows)

	h.runs.err = nil
	h.now = h.now.Add(30 * time.Second)
	assert.True(t, h.pass(t).StallTerminated)
	assert.Len(t, h.runs.calls, 2)
}

func TestStallWatchdog_ProgressRestartsTheStreak(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	h.pass(t)
	h.now = h.now.Add(DefaultStallMaxIdle - time.Minute)
	h.describe(stallRun, 43) // a node finished
	h.pass(t)
	h.now = h.now.Add(10 * time.Minute)
	assert.False(t, h.pass(t).StallTerminated, "idle is measured from the last progress")
	assert.Empty(t, h.runs.calls)
}

// A run parked on a person, its machine or a usage limit is never stalled.
func TestStallWatchdog_ParkedRunsAreNeverStopped(t *testing.T) {
	for _, tc := range []struct {
		name string
		park func(h *stallHarness)
	}{
		{"approval", func(h *stallHarness) {
			h.repo.pendingApprovals[stallChat] = []*db.Approval{{ID: "a-1"}}
		}},
		{"question", func(h *stallHarness) {
			h.repo.pendingQuestions[stallChat] = &db.Question{ID: "q-1"}
		}},
		{"awaiting input", chatActivity(reliantv1.ChatActivity_CHAT_ACTIVITY_AWAITING_INPUT)},
		{"waiting for its machine", chatActivity(reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON)},
		{"queued for a machine", chatActivity(reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE)},
		{"usage-limit timer", chatActivity(reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_USAGE_LIMIT)},
		{"paused row", func(h *stallHarness) {
			h.repo.workflowsByChat[stallChat] = []*db.Workflow{{ID: "child", ChatID: stallChat, Status: db.Paused()}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newStallHarness(t, StallWatchdogEnforce)
			tc.park(h)
			assert.False(t, h.stallAfter(t, 48*time.Hour).StallTerminated)
			assert.Empty(t, h.runs.calls)
			assert.Empty(t, h.records.rows)
		})
	}
}

func chatActivity(a reliantv1.ChatActivity) func(h *stallHarness) {
	return func(h *stallHarness) {
		v := int(a)
		h.repo.chats[stallChat].Activity = &v
	}
}

// In-flight work that is not an activity parks the run while it is pending:
// a child workflow, a nexus operation, a workflow task being processed.
func TestStallWatchdog_PendingChildOrTaskParksTheRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(resp *workflowservice.DescribeWorkflowExecutionResponse)
	}{
		{"child", func(resp *workflowservice.DescribeWorkflowExecutionResponse) {
			resp.PendingChildren = []*workflowpb.PendingChildExecutionInfo{{WorkflowId: "child-1"}}
		}},
		{"nexus", func(resp *workflowservice.DescribeWorkflowExecutionResponse) {
			resp.PendingNexusOperations = []*workflowpb.PendingNexusOperationInfo{{Operation: "op"}}
		}},
		{"workflow task", func(resp *workflowservice.DescribeWorkflowExecutionResponse) {
			resp.PendingWorkflowTask = &workflowpb.PendingWorkflowTaskInfo{State: enums.PENDING_WORKFLOW_TASK_STATE_STARTED, Attempt: 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newStallHarness(t, StallWatchdogEnforce)
			tc.set(h.describe(stallRun, 42))
			assert.False(t, h.stallAfter(t, 48*time.Hour).StallTerminated)
			assert.Empty(t, h.runs.calls)
		})
	}
}

// A pending node activity parks the run until its own timeout plus grace:
// the threshold is max(idle ceiling, the in-flight node's timeout + grace).
func TestStallWatchdog_InFlightNodeTimeoutRaisesTheThreshold(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fromHistory bool
	}{{"timeout from the describe", false}, {"timeout from the history", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newStallHarness(t, StallWatchdogEnforce)
			started := h.now
			pa := &workflowpb.PendingActivityInfo{
				ActivityId:      "7",
				ActivityType:    &commonpb.ActivityType{Name: "ExecuteRunStep"},
				State:           enums.PENDING_ACTIVITY_STATE_STARTED,
				ScheduledTime:   timestamppb.New(started),
				LastStartedTime: timestamppb.New(started),
			}
			if tc.fromHistory {
				h.started(t, stallRun, freshInput(), &historypb.HistoryEvent{
					EventType: enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
					Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
						ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
							ActivityId: "7", StartToCloseTimeout: durationpb.New(3 * time.Hour),
						},
					},
				})
			} else {
				pa.ActivityOptions = &activitypb.ActivityOptions{StartToCloseTimeout: durationpb.New(3 * time.Hour)}
			}
			resp := h.describe(stallRun, 42)
			resp.PendingActivities = []*workflowpb.PendingActivityInfo{pa}

			// Past the idle ceiling, inside the node's 3h + grace.
			assert.False(t, h.stallAfter(t, 3*time.Hour+StallNodeGrace-time.Minute).StallTerminated)
			assert.Empty(t, h.runs.calls)

			h.now = started.Add(3*time.Hour + StallNodeGrace + time.Minute)
			assert.True(t, h.pass(t).StallTerminated, "past the node's timeout + grace")
			// Once per streak: for the timeouts and the run id together, or,
			// with the timeouts described, for the run id alone.
			assert.Equal(t, 1, h.temporal.getWorkflowHistoryCnt, "the history is read once per streak")
			require.Len(t, h.records.rows, 1)
			assert.Equal(t, stallRun, h.records.rows[0].RunID)
		})
	}
}

// A pending activity whose timeout cannot be learned is work that cannot be
// bounded, and the run is never called stalled for it.
func TestStallWatchdog_UnboundedInFlightWorkIsNeverStopped(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	resp := h.describe(stallRun, 42)
	resp.PendingActivities = []*workflowpb.PendingActivityInfo{{
		ActivityId: "9", ScheduledTime: timestamppb.New(h.now), LastStartedTime: timestamppb.New(h.now),
	}}
	assert.False(t, h.stallAfter(t, 30*24*time.Hour).StallTerminated)
	assert.Empty(t, h.runs.calls)
}

func TestStallWatchdog_ConversationsAreNotWatched(t *testing.T) {
	for _, name := range []string{"builtin://agent", "agent"} {
		h := newStallHarness(t, StallWatchdogEnforce)
		h.wf.WorkflowName = name
		assert.False(t, h.stallAfter(t, 48*time.Hour).StallTerminated, name)
		assert.Empty(t, h.runs.calls, name)
	}
}

func TestStallWatchdog_HoldsWhileTheFleetIsDown(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	h.temporal.setPollersActive(false)
	assert.False(t, h.stallAfter(t, 3*time.Hour).StallTerminated)
	assert.Empty(t, h.runs.calls)

	h.temporal.setPollersActive(true)
	h.now = h.now.Add(time.Minute)
	assert.True(t, h.pass(t).StallTerminated, "the streak kept counting while the fleet was down")
}

// A run parked on a timer (a capacity-slot backoff, a machine recheck) shows
// no pending work between fires, but each fire is a history event: progress,
// so the run is never stalled however long the wait.
func TestStallWatchdog_TimerParkedRunMakesProgress(t *testing.T) {
	h := newStallHarness(t, StallWatchdogEnforce)
	history := int64(42)
	for i := 0; i < 48; i++ {
		h.pass(t)
		h.now = h.now.Add(time.Hour)
		history += 2 // TimerStarted + TimerFired, then the next TimerStarted
		h.describe(stallRun, history)
	}
	assert.Empty(t, h.runs.calls)
	assert.Empty(t, h.records.rows)
}

// The record counts under the run id the stalled execution's own node
// observations carry (node_observations.run_id, forge.ai.run.id), never the
// workflow id: the root run's, kept across continue-as-new and a restart
// from a checkpoint, so each execution of one workflow is attributed to the
// run it belongs to.
func TestStallWatchdog_RecordIsUnderTheRunsOwnRunID(t *testing.T) {
	carried := &types.RunTelemetry{WorkflowName: "pr-fix", RunID: "run-1"}
	for _, tc := range []struct {
		name, temporalRun string
		input             wfruntime.WorkflowInput
		want              string
	}{
		{"a fresh run", "run-1", freshInput(), "run-1"},
		{"its continue-as-new successor", "run-2", wfruntime.WorkflowInput{
			ChatID: stallChat, WorkflowName: "pr-fix", Resume: &wfruntime.ResumeInput{NodeID: "fix"},
			ExecContext: &wfruntime.ExecutionContext{Thread: "main", Telemetry: carried},
		}, "run-1"},
		{"a restart from a checkpoint", "run-3", wfruntime.WorkflowInput{
			ChatID: stallChat, WorkflowName: "pr-fix", Resume: &wfruntime.ResumeInput{NodeID: "fix", RunID: "run-root"},
			ExecContext: &wfruntime.ExecutionContext{Thread: "main"},
		}, "run-root"},
		{"a restart from a checkpoint that recorded none", "run-5", wfruntime.WorkflowInput{
			ChatID: stallChat, WorkflowName: "pr-fix", Resume: &wfruntime.ResumeInput{},
			ExecContext: &wfruntime.ExecutionContext{Thread: "main"},
		}, "run-5"},
		{"a later fresh run of the same workflow", "run-4", wfruntime.WorkflowInput{
			ChatID: stallChat, WorkflowName: "pr-fix",
			ExecContext: &wfruntime.ExecutionContext{Thread: "main", Telemetry: carried},
		}, "run-4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, wfruntime.RootRunID(tc.input, tc.temporalRun), "the rule the run applies")
			h := newStallHarness(t, StallWatchdogReport)
			h.describe(tc.temporalRun, 42)
			h.started(t, tc.temporalRun, tc.input)
			h.stallAfter(t, 3*time.Hour)
			require.Len(t, h.records.rows, 1)
			row := h.records.rows[0]
			assert.Equal(t, tc.want, row.RunID)
			assert.NotEqual(t, stallWF, row.RunID, "never the workflow id")
			assert.Equal(t, stallWF, row.WorkflowID)
			assert.Contains(t, row.Attributes, `"reliant.stall.temporal_run_id":"`+tc.temporalRun+`"`)
		})
	}
}

// A run id that cannot be learned is not guessed: the record names no run,
// which the store counts under the workflow.
func TestStallWatchdog_RecordNamesNoRunWhenTheStartCannotBeRead(t *testing.T) {
	h := newStallHarness(t, StallWatchdogReport)
	h.temporal.mu.Lock()
	h.temporal.historyByRun[stallRun] = nil
	h.temporal.mu.Unlock()
	h.stallAfter(t, 3*time.Hour)
	require.Len(t, h.records.rows, 1)
	assert.Empty(t, h.records.rows[0].RunID)
}

// Against the real store: the record shares the cap of the run it is about,
// and executions of the same workflow that are other runs do not share it.
func TestStallWatchdog_RecordSharesItsRunsObservationCap(t *testing.T) {
	env, ctx := storetest.New(t), context.Background()
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateChat(ctx, env.ORM, &store.Chat{
		ID: stallChat, Title: stallChat, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}))
	seed := func(runID string, n int) {
		t.Helper()
		_, err := env.SQL.ExecContext(ctx, `INSERT INTO node_observations (id, chat_id, workflow_id, run_id, step_id, node_path, source)
			SELECT $3 || '-' || g, $1, $2, $3, 'fix', $3 || '/fix-' || g, 'workflow' FROM generate_series(1, $4) g`,
			stallChat, stallWF, runID, n)
		require.NoError(t, err)
	}
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM node_observations WHERE `+where, args...).Scan(&n))
		return n
	}
	h := newStallHarnessRecordingTo(t, StallWatchdogReport, env.Repo)

	// A restart from a checkpoint of run-root: the record joins run-root's
	// own observations.
	seed("run-root", 1)
	h.describe("run-3", 42)
	h.started(t, "run-3", wfruntime.WorkflowInput{
		ChatID: stallChat, WorkflowName: "pr-fix", Resume: &wfruntime.ResumeInput{NodeID: "fix", RunID: "run-root"},
		ExecContext: &wfruntime.ExecutionContext{Thread: "main"},
	})
	h.stallAfter(t, 3*time.Hour)
	assert.Equal(t, 1, count(`source = 'reconciler' AND run_id = 'run-root'`))
	assert.Equal(t, 2, count(`run_id = 'run-root'`))

	// run-1 holds its cap. Its continue-as-new successor stalls: the
	// record counts against run-1's cap, so it is not stored.
	seed("run-1", wfobserve.MaxRowsPerRun)
	h.describe("run-2", 7)
	h.started(t, "run-2", wfruntime.WorkflowInput{
		ChatID: stallChat, WorkflowName: "pr-fix", Resume: &wfruntime.ResumeInput{NodeID: "fix"},
		ExecContext: &wfruntime.ExecutionContext{Thread: "main", Telemetry: &types.RunTelemetry{RunID: "run-1"}},
	})
	h.now = h.now.Add(time.Minute)
	h.stallAfter(t, 3*time.Hour)
	assert.Equal(t, wfobserve.MaxRowsPerRun, count(`run_id = 'run-1'`))
	assert.Zero(t, count(`source = 'reconciler' AND attributes->>'reliant.stall.temporal_run_id' = 'run-2'`))

	// A later fresh run of the same workflow is a run of its own, with a cap
	// of its own: its record is stored.
	h.describe("run-4", 3)
	h.started(t, "run-4", freshInput())
	h.now = h.now.Add(time.Minute)
	h.stallAfter(t, 3*time.Hour)
	assert.Equal(t, 1, count(`source = 'reconciler' AND run_id = 'run-4'`))
	assert.Zero(t, count(`source = 'reconciler' AND run_id = $1`, stallWF), "no record is counted under the workflow id")
}
