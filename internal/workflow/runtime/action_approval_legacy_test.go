// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	gateSlackPost = "slack__message_post"
	gateView      = "view"
)

// gateRecorder captures what a batch asked and what it was handed.
type gateRecorder struct {
	mu               sync.Mutex
	approvalCreates  []map[string]interface{}
	approvalResolves []map[string]interface{}
	executeInputs    []*reliantv1.ExecuteToolsArgs
	// executedAfterAnswer records, per ExecuteTools run, whether the
	// person's answer had been delivered when it started.
	executedAfterAnswer []bool
	answered            bool
	// versionConsulted is whether the batch asked GetVersion about the
	// removal at all.
	versionConsulted bool
	// elapsed is the workflow's run time on the test clock.
	elapsed time.Duration
}

func (r *gateRecorder) refused() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.executeInputs) == 0 {
		return nil
	}
	return r.executeInputs[len(r.executeInputs)-1].GetRefusedToolCalls() //nolint:staticcheck // the legacy gate's output
}

type gateScenario struct {
	calls []*reliantv1.ToolCallMsg
	caps  *reliantv1.ToolCapabilities
	// recordedBeforeRemoval replays the batch the way a run recorded before
	// the prompt was removed does: GetVersion answers DefaultVersion.
	recordedBeforeRemoval bool
	// createOutput is what ApprovalCreate answers.
	createOutput map[string]interface{}
	// signal, when set, is delivered on signal.approval.<approval_id> after
	// signalAfter.
	signal      map[string]interface{}
	signalAfter time.Duration
	// interruptAfter, when set, cancels the batch's context after that long,
	// the way an interrupt or pause does.
	interruptAfter time.Duration
}

// runGate drives executeToolsWithSpawnSupport for one batch through the
// Temporal test environment and returns what it recorded.
func runGate(t *testing.T, sc gateScenario) *gateRecorder {
	t.Helper()
	rec, err := runGateWorkflow(t, sc)
	require.NoError(t, err)
	return rec
}

// runGateWorkflow is runGate without requiring the batch to succeed.
func runGateWorkflow(t *testing.T, sc gateScenario) (*gateRecorder, error) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := &gateRecorder{}

	version := workflow.Version(1)
	if sc.recordedBeforeRemoval {
		version = workflow.DefaultVersion
	}
	env.OnGetVersion(actionApprovalRemovedChangeID, workflow.DefaultVersion, 1).
		Run(func(mock.Arguments) {
			rec.mu.Lock()
			rec.versionConsulted = true
			rec.mu.Unlock()
		}).
		Return(version)

	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		rec.mu.Lock()
		rec.approvalCreates = append(rec.approvalCreates, input)
		rec.mu.Unlock()
		return sc.createOutput, nil
	}, activity.RegisterOptions{Name: "ApprovalCreate"})
	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		rec.mu.Lock()
		rec.approvalResolves = append(rec.approvalResolves, input)
		rec.mu.Unlock()
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "ApprovalResolve"})
	env.RegisterActivityWithOptions(func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
		rec.mu.Lock()
		rec.executeInputs = append(rec.executeInputs, input.Node.GetExecuteTools())
		rec.executedAfterAnswer = append(rec.executedAfterAnswer, rec.answered)
		rec.mu.Unlock()
		var results []interface{}
		for _, tc := range input.Node.GetExecuteTools().GetResolvedToolCalls() {
			if reason := input.Node.GetExecuteTools().GetRefusedToolCalls()[tc.GetId()]; reason != "" { //nolint:staticcheck // the legacy gate's output
				results = append(results, map[string]interface{}{"tool_call_id": tc.GetId(), "content": reason, "is_error": true})
				continue
			}
			results = append(results, map[string]interface{}{"tool_call_id": tc.GetId(), "content": "ok"})
		}
		return map[string]interface{}{
			"tool_results":       results,
			"thread_token_count": 42,
			"message":            map[string]interface{}{"role": "tool", "text": ""},
		}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})

	if sc.signal != nil {
		approvalID, _ := sc.createOutput["approval_id"].(string)
		env.RegisterDelayedCallback(func() {
			rec.mu.Lock()
			rec.answered = true
			rec.mu.Unlock()
			env.SignalWorkflow("signal.approval."+approvalID, sc.signal)
		}, sc.signalAfter)
	}

	start := env.Now()
	env.ExecuteWorkflow(func(ctx workflow.Context) (map[string]interface{}, error) {
		cancellable, interrupt := workflow.WithCancel(ctx)
		if sc.interruptAfter > 0 {
			workflow.Go(ctx, func(gCtx workflow.Context) {
				_ = workflow.Sleep(gCtx, sc.interruptAfter)
				interrupt()
			})
		}
		activityCtx := workflow.WithActivityOptions(cancellable, workflow.ActivityOptions{
			StartToCloseTimeout: time.Minute,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
		})
		evalNode := &reliantv1.Node{
			Id:   "tools",
			Type: "execute_tools",
			Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
				ResolvedToolCalls: sc.calls,
				Capabilities:      sc.caps,
			}},
		}
		rtx := types.RuntimeContext{ChatID: "chat-gate", WorkflowID: "wf-gate", StepID: "tools", Thread: "thread-gate"}
		future := executeToolsWithSpawnSupport(ctx, activityCtx, rtx, evalNode, map[string]interface{}{},
			&ChildWorkflowTracker{children: make(map[string]bool)},
			func(string) *PauseController { return nil })
		var out map[string]interface{}
		err := future.Get(ctx, &out)
		return out, err
	})
	require.True(t, env.IsWorkflowCompleted())
	rec.elapsed = env.Now().Sub(start)
	if err := env.GetWorkflowError(); err != nil {
		return rec, err
	}
	var out map[string]interface{}
	require.NoError(t, env.GetWorkflowResult(&out))
	assert.EqualValues(t, 42, out["thread_token_count"], "the batch's own output passes through untouched")
	return rec, nil
}

// recordedCaps is a capability set as a call_llm turn recorded before the
// prompt was removed: what it offered, and which of those asked first.
func recordedCaps(offered []string, approvalRequired ...string) *reliantv1.ToolCapabilities {
	return &reliantv1.ToolCapabilities{
		Permission:       "mutating",
		Offered:          offered,
		ApprovalRequired: approvalRequired, //nolint:staticcheck // what a turn recorded before the removal carries
	}
}

func slackCall(id string) *reliantv1.ToolCallMsg {
	input, _ := json.Marshal(map[string]interface{}{"channel": "#general", "text": "Ship it"})
	return &reliantv1.ToolCallMsg{Id: id, Name: gateSlackPost, Input: string(input)}
}

// The removal itself: a batch this code schedules runs a mutating
// integration action at once — even when its turn was recorded before the
// removal and still lists the call as needing approval.
func TestActionApprovalRemoved_MutatingCallRunsWithoutAsking(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls: []*reliantv1.ToolCallMsg{slackCall("tc-slack"), {Id: "tc-view", Name: gateView, Input: `{"file_path":"a"}`}},
		caps:  recordedCaps([]string{gateSlackPost, gateView}, gateSlackPost),
	})
	assert.True(t, rec.versionConsulted, "the batch differs from the recorded shape, so it is gated")
	assert.Empty(t, rec.approvalCreates, "nobody is asked")
	require.Len(t, rec.executeInputs, 1)
	assert.Len(t, rec.executeInputs[0].GetResolvedToolCalls(), 2, "the whole batch runs")
	assert.Empty(t, rec.refused())
	assert.Less(t, rec.elapsed, time.Minute, "nothing waited")
}

// A batch whose turn lists nothing needing approval — every turn recorded
// since the removal — never consults the gate, so its run records no marker.
func TestActionApprovalRemoved_OtherBatchesNeverConsultTheGate(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls: []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:  recordedCaps([]string{gateSlackPost}),
	})
	assert.False(t, rec.versionConsulted)
	assert.Empty(t, rec.approvalCreates)
	require.Len(t, rec.executeInputs, 1)
}

// A run recorded before the removal keeps the prompt it recorded: it waits
// for the answer, then runs the batch.
func TestActionApprovalLegacy_RecordedRunWaitsAndRunsOnAllow(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls:                 []*reliantv1.ToolCallMsg{slackCall("tc-slack"), {Id: "tc-view", Name: gateView, Input: `{"file_path":"a"}`}},
		caps:                  recordedCaps([]string{gateSlackPost, gateView}, gateSlackPost),
		recordedBeforeRemoval: true,
		createOutput:          map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
		signal:                map[string]interface{}{"status": "approved", "action_taken": "allow_once"},
		signalAfter:           10 * time.Minute,
	})

	require.Len(t, rec.approvalCreates, 1, "one approval for the one gated call")
	assert.Equal(t, "Run slack__message_post?", rec.approvalCreates[0]["title"])
	require.Len(t, rec.executeInputs, 1, "the whole batch runs once, after the answer")
	assert.Empty(t, rec.refused(), "an approved call is not refused")
	assert.True(t, rec.executedAfterAnswer[0], "nothing ran before the person answered")
}

func TestActionApprovalLegacy_DenyRefusesTheCall(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls:                 []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:                  recordedCaps([]string{gateSlackPost}, gateSlackPost),
		recordedBeforeRemoval: true,
		createOutput:          map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
		signal:                map[string]interface{}{"status": "denied", "denial_reason": "no"},
		signalAfter:           time.Minute,
	})
	require.Len(t, rec.executeInputs, 1)
	assert.Equal(t,
		"The user did not approve slack__message_post, so it was not run. Do not attempt it another way.",
		rec.refused()["tc-slack"])
}

func TestActionApprovalLegacy_UnansweredTimesOutAndRefuses(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls:                 []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:                  recordedCaps([]string{gateSlackPost}, gateSlackPost),
		recordedBeforeRemoval: true,
		createOutput:          map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
	})
	require.Len(t, rec.approvalResolves, 1, "the row is resolved as timed out")
	assert.Contains(t, rec.refused()["tc-slack"], "did not approve slack__message_post in time")
}

// An interrupt while the approval is up ends the wait: the batch is cancelled
// like any interrupted batch, and the approval is closed rather than left
// asking about a call that will never run.
func TestActionApprovalLegacy_InterruptEndsTheWaitAndClosesTheApproval(t *testing.T) {
	t.Parallel()
	rec, err := runGateWorkflow(t, gateScenario{
		calls:                 []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:                  recordedCaps([]string{gateSlackPost}, gateSlackPost),
		recordedBeforeRemoval: true,
		createOutput:          map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
		interruptAfter:        5 * time.Minute,
	})
	require.Error(t, err, "the interrupted batch is cancelled")
	assert.True(t, temporal.IsCanceledError(err), "cancelled, not failed: %v", err)
	assert.Empty(t, rec.executeInputs, "nothing ran")
	assert.Less(t, rec.elapsed, time.Hour, "the wait ended at the interrupt, not at the approval timeout")
	var closed bool
	for _, resolve := range rec.approvalResolves {
		if resolve["approval_id"] == "appr-1" && resolve["status"] == "interrupted" {
			closed = true
		}
	}
	assert.True(t, closed, "the approval is closed as interrupted: %v", rec.approvalResolves)
}

// The legacy branch reads approval_required from the call_llm output a run
// recorded, so the field must keep decoding until the branch is retired —
// reserving it first would make every such run take the new path on replay.
func TestActionApprovalLegacy_RecordedApprovalRequiredStillDecodes(t *testing.T) {
	t.Parallel()
	encoded, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(recordedCaps([]string{gateSlackPost}, gateSlackPost))
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"approval_required"`, "the recorded spelling")
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	fromHistory := capabilitiesFromNodeOutput(map[string]interface{}{"capabilities": decoded})
	gated := legacyActionApprovalCalls([]*reliantv1.ToolCallMsg{slackCall("tc")}, fromHistory)
	require.Len(t, gated, 1)
	assert.Equal(t, "tc", gated[0].GetId())
}
