// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Sub-agent failure isolation, through the REAL DynamicWorkflow wiring.
//
// Every spawned sub-agent runs as a goroutine inside its parent's one Temporal
// execution, and they all share one pause coordinator. A sub-agent whose step
// exhausted its retries used to self-pause that coordinator: every thread in
// the chat — the orchestrator and every healthy sibling — stopped, and their
// in-flight activities were cancelled, until a human resumed the chat. On the
// 2026-10-10 incident one implementer's provider stalls parked an
// orchestrator and its other sub-agents for 70 minutes; earlier the same night
// a sub-agent's permanent copilot 400 cancelled the main thread's healthy
// codex call mid-stream.
//
// The contract pinned here: a sub-agent's exhausted step ends THAT agent,
// reports status="failed" to its parent the way a completion is reported,
// and touches nothing else. The parent decides what happens next. The root
// thread's own exhaustion keeps pausing the run, because there is no parent
// above it to decide.

// providerBadRequest is the shape of the 2026-10-10 copilot failure: a
// provider rejecting the request outright. Non-retryable, so the ladder ends
// on the first attempt without waiting out backoff timers — which matters to
// the test environment, whose clock cannot advance while another stub is held
// in flight.
func providerBadRequest() error {
	return temporal.NewNonRetryableApplicationError(
		`POST "https://api.individual.githubcopilot.com/v1/messages": 400 Bad Request`,
		"ProviderError", nil)
}

// subAgentFailureEnv wires DynamicWorkflow's activity surface with CallLLM
// stubs that tell the parent thread from each spawned child, and records
// everything a user (or the parent) would observe.
type subAgentFailureEnv struct {
	t   *testing.T
	env *testsuite.TestWorkflowEnvironment

	parentThread string

	// parentTurn returns the parent's tool calls for its Nth turn (1-based),
	// or the error that turn fails with.
	parentTurn func(turn int) ([]map[string]interface{}, error)
	// childTurn answers a child's Nth turn, keyed by the spawn tool call that
	// currently owns the child's thread (a resumption takes it over).
	childTurn func(toolCallID string, turn int) (map[string]interface{}, error)
	// hold, when it returns a non-empty milestone, parks that CallLLM stub
	// until the milestone is reached. toolCallID is "" for the parent.
	hold func(toolCallID string, turn int) string

	mu                sync.Mutex
	threadForToolCall map[string]string
	toolCallForThread map[string]string
	turnsByThread     map[string]int
	reports           []map[string]interface{}
	statuses          []map[string]interface{}
	toolStatuses      []map[string]interface{}
	workflowErrors    []map[string]interface{}
	// cancelledCalls records CallLLM stubs whose activity context was
	// cancelled while they were held in flight, as "<toolCallID>#<turn>"
	// ("parent#N" for the parent).
	cancelledCalls []string

	milestones map[string]chan struct{}
	testDone   chan struct{}
}

const (
	milestoneFailureReported = "failure-reported"
)

func milestoneStarted(who string, turn int) string {
	return fmt.Sprintf("%s-turn-%d-started", who, turn)
}

func newSubAgentFailureEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, parentThread string) *subAgentFailureEnv {
	t.Helper()
	e := &subAgentFailureEnv{
		t:                 t,
		env:               env,
		parentThread:      parentThread,
		threadForToolCall: map[string]string{},
		toolCallForThread: map[string]string{},
		turnsByThread:     map[string]int{},
		milestones:        map[string]chan struct{}{},
		testDone:          make(chan struct{}),
		parentTurn:        func(int) ([]map[string]interface{}, error) { return nil, nil },
		childTurn: func(string, int) (map[string]interface{}, error) {
			return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
		},
		hold: func(string, int) string { return "" },
	}
	t.Cleanup(func() { close(e.testDone) })

	wf, err := wfyaml.ParseWorkflow([]byte(spawnBackgroundE2EYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	register := func(name string, fn interface{}) {
		env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	ok := func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}

	register("ActivityLoadWorkflow", func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	})
	for _, name := range []string{"WorkflowCheckpoint", "ThreadStatus", "Cleanup"} {
		register(name, ok)
	}
	register("EmitStreamFinalized", func(_ context.Context, _ types.EmitStreamFinalizedInput) (types.EmitStreamFinalizedOutput, error) {
		return types.EmitStreamFinalizedOutput{}, nil
	})
	register("WorkflowError", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		e.workflowErrors = append(e.workflowErrors, input)
		e.mu.Unlock()
		return map[string]interface{}{"success": true}, nil
	})
	register("WorkflowStatus", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.statuses = append(e.statuses, input)
		// The spawn path reports which tool call owns a child thread on its
		// "started" status. Thread ids are generated, so this pairing is the
		// only way to address a NAMED spawn — and a resumption re-pairs the
		// thread with the tool call that resumed it.
		toolCallID, _ := input["spawned_by_tool_call_id"].(string)
		thread, _ := input["thread"].(string)
		if toolCallID != "" && thread != "" && input["status"] == "started" {
			e.threadForToolCall[toolCallID] = thread
			e.toolCallForThread[thread] = toolCallID
		}
		return map[string]interface{}{"success": true}, nil
	})
	register("EmitToolCallStatus", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		e.toolStatuses = append(e.toolStatuses, input)
		e.mu.Unlock()
		return map[string]interface{}{"success": true}, nil
	})
	register("EmitThreadEvent", func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil })
	register("CreateWorkflowWithThread", func(_ context.Context, input map[string]interface{}) (interface{}, error) {
		threadID, _ := input["thread_id"].(string)
		return map[string]interface{}{"message_id": "msg-" + threadID}, nil
	})
	register("SaveMessage", func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"message_id": "msg-save"}, nil
	})
	register("FetchThreadResult", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"content": "agent finished", "is_error": false}, nil
	})
	register("DrainAgentMessages", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"count": 0, "has_messages": false}, nil
	})
	register("EnqueueAgentMessage", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		e.mu.Lock()
		e.reports = append(e.reports, input)
		failed := reportKind(input) == 4
		e.mu.Unlock()
		if failed {
			e.reach(milestoneFailureReported)
		}
		id, _ := input["tool_call_id"].(string)
		return map[string]interface{}{"id": "am-" + id}, nil
	})
	register("ValidateThreadOwnership", func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"valid": true}, nil
	})
	register("LoadPresetParams", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	})
	register("ExecuteTools", func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
		return map[string]interface{}{"tool_results": []interface{}{}}, nil
	})
	register("CallLLM", e.callLLM)
	return e
}

// milestone returns name's channel, creating it on first use. Callers hold e.mu.
func (e *subAgentFailureEnv) milestone(name string) chan struct{} {
	ch, ok := e.milestones[name]
	if !ok {
		ch = make(chan struct{})
		e.milestones[name] = ch
	}
	return ch
}

func (e *subAgentFailureEnv) reach(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := e.milestone(name)
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (e *subAgentFailureEnv) reached(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.milestone(name):
		return true
	default:
		return false
	}
}

// await parks a stub until name is reached. A workflow that never gets there
// fails the test on the environment's timeout; the stub is released by the
// test's cleanup rather than left blocked behind it.
func (e *subAgentFailureEnv) await(name string) {
	e.mu.Lock()
	ch := e.milestone(name)
	e.mu.Unlock()
	select {
	case <-ch:
	case <-e.testDone:
	}
}

func (e *subAgentFailureEnv) callLLM(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mu.Lock()
	e.turnsByThread[thread]++
	turn := e.turnsByThread[thread]
	isParent := thread == e.parentThread
	toolCallID := e.toolCallForThread[thread]
	e.mu.Unlock()

	who := "parent"
	if !isParent {
		who = toolCallID
	}
	e.reach(milestoneStarted(who, turn))
	if waitFor := e.hold(toolCallIDOrEmpty(isParent, toolCallID), turn); waitFor != "" {
		e.await(waitFor)
		// A held call that a pause cancelled is exactly the collateral damage
		// this suite exists to rule out.
		if ctx.Err() != nil {
			e.mu.Lock()
			e.cancelledCalls = append(e.cancelledCalls, fmt.Sprintf("%s#%d", who, turn))
			e.mu.Unlock()
		}
	}

	if !isParent {
		return e.childTurn(toolCallID, turn)
	}
	calls, err := e.parentTurn(turn)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"response_text": "working", "tool_calls": toolCallsJSON(e.t, calls)}, nil
}

func toolCallIDOrEmpty(isParent bool, toolCallID string) string {
	if isParent {
		return ""
	}
	return toolCallID
}

func toolCallsJSON(t *testing.T, calls []map[string]interface{}) []interface{} {
	if len(calls) == 0 {
		return nil
	}
	raw, err := json.Marshal(calls)
	require.NoError(t, err)
	var out []interface{}
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func reportKind(input map[string]interface{}) int {
	switch k := input["kind"].(type) {
	case float64:
		return int(k)
	case int32:
		return int(k)
	case int:
		return k
	case int64:
		return int(k)
	}
	return 0
}

func (e *subAgentFailureEnv) reportFor(toolCallID string) map[string]interface{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.reports {
		if r["tool_call_id"] == toolCallID {
			return r
		}
	}
	return nil
}

func (e *subAgentFailureEnv) toolStatusesFor(toolCallID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, st := range e.toolStatuses {
		if st["tool_call_id"] == toolCallID {
			if s, ok := st["status"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func (e *subAgentFailureEnv) pausedStatuses() []map[string]interface{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []map[string]interface{}
	for _, st := range e.statuses {
		if st["status"] == "paused" {
			out = append(out, st)
		}
	}
	return out
}

// rootWorkflowID is the workflow id the root run reported itself started under.
func (e *subAgentFailureEnv) rootWorkflowID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, st := range e.statuses {
		if st["status"] == "started" && st["spawned_by_tool_call_id"] == nil {
			id, _ := st["workflow_id"].(string)
			return id
		}
	}
	return ""
}

func (e *subAgentFailureEnv) threadOf(toolCallID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.threadForToolCall[toolCallID]
}

func (e *subAgentFailureEnv) turns(thread string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.turnsByThread[thread]
}

type SubAgentFailureIsolationSuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestSubAgentFailureIsolation(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(SubAgentFailureIsolationSuite))
}

// TestSubAgentExhaustion_FailsOnlyThatAgent is the incident: one sub-agent's
// step fails for good while its parent and a sibling each have a call IN
// FLIGHT. The failing agent must end with status failed and report it; the
// parent and the sibling must finish their calls uncancelled and keep going.
//
// Before the fix the failing agent self-paused the shared coordinator, which
// cancelled both in-flight calls and parked every thread until a human
// resumed the chat — here, forever, so the run never completes.
func (s *SubAgentFailureIsolationSuite) TestSubAgentExhaustion_FailsOnlyThatAgent() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)

	const chatID = "chat-subagent-fails-alone"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{
				spawnToolCall("tc-doomed", "implement the feature"),
				spawnToolCall("tc-healthy", "review the design"),
			}, nil
		}
		return nil, nil
	}
	e.childTurn = func(toolCallID string, turn int) (map[string]interface{}, error) {
		if toolCallID == "tc-doomed" {
			return nil, providerBadRequest()
		}
		// The healthy sibling takes two turns, so its SECOND is dispatched
		// after the failure: it kept working, not merely finished a call.
		if turn == 1 {
			return map[string]interface{}{"response_text": "reading", "tool_calls": toolCallsJSON(e.t, []map[string]interface{}{
				{"id": "healthy-read", "name": "read_file", "input": `{"path":"a.txt"}`},
			})}, nil
		}
		return map[string]interface{}{"response_text": "reviewed", "tool_calls": nil}, nil
	}
	// The failure must land while the parent's turn 2 and the sibling's
	// turn 1 are both in flight — that is when a run-wide pause cancels them.
	e.hold = func(toolCallID string, turn int) string {
		switch {
		case toolCallID == "tc-doomed" && turn == 1:
			if !e.reached(milestoneStarted("parent", 2)) || !e.reached(milestoneStarted("tc-healthy", 1)) {
				return "both-in-flight"
			}
		case toolCallID == "" && turn == 2, toolCallID == "tc-healthy" && turn == 1:
			return milestoneFailureReported
		}
		return ""
	}
	go func() {
		e.await(milestoneStarted("parent", 2))
		e.await(milestoneStarted("tc-healthy", 1))
		e.reach("both-in-flight")
	}()

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted(),
		"a sub-agent's failure must not stop the run: the run never completed, which is the "+
			"whole chat sitting paused behind one agent")
	s.Require().NoError(env.GetWorkflowError())

	s.Empty(e.pausedStatuses(), "nothing may be marked paused for a sub-agent's failure")
	s.Empty(e.cancelledCalls, "the parent's and the sibling's in-flight calls must not be cancelled")

	doomedThread := e.threadOf("tc-doomed")
	s.Require().NotEmpty(doomedThread)

	failed := e.reportFor("tc-doomed")
	s.Require().NotNil(failed, "the parent must be told its sub-agent failed")
	s.Equal(4, reportKind(failed), "the report must carry status=failed (AgentMessageKindFailed)")
	body, _ := failed["body"].(string)
	s.Contains(body, "400 Bad Request", "the report names the error")
	s.Contains(body, fmt.Sprintf(`spawn(agent_id="%s"`, doomedThread), "the report hands the parent a resume handle")
	s.NotContains(body, "scheduledEventID", "Temporal's bookkeeping does not reach the parent")
	s.Equal([]string{"backgrounded", "failed"}, e.toolStatusesFor("tc-doomed"),
		"the failed agent's tool call ends failed")
	s.Equal(1, e.turns(doomedThread), "a failed agent is not retried behind the parent's back")

	healthy := e.reportFor("tc-healthy")
	s.Require().NotNil(healthy, "the sibling must finish and report")
	s.Equal(2, reportKind(healthy))
	s.Equal(2, e.turns(e.threadOf("tc-healthy")),
		"the sibling takes exactly its two turns: none cancelled and re-dispatched, none skipped")
	s.Contains(e.toolStatusesFor("tc-healthy"), "completed")

	s.GreaterOrEqual(e.turns(e.parentThread), 3,
		"the parent keeps running: it reacts to the failure on a later turn")

	// The failing agent's own thread still explains what happened, scoped to
	// that thread — never a chat-wide error claiming the workflow paused.
	e.mu.Lock()
	defer e.mu.Unlock()
	s.Require().Len(e.workflowErrors, 1)
	werr := e.workflowErrors[0]
	s.Equal(doomedThread, werr["thread"], "the error belongs to the failed agent's thread")
	summary, _ := werr["error_summary"].(string)
	s.NotContains(summary, "paused", "the run did not pause, so the error must not say it did")
}

// TestRootExhaustion_StillPauses pins the half of the contract that does NOT
// change: the root thread has no parent to report to, so its exhausted step
// keeps today's pause-and-resume behaviour.
func (s *SubAgentFailureIsolationSuite) TestRootExhaustion_StillPauses() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)

	const chatID = "chat-root-still-pauses"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	var mu sync.Mutex
	resumed := false
	// The root's own call fails until the user resumes.
	e.parentTurn = func(int) ([]map[string]interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		if !resumed {
			return nil, providerBadRequest()
		}
		return nil, nil
	}

	env.RegisterDelayedCallback(func() {
		mu.Lock()
		resumed = true
		mu.Unlock()
		env.SignalWorkflow("signal.resume", nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	paused := e.pausedStatuses()
	s.Require().Len(paused, 1, "the root's exhausted step must still pause the run")
	s.Equal(e.rootWorkflowID(), paused[0]["workflow_id"], "and it is the root's own workflow that pauses")
	s.Equal(2, e.turns(e.parentThread), "after resume the root re-dispatches the step that failed")

	e.mu.Lock()
	defer e.mu.Unlock()
	s.Require().Len(e.workflowErrors, 1)
	s.Equal(e.parentThread, e.workflowErrors[0]["thread"])
}

// TestFailedSubAgent_ResumesViaSpawnAgentID: the resume handle the parent is
// given works. The agent fails through a full retryable ladder, the parent
// resumes it with spawn(agent_id=...) on its next turn, and the resumed run
// continues on the SAME thread and reports a completion.
func (s *SubAgentFailureIsolationSuite) TestFailedSubAgent_ResumesViaSpawnAgentID() {
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)

	const chatID = "chat-resume-failed-subagent"
	input := spawnE2EWorkflowInput(chatID)
	e := newSubAgentFailureEnv(s.T(), env, input.ExecContext.Thread)

	resumeIssued := false
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tc-first", "implement the feature")}, nil
		}
		if !resumeIssued && e.reached(milestoneFailureReported) {
			resumeIssued = true
			resume, _ := json.Marshal(map[string]interface{}{
				"agent_id": e.threadOf("tc-first"),
				"prompt":   "The provider recovered; continue where you stopped.",
				"preset":   "general",
			})
			return []map[string]interface{}{{"id": "tc-resume", "name": "spawn", "input": string(resume)}}, nil
		}
		return nil, nil
	}
	e.childTurn = func(toolCallID string, turn int) (map[string]interface{}, error) {
		if toolCallID == "tc-first" {
			// Retryable: this one spends the whole ladder, like the
			// incident's five provider stalls.
			return nil, fmt.Errorf("provider stream stalled: no content for 120s")
		}
		return map[string]interface{}{"response_text": "finished after resume", "tool_calls": nil}, nil
	}

	env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(env.IsWorkflowCompleted(), "the run must not park behind the failed agent")
	s.Require().NoError(env.GetWorkflowError())
	s.Empty(e.pausedStatuses())

	thread := e.threadOf("tc-first")
	s.Require().NotEmpty(thread)
	s.Equal(thread, e.threadOf("tc-resume"), "spawn(agent_id) resumes the failed agent's own thread")

	failed := e.reportFor("tc-first")
	s.Require().NotNil(failed)
	s.Equal(4, reportKind(failed))
	s.Equal([]string{"backgrounded", "failed"}, e.toolStatusesFor("tc-first"))

	resumed := e.reportFor("tc-resume")
	s.Require().NotNil(resumed, "the resumed agent reports like any other")
	s.Equal(2, reportKind(resumed), "and it completed")
	s.Contains(e.toolStatusesFor("tc-resume"), "completed")
	s.Equal(stepActivityMaxAttempts, int32(e.turns(thread)-1),
		"the first run spent exactly one retry ladder; the resumption took one turn")
}
