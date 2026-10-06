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
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	gateSlackPost = "slack__message_post"
	// gateIssueGet is a read-only GitHub action: it changes nothing.
	gateIssueGet = "github__issue_get"
	gateView     = "view"
)

// gateRecorder captures what the gate asked and what the batch was handed.
type gateRecorder struct {
	mu               sync.Mutex
	approvalCreates  []map[string]interface{}
	approvalResolves []map[string]interface{}
	executeInputs    []*reliantv1.ExecuteToolsArgs
	// executedAfterAnswer records, per ExecuteTools run, whether the
	// person's answer had been delivered when it started.
	executedAfterAnswer []bool
	answered            bool
	// elapsed is the workflow's run time on the test clock.
	elapsed time.Duration
}

func (r *gateRecorder) refused() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.executeInputs) == 0 {
		return nil
	}
	return r.executeInputs[len(r.executeInputs)-1].GetRefusedToolCalls()
}

type gateScenario struct {
	calls []*reliantv1.ToolCallMsg
	caps  *reliantv1.ToolCapabilities
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
			if reason := input.Node.GetExecuteTools().GetRefusedToolCalls()[tc.GetId()]; reason != "" {
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
	assert.EqualValues(t, 42, out["thread_token_count"], "the batch's own output passes through the gate untouched")
	return rec, nil
}

// capsFor is a turn's capability set as call_llm records it
// (tools.Capabilities.Proto): what it offered, and which of those ask first.
// That attended runs list the mutating actions and unattended runs list none
// is pinned in internal/llm/tools; here it is the data the gate reads.
func capsFor(offered []string, approvalRequired ...string) *reliantv1.ToolCapabilities {
	return &reliantv1.ToolCapabilities{
		Permission:       "mutating",
		Offered:          offered,
		ApprovalRequired: approvalRequired,
	}
}

func slackCall(id string) *reliantv1.ToolCallMsg {
	input, _ := json.Marshal(map[string]interface{}{"channel": "#general", "text": "Ship it"})
	return &reliantv1.ToolCallMsg{Id: id, Name: gateSlackPost, Input: string(input)}
}

func TestActionApproval_AttendedMutatingCallWaitsAndRunsOnAllow(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls:        []*reliantv1.ToolCallMsg{slackCall("tc-slack"), {Id: "tc-view", Name: gateView, Input: `{"file_path":"a"}`}},
		caps:         capsFor([]string{gateSlackPost, gateView}, gateSlackPost),
		createOutput: map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
		signal:       map[string]interface{}{"status": "approved", "action_taken": "allow_once"},
		signalAfter:  10 * time.Minute,
	})

	require.Len(t, rec.approvalCreates, 1, "one card for the one mutating call")
	create := rec.approvalCreates[0]
	assert.Equal(t, gateSlackPost, create["tool_name"])
	assert.Equal(t, "tc-slack", create["tool_call_id"])
	assert.JSONEq(t, `{"channel":"#general","text":"Ship it"}`, create["tool_input"].(string))

	require.Len(t, rec.executeInputs, 1, "the whole batch runs once, after the answer")
	assert.Empty(t, rec.refused(), "an approved call is not refused")
	assert.True(t, rec.executedAfterAnswer[0], "nothing ran before the person answered")
}

func TestActionApproval_DenyRefusesTheCall(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls:        []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:         capsFor([]string{gateSlackPost}, gateSlackPost),
		createOutput: map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
		signal:       map[string]interface{}{"status": "denied", "denial_reason": "no"},
		signalAfter:  time.Minute,
	})
	require.Len(t, rec.executeInputs, 1)
	assert.Equal(t,
		"The user did not approve slack__message_post, so it was not run. Do not attempt it another way.",
		rec.refused()["tc-slack"])
}

func TestActionApproval_UnansweredTimesOutAndRefuses(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls:        []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:         capsFor([]string{gateSlackPost}, gateSlackPost),
		createOutput: map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
	})
	require.Len(t, rec.approvalResolves, 1, "the row is resolved as timed out")
	assert.Contains(t, rec.refused()["tc-slack"], "did not approve slack__message_post in time")
}

func TestActionApproval_AlwaysAllowedRunsWithoutWaiting(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls: []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:  capsFor([]string{gateSlackPost}, gateSlackPost),
		// ApprovalCreate found the user's "always allow" and made no row.
		createOutput: map[string]interface{}{"already_resolved": true, "status": "approved", "action_taken": "always_allow"},
	})
	require.Len(t, rec.executeInputs, 1)
	assert.Empty(t, rec.refused())
	assert.Less(t, rec.elapsed, time.Minute, "no wait for an answer")
}

func TestActionApproval_ReadOnlyAndOtherToolsNeverAsk(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls: []*reliantv1.ToolCallMsg{
			{Id: "tc-get", Name: gateIssueGet, Input: `{"owner":"acme","repo":"api","issue_number":1}`},
			{Id: "tc-view", Name: gateView, Input: `{"file_path":"a"}`},
		},
		caps: capsFor([]string{gateIssueGet, gateView}),
	})
	assert.Empty(t, rec.approvalCreates)
	require.Len(t, rec.executeInputs, 1)
}

// An unattended run that names the tool keeps it (the author's approval) and
// is never asked: nobody is there to answer.
func TestActionApproval_UnattendedOptedInRunDoesNotAsk(t *testing.T) {
	t.Parallel()
	caps := capsFor([]string{gateSlackPost})
	caps.Unattended = true
	caps.UnattendedOptIn = []string{gateSlackPost}
	rec := runGate(t, gateScenario{calls: []*reliantv1.ToolCallMsg{slackCall("tc-slack")}, caps: caps})
	assert.Empty(t, rec.approvalCreates)
	require.Len(t, rec.executeInputs, 1)
	assert.Empty(t, rec.refused())
}

// A call the turn did not offer is not asked about; the activity refuses it.
func TestActionApproval_CallNotOfferedIsNotAskedAbout(t *testing.T) {
	t.Parallel()
	rec := runGate(t, gateScenario{
		calls: []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:  capsFor([]string{gateView}),
	})
	assert.Empty(t, rec.approvalCreates)
}

// An interrupt while the card is up ends the wait: the batch is cancelled like
// any interrupted batch (nothing runs, nothing is recorded FAILED), and the
// card is closed rather than left asking about a call that will never run.
func TestActionApproval_InterruptEndsTheWaitAndClosesTheCard(t *testing.T) {
	t.Parallel()
	rec, err := runGateWorkflow(t, gateScenario{
		calls:          []*reliantv1.ToolCallMsg{slackCall("tc-slack")},
		caps:           capsFor([]string{gateSlackPost}, gateSlackPost),
		createOutput:   map[string]interface{}{"approval_id": "appr-1", "already_resolved": false},
		interruptAfter: 5 * time.Minute,
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
	assert.True(t, closed, "the card is closed as interrupted: %v", rec.approvalResolves)
}

// The meta envelope a call may carry is unwrapped, so the card shows the
// arguments the model wrote.
func TestUnwrapToolMetaInput(t *testing.T) {
	t.Parallel()
	wrapped, _ := json.Marshal(map[string]interface{}{"input": `{"to":["a@b.c"]}`, "__reliant_tool_meta__": map[string]interface{}{}})
	assert.Equal(t, `{"to":["a@b.c"]}`, unwrapToolMetaInput(string(wrapped)))
	assert.Equal(t, `{"to":["a@b.c"]}`, unwrapToolMetaInput(`{"to":["a@b.c"]}`))
}

// Old call_llm outputs carry no approval_required, so a recorded batch replays
// without the gate's commands.
func TestActionApproval_CapabilitiesWithoutTheFieldDoNotGate(t *testing.T) {
	t.Parallel()
	caps := capsFor([]string{gateSlackPost})
	encoded, err := protojson.Marshal(caps)
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	fromHistory := capabilitiesFromNodeOutput(map[string]interface{}{"capabilities": decoded})
	assert.Empty(t, actionApprovalCalls([]*reliantv1.ToolCallMsg{slackCall("tc")}, fromHistory))
}
