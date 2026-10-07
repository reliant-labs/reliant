// Copyright (c) 2025 Reliant Labs
package activities

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A tool an agent loaded must still be offered, and still run, after its run
// is restarted from its checkpoint.
//
// This drives the whole path with the production activities (RegisterAll) on
// a real database, and a scripted model:
//
//  1. Run 1 loads sourcegraph with load_tool on turn N. The loop checkpoints
//     iteration N+1, and the execution dies before that turn's call_llm —
//     what a history-limit terminate or a lost execution leaves behind, which
//     the reconciler marks failed with the checkpoint kept.
//  2. The coarse fresh restart builds its resume input from durable state
//     alone (ResumeInputFromDurableState, the one SendMessage uses) and starts
//     run 2 on a fresh worker: no activity instance, executor or memory is
//     shared with run 1.
//  3. Turn N+1 is offered sourcegraph, and its call to it is accepted at
//     execution.
//
// Before the grants were recorded durably, run 2 started with none: turn N+1
// was not offered the tool and execute_tools refused the call as "load it
// first", though the thread history told the model it was loaded.
func TestLoadedToolSurvivesACoarseRestartFromTheCheckpoint(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	chatID := seedGrantsChat(t, repo)

	// ── Run 1: load on turn N, then die ─────────────────────────────────
	run1Model := &scriptedModel{turns: []llm.DriverResponse{{
		ToolCalls: []message.ToolCall{{ID: "call-load-" + chatID, Name: tools.ToolLoadTool, Input: `{"name":"sourcegraph"}`, Finished: true}},
	}}}
	var suite temporaltest.WorkflowTestSuite
	env1 := suite.NewTestWorkflowEnvironment()
	newGrantsWorker(t, env1, repo, run1Model, newGrantsExecutor(repo), 1)
	env1.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: chatID})
	env1.ExecuteWorkflow(v2.DynamicWorkflow, grantsWorkflowInput(chatID, nil))
	require.True(t, env1.IsWorkflowCompleted())
	require.Error(t, env1.GetWorkflowError(), "run 1 must die before turn N+1, as an interrupted run does")
	require.Len(t, run1Model.offeredTools(), 1, "run 1 took exactly turn N")
	require.NotContains(t, run1Model.offeredTools()[0], tools.ToolSourcegraph, "precondition: not preloaded")

	// ── The coarse fresh restart ────────────────────────────────────────
	resume := v2.ResumeInputFromDurableState(ctx, repo, chatID, chatID)
	require.Equal(t, "agent_loop", resume.NodeID, "the checkpoint run 1 wrote")
	require.Equal(t, 1, resume.LoopIteration, "turn N+1's iteration")

	// ── Run 2: a fresh worker, turn N+1 ─────────────────────────────────
	searchCallID := "call-search-" + chatID
	run2Model := &scriptedModel{turns: []llm.DriverResponse{{
		ToolCalls: []message.ToolCall{{ID: searchCallID, Name: tools.ToolSourcegraph, Input: `{"query":"repo:x foo"}`, Finished: true}},
	}}}
	run2Executor := newGrantsExecutor(repo)
	env2 := suite.NewTestWorkflowEnvironment()
	newGrantsWorker(t, env2, repo, run2Model, run2Executor, 0)
	env2.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: chatID})
	env2.ExecuteWorkflow(v2.DynamicWorkflow, grantsWorkflowInput(chatID, resume))
	require.True(t, env2.IsWorkflowCompleted())
	require.NoError(t, env2.GetWorkflowError())

	offered := run2Model.offeredTools()
	require.NotEmpty(t, offered)
	assert.Contains(t, offered[0], tools.ToolSourcegraph, "turn N+1 is offered the tool loaded on turn N")

	assert.Equal(t, []string{searchCallID}, run2Executor.ranCalls(), "turn N+1's call to it is accepted and runs")
	call, err := repo.GetToolCall(ctx, searchCallID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCompleted, call.Status, "not refused as 'not offered'")
}

// grantsAgentYAML is the builtin agent's loop shape: one call_llm, then
// execute_tools while the model calls tools. load_tool reaches everything; only
// view is preloaded.
const grantsAgentYAML = `
name: agent
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: outputs.tool_calls != null && size(outputs.tool_calls) > 0
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          save_message:
            role: "{{output.message.role}}"
            content: "{{output.message.text}}"
            tool_calls: "{{output.tool_calls}}"
          args:
            model: mock-model
            tools_config:
              preloaded_tools: ["view"]
              loadable_tools: ["*"]
              permission: mutating
        - id: execute_tools
          type: execute_tools
          save_message:
            role: tool
            content: ""
            tool_results: "{{output.tool_results}}"
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: execute_tools
              condition: nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0
`

// grantsWorkflowInput is what SendMessage starts for the chat: a fresh run, or
// with resume a coarse restart on the same root thread.
func grantsWorkflowInput(chatID string, resume *v2.ResumeInput) v2.WorkflowInput {
	mode := model.ThreadModeNew
	if resume != nil {
		mode = model.ThreadModeInherit
	}
	return v2.WorkflowInput{
		ChatID:       chatID,
		WorkflowName: "agent",
		Inputs:       map[string]interface{}{},
		ExecContext: &v2.ExecutionContext{
			WorkflowID: chatID, ChatID: chatID, Thread: chatID,
			ThreadMode: mode, WorkflowName: "agent",
		},
		Resume: resume,
	}
}

// seedGrantsChat creates a project, a chat whose root thread is the chat id
// (as every chat's is), and the user's message the run answers.
func seedGrantsChat(t *testing.T, repo *db.Repo) string {
	t.Helper()
	ctx := context.Background()
	chatID := "chat-" + uuid.NewString()
	userID := "user-" + uuid.NewString()
	project := &db.Project{ID: "project-" + uuid.NewString(), UserID: userID, Name: "Grants", Path: "/tmp/grants"}
	require.NoError(t, repo.CreateProject(ctx, project))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: project.ID, UserID: userID}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID})
	require.NoError(t, err)
	contextWindowID := chatID + ":" + chatID + ":0"
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: contextWindowID, ThreadID: chatID})
	require.NoError(t, err)

	ordinal, err := repo.GetNextOrdinal(ctx, chatID)
	require.NoError(t, err)
	seq, err := repo.GetNextSeq(ctx, chatID, chatID)
	require.NoError(t, err)
	msgID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID: msgID, ChatID: chatID, ThreadID: chatID, ContextWindowID: contextWindowID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Ordinal: ordinal, Seq: seq,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: uuid.NewString(), MessageID: msgID, Position: 0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptrTo("search the repo"),
		CreatedAt: now, UpdatedAt: now,
	}))
	return chatID
}

func ptrTo[T any](v T) *T { return &v }

// newGrantsWorker registers the production activities (RegisterAll) on env,
// with a scripted model and the given tool executor. Only the workflow
// definition and the run-status write are stubbed: the definition is the
// test's, and the status write needs a workflows row this test does not
// create. When callLLMTurns > 0, the execution dies after that many call_llm
// turns, at the next one.
func newGrantsWorker(t *testing.T, env *testsuite.TestWorkflowEnvironment, repo *db.Repo, model *scriptedModel, executor toolexec.ToolExecutor, callLLMTurns int) {
	t.Helper()
	// The production data converter: activities return protos, which the
	// workflow decodes into maps.
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	wf, err := wfyaml.ParseWorkflow([]byte(grantsAgentYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	deps := &Activities{
		Repo:           repo,
		Threads:        threads.NewService(repo),
		ToolsFactory:   tools.NewToolsFactory(&tools.ToolsOptions{Repo: repo}),
		ToolExecutor:   executor,
		ConfigProvider: emptyConfigProvider{},
		DriverResolver: func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return model, nil
		},
	}
	registry := v2.NewActivityRegistry(repo)
	RegisterAll(registry, deps)

	stubbed := map[string]bool{"ActivityLoadWorkflow": true, "WorkflowStatus": true, "CallLLM": callLLMTurns > 0}
	for _, name := range registry.List() {
		if stubbed[name] {
			continue
		}
		fn, err := registry.Get(name)
		require.NoError(t, err)
		env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	env.RegisterActivityWithOptions(func(context.Context, map[string]string) (v2.LoadedWorkflow, error) {
		return v2.LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowStatus"})
	env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "EmitThreadEvent"})

	if callLLMTurns > 0 {
		fn, err := registry.Get("CallLLM")
		require.NoError(t, err)
		callLLM, ok := fn.(func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error))
		require.True(t, ok, "CallLLM is registered as %T", fn)
		var mu sync.Mutex
		turns := 0
		env.RegisterActivityWithOptions(func(ctx context.Context, input types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
			mu.Lock()
			turns++
			turn := turns
			mu.Unlock()
			if turn > callLLMTurns {
				return nil, temporal.NewNonRetryableApplicationError("the execution was lost", "ExecutionLost", nil)
			}
			return callLLM(ctx, input)
		}, activity.RegisterOptions{Name: "CallLLM"})
	}
}

type emptyConfigProvider struct{}

func (emptyConfigProvider) GetProjectConfig(context.Context, config.ProjectRef) (*config.Config, error) {
	return &config.Config{}, nil
}

// scriptedModel answers each turn from its script — a turn past the end
// finishes without tool calls — and records the tool names each turn was
// offered. No model is ever called.
type scriptedModel struct {
	mu      sync.Mutex
	turns   []llm.DriverResponse
	offered [][]string
}

func (m *scriptedModel) Name() string { return "scripted" }

func (m *scriptedModel) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}

func (m *scriptedModel) ValidateKey(context.Context) error { return nil }

func (m *scriptedModel) SendMessages(_ context.Context, _ []string, _ []message.Message, offered []tools.Tool) (*llm.DriverResponse, error) {
	return m.next(offered), nil
}

func (m *scriptedModel) StreamResponse(_ context.Context, _ []string, _ []message.Message, offered []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: m.next(offered)}
	close(ch)
	return ch
}

func (m *scriptedModel) next(offered []tools.Tool) *llm.DriverResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(offered))
	for _, tool := range offered {
		names = append(names, tool.Name())
	}
	turn := len(m.offered)
	m.offered = append(m.offered, names)

	response := llm.DriverResponse{Content: "done", FinishReason: message.FinishReasonEndTurn}
	if turn < len(m.turns) {
		response = m.turns[turn]
		response.FinishReason = message.FinishReasonToolUse
	}
	response.Usage = llm.TokenUsage{TokenCount: 1}
	return &response
}

func (m *scriptedModel) offeredTools() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]string(nil), m.offered...)
}

// grantsExecutor runs load_tool for real, in process, as the worker does —
// so its grant is decided and recorded by the production code — and answers
// every other tool without running it, recording the call.
type grantsExecutor struct {
	server toolexec.ToolExecutor
	mu     sync.Mutex
	ran    []string
}

func newGrantsExecutor(repo db.Repository) *grantsExecutor {
	server := toolexec.NewRemoteExecutor(nil)
	server.SetServerExecutor(toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{Repo: repo})))
	return &grantsExecutor{server: server}
}

func (e *grantsExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	if req.ToolName == tools.ToolLoadTool {
		return e.server.ExecuteTool(ctx, req)
	}
	e.mu.Lock()
	e.ran = append(e.ran, req.ToolCallID)
	e.mu.Unlock()
	return &toolexec.ToolResult{Success: true, Content: "3 matches"}, nil
}

func (e *grantsExecutor) Close() error { return nil }

func (e *grantsExecutor) ranCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ran...)
}
