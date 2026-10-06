// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// These drive a turn end to end through the REAL activities: call_llm resolves
// and records the capability set, the set crosses a serialization boundary the
// way it crosses Temporal history, and a separately constructed execute_tools
// — standing in for another worker, or this one after a restart — enforces it.
// Nothing is shared between the two but the recorded output, which is the
// property the old process-global store could not give.

// toolsConfig is a node's tools_config.
func toolsConfig(permission string, preloaded, loadable, spawn []string) *reliantv1.ToolsConfig {
	cfg := &reliantv1.ToolsConfig{
		PreloadedTools: celStringListLiteral(preloaded),
		Permission:     &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: permission}},
	}
	if loadable != nil {
		cfg.LoadableTools = celStringListLiteral(loadable)
	}
	if spawn != nil {
		cfg.Spawn = celStringListLiteral(spawn)
	}
	return cfg
}

// turn runs one call_llm turn on a FRESH activity instance with the grants the
// workflow recorded for the thread, and returns its output.
func (f *noMachineFixture) turn(t *testing.T, cfg *reliantv1.ToolsConfig, grants []string) *reliantv1.CallLLMOutput {
	t.Helper()
	callLLM := NewCallLLMActivity(f.h.Repo(), nil,
		tools.NewToolsFactory(&tools.ToolsOptions{Repo: f.h.Repo()}),
		&staticConfigProvider{}, f.callLLM.driverResolver, f.callLLM.mcpBinder)
	var output CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(callLLM.Execute, ActivityInput{
		Runtime: RuntimeContext{ChatID: f.chat.ID, Thread: f.chat.ID, ToolGrants: grants},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model:       &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "mock-model"}}},
			ToolsConfig: cfg,
		}}},
	}, &output))
	return &output
}

// throughHistory serializes a recorded set and reads it back, the way it
// travels from call_llm's output to execute_tools' input.
func throughHistory(t *testing.T, caps *reliantv1.ToolCapabilities) *reliantv1.ToolCapabilities {
	t.Helper()
	require.NotNil(t, caps, "call_llm must record the turn's capability set")
	encoded, err := protojson.Marshal(caps)
	require.NoError(t, err)
	decoded := &reliantv1.ToolCapabilities{}
	require.NoError(t, protojson.Unmarshal(encoded, decoded))
	return decoded
}

// serverToolExecutor runs server-placed tools in-process, as the worker does,
// so load_tool really runs.
func serverToolExecutor(repo db.Repository) toolexec.ToolExecutor {
	executor := toolexec.NewRemoteExecutor(nil)
	executor.SetServerExecutor(toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{Repo: repo})))
	return executor
}

func (f *noMachineFixture) execute(t *testing.T, executor toolexec.ToolExecutor, caps *reliantv1.ToolCapabilities, calls ...message.ToolCall) *ExecuteToolsOutput {
	t.Helper()
	var output ExecuteToolsOutput
	require.NoError(t, f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), executor).Execute, ExecuteToolsInput{
		ChatID:       f.chat.ID,
		Thread:       f.chat.ID,
		Capabilities: caps,
		ToolCalls:    calls,
	}, &output))
	return &output
}

// A tool loaded via load_tool on turn N is offered, and accepted, on turn N+1
// — and NOT accepted on turn N, where it was never offered. sourcegraph stands
// in for any tool outside the preloaded bundle (generate_image is the shipped
// case, but loading it also needs a media provider this test does not have).
func TestCapabilities_LoadInTurnNIsAcceptedInTurnN1(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionMutating, []string{tools.ToolView}, []string{"*"}, nil)

	turnN := f.turn(t, cfg, nil)
	capsN := throughHistory(t, turnN.GetCapabilities())
	require.NotContains(t, capsN.GetOffered(), tools.ToolSourcegraph, "precondition: not preloaded")
	require.NotContains(t, f.driver.capturedTools, tools.ToolSourcegraph)

	executed := f.execute(t, serverToolExecutor(f.h.Repo()), capsN,
		message.ToolCall{ID: "call-load", Name: tools.ToolLoadTool, Input: `{"name":"sourcegraph"}`},
		message.ToolCall{ID: "call-search-early", Name: tools.ToolSourcegraph, Input: `{"query":"repo:x foo"}`},
	)
	results := resultsByID(executed.GetToolResults())
	require.False(t, results["call-load"].GetIsError(), results["call-load"].GetContent())
	assert.True(t, results["call-search-early"].GetIsError(), "not offered on turn N, so refused on turn N")
	assert.Equal(t, []string{tools.ToolSourcegraph}, executed.GetGrantedTools(),
		"the grant is reported for the workflow to record")

	// The workflow hands the thread's grants to its next call_llm.
	turnN1 := f.turn(t, cfg, executed.GetGrantedTools())
	capsN1 := throughHistory(t, turnN1.GetCapabilities())
	assert.Contains(t, f.driver.capturedTools, tools.ToolSourcegraph, "offered to the model on turn N+1")
	assert.Contains(t, capsN1.GetOffered(), tools.ToolSourcegraph)

	mock := newMockToolExecutor()
	accepted := f.execute(t, mock, capsN1,
		message.ToolCall{ID: "call-search", Name: tools.ToolSourcegraph, Input: `{"query":"repo:x foo"}`})
	require.Len(t, accepted.GetToolResults(), 1)
	assert.False(t, accepted.GetToolResults()[0].GetIsError(), accepted.GetToolResults()[0].GetContent())
	assert.Equal(t, 1, mock.GetExecutionCount("call-search"))
}

// The tier and the grants live in what call_llm recorded, not in the worker
// that recorded it: an execute_tools on a different worker (or the same one
// after a restart — every deploy) enforces them exactly. Before, the tier and
// the grants were in a process-global map, so this execute_tools would have
// read "mutating" and refused start_run.
func TestCapabilities_SurviveAFreshWorkerBetweenCallLLMAndExecuteTools(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionOrchestrator,
		[]string{tools.ToolView, tools.ToolStartRun}, []string{"*"}, nil)

	// Worker A: a turn whose thread had loaded generate_image earlier.
	recorded := throughHistory(t, f.turn(t, cfg, []string{tools.ToolGenerateImage}).GetCapabilities())
	assert.Equal(t, tools.PermissionOrchestrator, recorded.GetPermission())

	// Worker B: a fresh activity and executor; nothing in this process ran the turn.
	mock := newMockToolExecutor()
	executed := f.execute(t, mock, recorded,
		message.ToolCall{ID: "call-start-run", Name: tools.ToolStartRun, Input: `{"prompt":"go"}`},
		message.ToolCall{ID: "call-image", Name: tools.ToolGenerateImage, Input: `{"prompt":"a cat"}`},
	)
	for _, r := range executed.GetToolResults() {
		assert.False(t, r.GetIsError(), "%s must run on a fresh worker: %s", r.GetName(), r.GetContent())
	}
	assert.Equal(t, 1, mock.GetExecutionCount("call-start-run"), "the orchestrator tier survived")
	assert.Equal(t, 1, mock.GetExecutionCount("call-image"), "the earlier grant survived")
}

// An orchestrator that spawns and runs orchestrator-only tools works when
// call_llm and execute_tools share no memory: the spawn offer and its presets
// — what the workflow dispatches against — and the tier all ride the record.
func TestCapabilities_OrchestratorToolRunsWithNoSharedMemory(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionOrchestrator,
		[]string{tools.ToolView, tools.ToolStartRun, tools.ToolControlRun}, nil,
		[]string{"spawn:builtin://agent(general,researcher)"})

	recorded := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())
	assert.Contains(t, f.driver.capturedTools, "spawn")
	assert.Contains(t, recorded.GetOffered(), "spawn")
	assert.Equal(t, []string{"general", "researcher"}, recorded.GetSpawnPresets())
	assert.Contains(t, recorded.GetOffered(), tools.ToolSpawnStop, "an agent that spawns can stop what it spawned")

	mock := newMockToolExecutor()
	executed := f.execute(t, mock, recorded,
		message.ToolCall{ID: "call-start-run", Name: tools.ToolStartRun, Input: `{"prompt":"go"}`},
		message.ToolCall{ID: "call-control-run", Name: tools.ToolControlRun, Input: `{"run_id":"r","action":"stop"}`},
	)
	for _, r := range executed.GetToolResults() {
		assert.False(t, r.GetIsError(), "%s: %s", r.GetName(), r.GetContent())
	}
	assert.Equal(t, 1, mock.GetExecutionCount("call-start-run"))
	assert.Equal(t, 1, mock.GetExecutionCount("call-control-run"))
}

// The spawn management tools a thread gets from having sub-agents are part of
// the recorded set, so execute_tools runs them. They are granted from spawn
// history, which is read in call_llm; were they appended to the tool array
// past the set, the model would be handed spawn_status and every call to it
// refused as not offered — on the turn right after a fan-out, which is when an
// orchestrator needs it.
func TestCapabilities_SpawnHistoryGrantsAreAcceptedAtExecution(t *testing.T) {
	// No spawn config and no loadable reach: nothing but spawn history can
	// grant these tools.
	cfg := toolsConfig(tools.PermissionMutating, []string{tools.ToolView}, nil, nil)
	statusCall := message.ToolCall{ID: "call-status", Name: tools.ToolSpawnStatus, Input: `{}`}
	sendCall := message.ToolCall{ID: "call-send", Name: tools.ToolSpawnSend, Input: `{"agent_id":"a","message":"hi"}`}
	stopCall := message.ToolCall{ID: "call-stop", Name: tools.ToolSpawnStop, Input: `{"agent_id":"a"}`}

	t.Run("a thread with sub-agents of its own may check on, message and stop them", func(t *testing.T) {
		f := setupNoMachineFixture(t, false)
		seedSpawnedChild(t, f.h.Repo(), f.chat.ID, f.chat.ID)

		recorded := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())
		for _, name := range []string{tools.ToolSpawnStatus, tools.ToolSpawnSend, tools.ToolSpawnStop} {
			assert.Contains(t, f.driver.capturedTools, name)
			assert.Contains(t, recorded.GetOffered(), name)
		}

		mock := newMockToolExecutor()
		executed := f.execute(t, mock, recorded, statusCall, sendCall, stopCall)
		for _, r := range executed.GetToolResults() {
			assert.False(t, r.GetIsError(), "%s: %s", r.GetName(), r.GetContent())
		}
		assert.Equal(t, 1, mock.GetExecutionCount("call-status"))
		assert.Equal(t, 1, mock.GetExecutionCount("call-send"))
		assert.Equal(t, 1, mock.GetExecutionCount("call-stop"))
	})

	t.Run("a branch may look at the sub-agents it inherited, never control them", func(t *testing.T) {
		f := setupNoMachineFixture(t, false)
		seedSpawnedChild(t, f.h.Repo(), f.chat.ID, f.chat.ID)
		f.h.CreateTestUserMessage(context.Background(), f.chat.ID, f.chat.ID)
		f.chat = branchAtLatestMessage(t, f.h.Repo(), f.chat, "chat-branch-"+uuid.NewString())
		f.h.CreateTestUserMessage(context.Background(), f.chat.ID, f.chat.ID)

		recorded := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())
		assert.Contains(t, recorded.GetOffered(), tools.ToolSpawnStatus)
		assert.NotContains(t, recorded.GetOffered(), tools.ToolSpawnSend)
		assert.NotContains(t, recorded.GetOffered(), tools.ToolSpawnStop)

		mock := newMockToolExecutor()
		results := resultsByID(f.execute(t, mock, recorded, statusCall, sendCall, stopCall).GetToolResults())
		assert.False(t, results["call-status"].GetIsError(), results["call-status"].GetContent())
		assert.Equal(t, 1, mock.GetExecutionCount("call-status"))
		for _, id := range []string{"call-send", "call-stop"} {
			assert.True(t, results[id].GetIsError(), "%s must be refused on a branch", id)
			assert.Contains(t, results[id].GetContent(), "not offered")
			assert.Equal(t, 0, mock.GetExecutionCount(id), "%s must not run", id)
		}
	})
}

// The set call_llm records is exactly the tool array it sent, spawn and the
// structural tools included.
func TestCallLLM_RecordsExactlyTheToolArrayItSent(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionMutating,
		[]string{"tag:coding:default"}, []string{"*"}, []string{"spawn:builtin://agent(general)"})

	recorded := tools.CapabilitiesFromProto(f.turn(t, cfg, nil).GetCapabilities())
	require.NotNil(t, recorded)
	assert.ElementsMatch(t, f.driver.capturedTools, recorded.Offered)
	assert.True(t, recorded.LoadableAll)
}
