// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow"
	wfmodel "github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The chat handler and the workflow it signals are tested separately
// everywhere else. This drives them together: the real UpdateWorkflowParams
// handler runs against the real database fixture, the signals it sends are
// replayed into the real DynamicWorkflow, and the activity stubs record what
// every later model call was dispatched with.

const switchedModel = "gpt-6.1-sol@codex"

// providerSwitchAgentYAML is agent.yaml's shape trimmed to the loop, but with
// the call_llm model arg wired to the run's `model` input as production does,
// so a stub sees the selector each call would resolve.
const providerSwitchAgentYAML = `
name: agent
entry: [agent_loop]
inputs:
  model:
    type: model
    default:
      tags: ["flagship"]
nodes:
  - id: agent_loop
    type: loop
    while: (outputs.tool_calls != null && size(outputs.tool_calls) > 0)
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: "{{inputs.model}}"
        - id: execute_tools
          type: execute_tools
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: execute_tools
              condition: nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0
              label: "has_tools"
`

type switchCall struct {
	who      string
	turn     int
	at       time.Time
	runtime  types.RuntimeContext
	selector *reliantv1.ModelSelector
}

// switchHarness is the activity surface of DynamicWorkflow with a scripted
// CallLLM. Calls listed in hold stay in flight until `switched` closes, so the
// clock cannot advance and every thread sits at a known point when the user
// switches the chat's model.
type switchHarness struct {
	t          *testing.T
	env        *testsuite.TestWorkflowEnvironment
	rootThread string

	mu          sync.Mutex
	turns       map[string]int
	whoOfThread map[string]string
	calls       []switchCall
	parked      map[string]bool

	// durable is what the stopped-descendant lookup reports, once: a
	// descendant that failed in an earlier execution of the chat.
	durable     *db.StoppedSpawnAgent
	durableOnce sync.Once

	hold     map[string]bool
	switched chan struct{}
	script   func(who string, turn int) ([]map[string]interface{}, error)
}

func (h *switchHarness) callsOf(who string) []switchCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []switchCall
	for _, c := range h.calls {
		if c.who == who {
			out = append(out, c)
		}
	}
	return out
}

// call returns who's turn-th model call, or fails the test.
func (h *switchHarness) call(who string, turn int) switchCall {
	h.t.Helper()
	for _, c := range h.callsOf(who) {
		if c.turn == turn {
			return c
		}
	}
	require.FailNowf(h.t, "model call never dispatched", "%s turn %d was not dispatched; calls: %v", who, turn, h.summary())
	return switchCall{}
}

func (h *switchHarness) summary() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.calls))
	for _, c := range h.calls {
		out = append(out, fmt.Sprintf("%s#%d", c.who, c.turn))
	}
	return out
}

func (h *switchHarness) dispatched(who string, turn int) bool {
	for _, c := range h.callsOf(who) {
		if c.turn == turn {
			return true
		}
	}
	return false
}

func (h *switchHarness) isParked(who string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.parked[who]
}

func (h *switchHarness) callLLM(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	h.mu.Lock()
	h.turns[thread]++
	turn := h.turns[thread]
	who := "root"
	if thread != h.rootThread {
		who = h.whoOfThread[thread]
	}
	h.calls = append(h.calls, switchCall{
		who: who, turn: turn, at: h.env.Now(), runtime: input.Runtime,
		selector: wfmodel.CelModelSelectorValue(wfmodel.GetCallLLMArgs(input.Node).GetModel()),
	})
	held := h.hold[fmt.Sprintf("%s#%d", who, turn)]
	h.mu.Unlock()

	if held {
		select {
		case <-h.switched:
		case <-ctx.Done():
		}
	}
	calls, err := h.script(who, turn)
	if err != nil {
		return nil, err
	}
	var encoded []interface{}
	if len(calls) > 0 {
		raw, err := json.Marshal(calls)
		require.NoError(h.t, err)
		require.NoError(h.t, json.Unmarshal(raw, &encoded))
	}
	return map[string]interface{}{"response_text": "working", "tool_calls": encoded}, nil
}

func newSwitchHarness(t *testing.T, env *testsuite.TestWorkflowEnvironment, rootThread string) *switchHarness {
	t.Helper()
	h := &switchHarness{
		t: t, env: env, rootThread: rootThread,
		turns: map[string]int{}, whoOfThread: map[string]string{}, parked: map[string]bool{},
		hold: map[string]bool{}, switched: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-h.switched:
		default:
			close(h.switched)
		}
	})

	parsed, err := wfyaml.ParseWorkflow([]byte(providerSwitchAgentYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(parsed)
	require.NoError(t, err)

	register := func(name string, fn interface{}) {
		env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	ok := func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}
	register("ActivityLoadWorkflow", func(_ context.Context, _ types.LoadWorkflowInput) (v2.LoadedWorkflow, error) {
		return v2.LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	for _, name := range []string{"ThreadStatus", "Cleanup", "WorkflowError", "EmitToolCallStatus"} {
		register(name, ok)
	}
	register("EmitStreamFinalized", func(_ context.Context, _ types.EmitStreamFinalizedInput) (types.EmitStreamFinalizedOutput, error) {
		return types.EmitStreamFinalizedOutput{}, nil
	})
	register("UsageLimitWait", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		thread, _ := input["thread"].(string)
		clear, _ := input["clear"].(bool)
		if !clear && input["notice"] == nil {
			h.mu.Lock()
			h.parked[h.whoOfThread[thread]] = true
			h.mu.Unlock()
		}
		return map[string]interface{}{"success": true}, nil
	})
	register("WorkflowStatus", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		toolCallID, _ := input["spawned_by_tool_call_id"].(string)
		thread, _ := input["thread"].(string)
		if toolCallID != "" && thread != "" && input["status"] == "started" {
			h.mu.Lock()
			h.whoOfThread[thread] = toolCallID
			h.mu.Unlock()
		}
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
		id, _ := input["tool_call_id"].(string)
		return map[string]interface{}{"id": "am-" + id}, nil
	})
	register("ValidateThreadOwnership", func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"valid": true}, nil
	})
	// The presets own their selector, as production's preset YAMLs do: a
	// global model update must not be able to overwrite it.
	register("LoadPresetParams", func(_ context.Context, input types.LoadPresetParamsInput) (map[string]interface{}, error) {
		tag := "flagship"
		if input.PresetName == "fast" {
			tag = "fast"
		}
		return map[string]interface{}{"model": map[string]interface{}{"tags": []interface{}{tag}}}, nil
	})
	register("LookupStoppedSubAgent", func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		out := map[string]interface{}{}
		if all, _ := input["all"].(bool); all && h.durable != nil {
			h.durableOnce.Do(func() { out["agents"] = []*db.StoppedSpawnAgent{h.durable} })
		}
		return out, nil
	})
	register("ExecuteTools", func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
		return map[string]interface{}{"tool_results": []interface{}{}}, nil
	})
	register("CallLLM", h.callLLM)
	return h
}

func spawnPreset(id, preset, prompt string) map[string]interface{} {
	input, _ := json.Marshal(map[string]interface{}{"preset": preset, "prompt": prompt})
	return map[string]interface{}{"id": id, "name": "spawn", "input": string(input)}
}

func readFileCall(id string) map[string]interface{} {
	return map[string]interface{}{"id": id, "name": "read_file", "input": `{"path":"a.txt"}`}
}

func providerUsageLimit(resetAt time.Time) error {
	msg := chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, resetAt.UTC().Format(time.RFC3339),
		chatmarkers.ProviderUsageLimitLead+" (Anthropic, 5-hour window) — resets soon")
	return temporal.NewNonRetryableApplicationError(msg, "ProviderUsageLimit", nil)
}

// A user on Claude has a root plus fast, flagship and usage-limit-waiting
// descendants, then switches the chat to GPT-6.1 Sol on Codex. The root keeps
// that exact model. Each descendant keeps its preset's tag and resolves it on
// Codex; none of them is handed the root's concrete model.
func TestUpdateWorkflowParams_ProviderSwitchReachesTheWorkflowsDescendants(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Active())
	// The switch target must be a provider the user has connected, or the
	// handler moves the pick back onto Claude before anything is signalled.
	db.SeedProviderAccount(t, repo, "test-user", "codex", "test-key-codex")

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(60 * time.Second)

	input := v2.WorkflowInput{
		ChatID:       fx.chatID,
		WorkflowName: "agent",
		Inputs:       map[string]interface{}{"model": map[string]interface{}{"id": opusOnClaude}},
		ExecContext: &v2.ExecutionContext{
			WorkflowID:   fx.chatID,
			ChatID:       fx.chatID,
			Thread:       fx.rootThreadID,
			ThreadMode:   wfmodel.ThreadModeNew,
			WorkflowName: "agent",
			ProjectPath:  "/project",
		},
	}
	h := newSwitchHarness(t, env, fx.rootThreadID)
	resetAt := env.Now().Add(4 * time.Hour)

	// A fast descendant that stopped on a failure in an earlier execution:
	// only the database remembers it.
	const durableThread = "durable-thread"
	h.whoOfThread[durableThread] = "durable"
	h.durable = &db.StoppedSpawnAgent{
		ToolCallID:        "durable",
		ParentThreadID:    fx.rootThreadID,
		ToolInput:         `{"preset":"fast","prompt":"earlier job"}`,
		ChildThreadID:     durableThread,
		IssuingWorkflowID: fx.chatID,
		SpawnDepth:        1,
	}

	// Everything in flight at the switch is held until the handler has run and
	// its signals are queued, so the turn after each one is the first to see
	// the switch.
	for _, key := range []string{"root#2", "fast-a#2", "flag-a#2"} {
		h.hold[key] = true
	}
	h.script = func(who string, turn int) ([]map[string]interface{}, error) {
		switch who {
		case "root":
			switch turn {
			case 1:
				return []map[string]interface{}{
					spawnPreset("fast-a", "fast", "scan the repo"),
					spawnPreset("flag-a", "general", "implement the feature"),
					spawnPreset("waiting", "general", "long job"),
				}, nil
			case 2:
				// Issued after the switch is applied: a future descendant.
				return []map[string]interface{}{spawnPreset("future", "fast", "late helper")}, nil
			}
		case "fast-a", "flag-a":
			if turn <= 2 {
				return []map[string]interface{}{readFileCall(who + "-read")}, nil
			}
		case "waiting":
			if turn == 1 {
				return nil, providerUsageLimit(resetAt)
			}
		}
		return nil, nil
	}

	// The user switches the chat once every thread is where the scenario needs
	// it: the root and both active descendants mid-call, the third parked on
	// its provider's usage limit.
	var switchErr error
	var sent []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(h.switched)
		deadline := time.Now().Add(30 * time.Second)
		for !h.dispatched("root", 2) || !h.dispatched("fast-a", 2) || !h.dispatched("flag-a", 2) || !h.isParked("waiting") {
			if time.Now().After(deadline) {
				switchErr = fmt.Errorf("threads never reached the switch point; calls: %v", h.summary())
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, switchErr = service.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{
			"model": map[string]any{"id": switchedModel},
		})))
		if switchErr != nil {
			return
		}
		for _, sig := range temporal.signals {
			sent = append(sent, sig.Name)
			env.SignalWorkflow(sig.Name, sig.Arg)
		}
	}()

	env.ExecuteWorkflow(v2.DynamicWorkflow, input)
	<-done
	require.NoError(t, switchErr)
	require.True(t, env.IsWorkflowCompleted(), "the workflow must run to completion; calls: %v", h.summary())
	require.NoError(t, env.GetWorkflowError())

	// What the handler actually sent, so a failure below names its cause.
	require.Equal(t, []string{workflow.SignalUpdateWorkflowState, v2.CapacityWakeSignalName}, sent)
	update := temporal.signals[0].Arg.(map[string]interface{})
	assert.Equal(t, "codex", update[v2.InputKeyProviderSwitch], "the model update must name the provider the chat moved to")
	assert.Equal(t, switchedModel, temporal.signals[1].Arg.(v2.CapacityWakeSignal).UseModel)

	// Control: before the switch nothing was bound to a provider.
	for _, pre := range []switchCall{h.call("root", 1), h.call("root", 2), h.call("fast-a", 2), h.call("flag-a", 2), h.call("waiting", 1)} {
		assert.Empty(t, pre.runtime.ChatProvider, "%s#%d ran before the switch", pre.who, pre.turn)
	}

	// The root keeps exactly what the user picked.
	root := h.call("root", 3)
	assert.Equal(t, "codex", root.runtime.ChatProvider, "the root's next call must be bound to the chosen provider")
	assert.False(t, root.runtime.Descendant)
	assert.Equal(t, switchedModel, root.selector.GetId(), "the root's next call keeps the exact model and provider")
	assert.Empty(t, root.selector.GetTags())

	// Existing and future descendants keep their tags and move provider only.
	for _, want := range []struct {
		who  string
		turn int
		tag  string
		why  string
	}{
		{"fast-a", 3, "fast", "an existing fast descendant"},
		{"flag-a", 3, "flagship", "an existing flagship descendant"},
		{"future", 1, "fast", "a spawn made after the switch"},
	} {
		c := h.call(want.who, want.turn)
		assert.Equal(t, "codex", c.runtime.ChatProvider, "%s: its next call must carry the chosen provider", want.why)
		assert.True(t, c.runtime.Descendant, "%s", want.why)
		assert.Equal(t, []string{want.tag}, c.selector.GetTags(), "%s keeps its semantic tag", want.why)
		assert.Empty(t, c.selector.GetId(), "%s must not be pinned to the root's concrete model", want.why)
		assert.Equal(t, []string{want.tag}, c.runtime.PresetModelTags, "%s", want.why)
		assert.Empty(t, c.runtime.FallbackModel, "%s must not be on a concrete fallback lease", want.why)
	}

	// A descendant stopped in an earlier execution is revived by the switch
	// and resumes on the chosen provider with its own tag.
	durable := h.call("durable", 1)
	assert.Equal(t, "codex", durable.runtime.ChatProvider, "a durably stopped descendant must resume on the chosen provider")
	assert.True(t, durable.runtime.Descendant)
	assert.Equal(t, []string{"fast"}, durable.selector.GetTags(), "the revived descendant keeps its tag")
	assert.Empty(t, durable.selector.GetId(), "the revived descendant must not run on the root's concrete model")
	assert.Equal(t, []string{"fast"}, durable.runtime.PresetModelTags)
	assert.Empty(t, durable.runtime.FallbackModel)

	// The waiting descendant is woken by the switch, long before its limit
	// resets, and re-evaluates its own tag on the new provider.
	waiting := h.call("waiting", 2)
	assert.True(t, waiting.at.Before(resetAt), "the switch must wake the waiting descendant before its reset (%s), woke at %s", resetAt, waiting.at)
	assert.Equal(t, "codex", waiting.runtime.ChatProvider)
	assert.True(t, waiting.runtime.Descendant)
	assert.Equal(t, []string{"flagship"}, waiting.selector.GetTags(), "the woken descendant keeps its tag")
	assert.Empty(t, waiting.selector.GetId(), "the woken descendant must not run on the root's concrete model")
	assert.Equal(t, []string{"flagship"}, waiting.runtime.PresetModelTags)
	assert.Empty(t, waiting.runtime.FallbackModel, "the root's model must not become the waiting descendant's lease")
}
