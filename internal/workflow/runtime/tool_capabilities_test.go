// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// ── withCapabilitiesApplied ──────────────────────────────────────────────

func spawnCall(id, preset string) *reliantv1.ToolCallMsg {
	return &reliantv1.ToolCallMsg{Id: id, Name: spawnToolName, Input: `{"preset":"` + preset + `","prompt":"go"}`}
}

func TestWithCapabilitiesApplied_RoutesWhatTheSetDoesNotAllow(t *testing.T) {
	t.Parallel()

	split := protoToolCallSplit{
		regularToolCalls: []*reliantv1.ToolCallMsg{{Id: "view", Name: "view"}},
		spawnToolCalls:   []*reliantv1.ToolCallMsg{spawnCall("ok", "general"), spawnCall("bad", "planner")},
		askUserToolCalls: []*reliantv1.ToolCallMsg{{Id: "ask", Name: askUserToolName}},
	}

	t.Run("an offered spawn with an offered preset is dispatched", func(t *testing.T) {
		t.Parallel()
		caps := &reliantv1.ToolCapabilities{Permission: "mutating", Offered: []string{spawnToolName, askUserToolName}, SpawnPresets: []string{"general"}}
		applied := withCapabilitiesApplied(split, caps)
		assert.Equal(t, []string{"ok"}, callIDs(applied.spawnToolCalls))
		assert.Equal(t, []string{"view", "bad"}, callIDs(applied.regularToolCalls),
			"a preset the spawn tool was not offered with goes to the activity to be refused")
		assert.Equal(t, []string{"ask"}, callIDs(applied.askUserToolCalls))
	})

	t.Run("spawn and ask_user that were not offered go to the activity", func(t *testing.T) {
		t.Parallel()
		caps := &reliantv1.ToolCapabilities{Permission: "mutating", Offered: []string{"view"}}
		applied := withCapabilitiesApplied(split, caps)
		assert.Empty(t, applied.spawnToolCalls, "nothing is dispatched that was not offered")
		assert.Empty(t, applied.askUserToolCalls)
		assert.Equal(t, []string{"view", "ok", "bad", "ask"}, callIDs(applied.regularToolCalls))
	})

	// A batch whose call_llm recorded no set is split exactly as before —
	// which is what keeps every pre-existing history replaying the same
	// commands.
	t.Run("no recorded set leaves the split untouched", func(t *testing.T) {
		t.Parallel()
		for _, caps := range []*reliantv1.ToolCapabilities{nil, {}} {
			assert.Equal(t, split, withCapabilitiesApplied(split, caps))
		}
	})
}

func callIDs(calls []*reliantv1.ToolCallMsg) []string {
	ids := make([]string, 0, len(calls))
	for _, c := range calls {
		ids = append(ids, c.GetId())
	}
	return ids
}

// ── upstreamToolCapabilities ─────────────────────────────────────────────

func capsOutput(t *testing.T, caps *reliantv1.ToolCapabilities, toolCallIDs ...string) map[string]interface{} {
	t.Helper()
	encoded, err := protojson.Marshal(caps)
	require.NoError(t, err)
	var capsMap map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &capsMap))
	calls := make([]interface{}, 0, len(toolCallIDs))
	for _, id := range toolCallIDs {
		calls = append(calls, map[string]interface{}{"id": id, "name": "view"})
	}
	return map[string]interface{}{"capabilities": capsMap, "tool_calls": calls}
}

func TestUpstreamToolCapabilities_FindsTheProducingCallLLM(t *testing.T) {
	t.Parallel()

	wf, err := wfyaml.ParseWorkflow([]byte(`
name: two-llms
entry: [plan]
nodes:
  - id: plan
    type: call_llm
  - id: act
    type: call_llm
  - id: execute_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.act.tool_calls}}"
  - id: execute_filtered
    type: execute_tools
    args:
      tool_calls: "{{nodes.act.tool_calls.filter(t, t.name != 'x')}}"
`))
	require.NoError(t, err)
	nodes := map[string]*reliantv1.Node{}
	for _, n := range wf.GetNodes() {
		nodes[n.GetId()] = n
	}

	planCaps := &reliantv1.ToolCapabilities{Permission: "mutating", Offered: []string{"create_plan"}}
	actCaps := &reliantv1.ToolCapabilities{Permission: "orchestrator", Offered: []string{"view", "start_run"}}
	e := &StepExecutor{workflow: wf, nodeOutputs: map[string]interface{}{
		"plan": capsOutput(t, planCaps, "p1"),
		"act":  capsOutput(t, actCaps, "a1", "a2"),
	}}

	got := e.upstreamToolCapabilities(nodes["execute_tools"], []*reliantv1.ToolCallMsg{{Id: "a1"}})
	require.NotNil(t, got, "the node its tool_calls expression names")
	assert.Equal(t, actCaps.GetOffered(), got.GetOffered())

	got = e.upstreamToolCapabilities(nodes["execute_filtered"], []*reliantv1.ToolCallMsg{{Id: "a2"}})
	require.NotNil(t, got, "an expression that reshapes the list is matched by tool-call id")
	assert.Equal(t, "orchestrator", got.GetPermission())

	legacy := &StepExecutor{workflow: wf, nodeOutputs: map[string]interface{}{
		"act": map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"id": "a1"}},
			// What schema normalization makes of an output from before the set.
			"capabilities": map[string]interface{}{"offered": []interface{}{}, "permission": ""}},
	}}
	assert.Nil(t, legacy.upstreamToolCapabilities(nodes["execute_tools"], []*reliantv1.ToolCallMsg{{Id: "a1"}}),
		"an unresolved (zero-valued) set reads as no set")
}

// ── Per-thread grants ────────────────────────────────────────────────────

func TestToolGrants_ArePerThreadAndCumulative(t *testing.T) {
	t.Parallel()

	tracker := &ChildWorkflowTracker{}
	tracker.recordToolGrants("root", []string{"sourcegraph"})
	tracker.recordToolGrants("root", []string{"generate_image", "sourcegraph"})
	tracker.recordToolGrants("child", []string{"fetch"})

	assert.Equal(t, []string{"generate_image", "sourcegraph"}, tracker.toolGrantsFor("root"))
	assert.Equal(t, []string{"fetch"}, tracker.toolGrantsFor("child"),
		"a spawned child's grants are its own, and do not widen its parent")
	assert.Nil(t, tracker.toolGrantsFor("other"))

	successor := &ChildWorkflowTracker{}
	successor.seedToolGrants(tracker.toolGrantsHandoff())
	assert.Equal(t, tracker.toolGrantsFor("root"), successor.toolGrantsFor("root"))
	assert.Equal(t, tracker.toolGrantsFor("child"), successor.toolGrantsFor("child"))

	var none *ChildWorkflowTracker
	none.recordToolGrants("root", []string{"x"}) // nil-safe: tests build executors without one
	assert.Nil(t, none.toolGrantsFor("root"))
}

func continueAsNewGrantsWorkflow(ctx workflow.Context, input WorkflowInput) (*WorkflowResult, error) {
	tracker := &ChildWorkflowTracker{}
	tracker.recordToolGrants("thread-1", []string{"sourcegraph"})
	return nil, newContinueAsNewError(ctx, input, "agent_loop", 3, tracker, false)
}

type ToolGrantsContinueAsNewSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestContinueAsNew_CarriesToolGrants(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ToolGrantsContinueAsNewSuite))
}

// A tool an agent loaded before the handoff is still offered after it.
func (s *ToolGrantsContinueAsNewSuite) TestCarriesGrantsPerThread() {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(continueAsNewGrantsWorkflow)
	env.ExecuteWorkflow(continueAsNewGrantsWorkflow, WorkflowInput{
		ChatID:      "chat-1",
		ExecContext: &ExecutionContext{Thread: "thread-1"},
	})

	var contErr *workflow.ContinueAsNewError
	s.Require().True(errors.As(env.GetWorkflowError(), &contErr), "expected a ContinueAsNewError, got %v", env.GetWorkflowError())
	var carried WorkflowInput
	s.Require().NoError(converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &carried))
	s.Require().NotNil(carried.Resume)
	s.Equal(map[string][]string{"thread-1": {"sourcegraph"}}, carried.Resume.ToolGrants)
}

// ── The loop, end to end ─────────────────────────────────────────────────

// capsAgentEnv drives a full DynamicWorkflow agent loop (rootDrainAgentYAML)
// with scripted CallLLM / ExecuteTools activities, recording what the runtime
// hands each of them.
type capsAgentEnv struct {
	mu sync.Mutex
	// turns scripts each CallLLM turn's output; past the end, the turn ends
	// the run.
	turns []map[string]interface{}
	// executeGrants is what each ExecuteTools batch reports it granted.
	executeGrants [][]interface{}

	callLLMCount  int32
	executeCount  int32
	grantsSeen    [][]string
	capsSeen      []*reliantv1.ToolCapabilities
	toolCallsSeen [][]string
}

func newCapsAgentEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, e *capsAgentEnv) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(rootDrainAgentYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "EmitToolCallStatus", "Cleanup"} {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "EmitThreadEvent"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.ActivityInput) (interface{}, error) {
			return map[string]interface{}{"message_id": "msg-save"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.SetMessageWriter(messageWriterFunc(func(context.Context, types.RuntimeContext, *reliantv1.SaveMessageNodeArgs, string, int32) (*reliantv1.SaveMessageOutput, error) {
		return &reliantv1.SaveMessageOutput{MessageId: "msg-save"}, nil
	}))
	registerWrapped(env, registry, "CallLLM", func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
		idx := int(atomic.AddInt32(&e.callLLMCount, 1)) - 1
		e.mu.Lock()
		e.grantsSeen = append(e.grantsSeen, input.Runtime.ToolGrants)
		e.mu.Unlock()
		if idx < len(e.turns) {
			return e.turns[idx], nil
		}
		return map[string]interface{}{
			"response_text": "done",
			"message":       map[string]interface{}{"role": "assistant", "text": "done"},
		}, nil
	})
	registerWrapped(env, registry, "ExecuteTools", func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
		idx := int(atomic.AddInt32(&e.executeCount, 1)) - 1
		args := input.Node.GetExecuteTools()
		var names []string
		var results []interface{}
		for _, tc := range args.GetResolvedToolCalls() {
			names = append(names, tc.GetName())
			results = append(results, map[string]interface{}{"tool_call_id": tc.GetId(), "name": tc.GetName(), "content": "ok"})
		}
		e.mu.Lock()
		e.capsSeen = append(e.capsSeen, args.GetCapabilities())
		e.toolCallsSeen = append(e.toolCallsSeen, names)
		e.mu.Unlock()
		out := map[string]interface{}{"tool_results": results}
		if idx < len(e.executeGrants) && e.executeGrants[idx] != nil {
			out["granted_tools"] = e.executeGrants[idx]
		}
		return out, nil
	})
}

func scriptedTurn(caps map[string]interface{}, calls ...map[string]interface{}) map[string]interface{} {
	toolCalls := make([]interface{}, 0, len(calls))
	for _, c := range calls {
		toolCalls = append(toolCalls, c)
	}
	return map[string]interface{}{
		"response_text": "working",
		"tool_calls":    toolCalls,
		"capabilities":  caps,
		"message":       map[string]interface{}{"role": "assistant", "text": "working"},
	}
}

type ToolCapabilitiesLoopSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestToolCapabilitiesLoop(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ToolCapabilitiesLoopSuite))
}

// The set call_llm recorded reaches the execute_tools for that turn; what
// load_tool granted there reaches the thread's next call_llm, and every one
// after it.
func (s *ToolCapabilitiesLoopSuite) TestToolGrants_ThreadedFromExecuteToolsIntoNextCallLLM() {
	env := s.NewTestWorkflowEnvironment()
	turn0Caps := map[string]interface{}{"offered": []interface{}{"load_tool", "view"}, "permission": "mutating", "loadable_all": true}
	turn1Caps := map[string]interface{}{"offered": []interface{}{"load_tool", "sourcegraph", "view"}, "permission": "mutating", "loadable_all": true}
	e := &capsAgentEnv{
		turns: []map[string]interface{}{
			scriptedTurn(turn0Caps, map[string]interface{}{"id": "tc-load", "name": "load_tool", "input": `{"name":"sourcegraph"}`}),
			scriptedTurn(turn1Caps, map[string]interface{}{"id": "tc-search", "name": "sourcegraph", "input": `{"query":"x"}`}),
		},
		executeGrants: [][]interface{}{{"sourcegraph"}},
	}
	newCapsAgentEnv(s.T(), env, e)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput("chat-caps-grants", "wf-chat-caps-grants"))
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	s.Require().Equal(int32(3), atomic.LoadInt32(&e.callLLMCount))
	s.Require().Len(e.capsSeen, 2)
	s.Equal([]string{"load_tool", "view"}, e.capsSeen[0].GetOffered(), "turn 0's execute_tools got turn 0's set")
	s.Equal([]string{"load_tool", "sourcegraph", "view"}, e.capsSeen[1].GetOffered(), "turn 1's execute_tools got turn 1's set")

	s.Empty(e.grantsSeen[0], "nothing granted before the first turn")
	s.Equal([]string{"sourcegraph"}, e.grantsSeen[1], "the grant reaches the next call_llm")
	s.Equal([]string{"sourcegraph"}, e.grantsSeen[2], "and stays for the rest of the run")
}

// The bound parameters call_llm recorded travel with the set into that turn's
// execute_tools, which merges them over the model's calls. A literal arrives
// as its value; a global setting's parameter arrives as a name only.
func (s *ToolCapabilitiesLoopSuite) TestBoundParams_ReachExecuteTools() {
	env := s.NewTestWorkflowEnvironment()
	caps := map[string]interface{}{
		"offered":    []interface{}{"shell"},
		"permission": "mutating",
		"bound_params": map[string]interface{}{
			"shell": map[string]interface{}{"params": map[string]interface{}{
				"timeout":     map[string]interface{}{"literal": 30000},
				"description": map[string]interface{}{"global": true},
			}},
		},
	}
	e := &capsAgentEnv{turns: []map[string]interface{}{
		scriptedTurn(caps, map[string]interface{}{"id": "tc-shell", "name": "shell", "input": `{"command":"ls"}`}),
	}}
	newCapsAgentEnv(s.T(), env, e)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput("chat-caps-bound", "wf-chat-caps-bound"))
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	s.Require().Len(e.capsSeen, 1)
	params := e.capsSeen[0].GetBoundParams()["shell"].GetParams()
	s.Require().Len(params, 2)
	s.Equal(float64(30000), params["timeout"].GetLiteral().GetNumberValue())
	s.True(params["description"].GetGlobal())
	s.Nil(params["description"].GetLiteral(), "a global setting's value is not in history")
}

// A spawn the turn did not offer is not dispatched: it goes to the
// ExecuteTools activity, which refuses it like any other call.
func (s *ToolCapabilitiesLoopSuite) TestSpawnNotOffered_IsRefusedByTheActivityNotDispatched() {
	env := s.NewTestWorkflowEnvironment()
	caps := map[string]interface{}{"offered": []interface{}{"view"}, "permission": "mutating"}
	e := &capsAgentEnv{
		turns: []map[string]interface{}{
			scriptedTurn(caps,
				map[string]interface{}{"id": "tc-spawn", "name": "spawn", "input": `{"preset":"general","prompt":"go"}`},
				map[string]interface{}{"id": "tc-view", "name": "view", "input": `{"file_path":"a"}`}),
		},
	}
	newCapsAgentEnv(s.T(), env, e)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput("chat-caps-spawn", "wf-chat-caps-spawn"))
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	s.Require().Len(e.toolCallsSeen, 1)
	s.ElementsMatch([]string{"spawn", "view"}, e.toolCallsSeen[0],
		"the refused spawn is handed to the activity rather than dispatched as a child")
}

// A continue-as-new successor starts with its predecessor's grants, so its
// very first call_llm offers what the thread had loaded.
func (s *ToolCapabilitiesLoopSuite) TestContinueAsNewSuccessor_SeedsGrantsIntoItsFirstCallLLM() {
	env := s.NewTestWorkflowEnvironment()
	e := &capsAgentEnv{}
	newCapsAgentEnv(s.T(), env, e)

	const thread = "wf-chat-caps-successor"
	input := rootDrainWorkflowInput("chat-caps-successor", thread)
	input.Resume = &ResumeInput{
		NodeID:     "agent_loop",
		ToolGrants: map[string][]string{thread: {"sourcegraph"}, "some-child-thread": {"fetch"}},
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	s.Require().NotEmpty(e.grantsSeen)
	s.Equal([]string{"sourcegraph"}, e.grantsSeen[0], "only this thread's grants, on the first turn")
}

// A run whose call_llm recorded no set — every history from before the set —
// hands execute_tools none, and the activity falls back to the legacy policy.
func (s *ToolCapabilitiesLoopSuite) TestNoRecordedSet_HandsExecuteToolsNone() {
	env := s.NewTestWorkflowEnvironment()
	e := &capsAgentEnv{
		turns: []map[string]interface{}{{
			"response_text": "working",
			"tool_calls":    []interface{}{map[string]interface{}{"id": "tc-view", "name": "view", "input": `{"file_path":"a"}`}},
			"message":       map[string]interface{}{"role": "assistant", "text": "working"},
		}},
	}
	newCapsAgentEnv(s.T(), env, e)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput("chat-caps-legacy", "wf-chat-caps-legacy"))
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().Len(e.capsSeen, 1)
	s.Nil(e.capsSeen[0])
}
