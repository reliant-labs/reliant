// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// countingMCPRuntime reports one connected server with one tool and counts
// every call, so a test sees both "the menu left MCP out" and "nothing even
// asked". Built here rather than taken from the host: chrome-devtools is a
// builtin server that connects wherever Chrome is installed (GitHub's runners
// have it), so a test that relied on the host would be asserting about the
// machine it ran on.
type countingMCPRuntime struct{ calls atomic.Int32 }

func (r *countingMCPRuntime) EnsureProjectServersLoaded(context.Context, string) *mcp.ProjectServerLoadResult {
	r.calls.Add(1)
	return &mcp.ProjectServerLoadResult{LoadedServers: []string{"fake"}, Errors: map[string]error{}}
}

func (r *countingMCPRuntime) ListProjectTools(string) (map[string][]mcp.Tool, error) {
	r.calls.Add(1)
	return map[string][]mcp.Tool{"fake": {{Name: "probe", InputSchema: map[string]interface{}{"type": "object"}}}}, nil
}

func (r *countingMCPRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return r.ListProjectTools("")
}

func (r *countingMCPRuntime) ProjectCallTool(string, string, string, string, map[string]interface{}) (*mcp.ToolResult, error) {
	r.calls.Add(1)
	return &mcp.ToolResult{}, nil
}

func (r *countingMCPRuntime) CallTool(string, string, string, map[string]interface{}) (*mcp.ToolResult, error) {
	r.calls.Add(1)
	return &mcp.ToolResult{}, nil
}

// noMachineFixture is a chat that either has no machine (by design) or is an
// ordinary chat, with a call_llm activity whose driver records the tool list it
// is offered and whose MCP binder is constructed explicitly.
type noMachineFixture struct {
	h       *IdempotencyTestHelper
	chat    *db.Chat
	driver  *toolCaptureMockDriver
	mcp     *countingMCPRuntime
	callLLM *CallLLMActivity
}

func setupNoMachineFixture(t *testing.T, noMachine bool) *noMachineFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()

	userID := "user-" + uuid.NewString()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), userID)
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, userID)
	if noMachine {
		markChatNoMachine(t, h, chat.ID)
	}
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	driver := &toolCaptureMockDriver{}
	resolver := func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	}
	runtime := &countingMCPRuntime{}
	binder := toolexec.MCPContextBinderFunc(func(tc *rctx.ToolContext) *rctx.ToolContext {
		if nomachine.Is(tc.Context) {
			return tc // what the real daemon binder does for a no-machine run
		}
		return tc.WithMCP(runtime)
	})

	return &noMachineFixture{
		h:      h,
		chat:   chat,
		driver: driver,
		mcp:    runtime,
		callLLM: NewCallLLMActivity(h.Repo(), nil,
			tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}),
			&staticConfigProvider{}, resolver, binder),
	}
}

// markChatNoMachine sets the column directly: the launcher is what sets it in
// production (and has its own test); here the activity is under test.
func markChatNoMachine(t *testing.T, h *IdempotencyTestHelper, chatID string) {
	t.Helper()
	_, err := h.DB().Exec(`UPDATE chats SET no_machine = true WHERE id = $1`, chatID)
	require.NoError(t, err)
}

// offeredTools runs one call_llm turn with the given tool lists and returns the
// names actually handed to the model.
func (f *noMachineFixture) offeredTools(t *testing.T, preloaded, loadable []string) []string {
	t.Helper()
	cfg := &reliantv1.ToolsConfig{PreloadedTools: celStringListLiteral(preloaded)}
	if loadable != nil {
		cfg.LoadableTools = celStringListLiteral(loadable)
	}
	var output CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(f.callLLM.Execute, ActivityInput{
		Runtime: RuntimeContext{ChatID: f.chat.ID, Thread: f.chat.ID},
		Node: &reliantv1.Node{Type: "call_llm", Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
			Model:       &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "mock-model"}}},
			ToolsConfig: cfg,
		}}},
	}, &output))
	return append([]string(nil), f.driver.capturedTools...)
}

// The menu a no-machine run is offered contains only tools that run without
// the user's machine. The same lists offered to an ordinary chat include the
// shell, the file tools and the MCP tool — which is the regression this guards:
// before, a daemon-less run was offered all of them, failed each call with "no
// daemon connected", and the breaker paused it after three.
func TestCallLLM_NoMachineRunIsOfferedOnlyToolsThatRunWithoutAMachine(t *testing.T) {
	preloaded := []string{"tag:coding:default", "tag:mcp"}

	ordinary := setupNoMachineFixture(t, false)
	withMachine := ordinary.offeredTools(t, preloaded, nil)
	for _, name := range []string{tools.ShellToolName, tools.ToolView, tools.ToolEdit, "mcp__fake__probe", tools.ToolFetch} {
		assert.Contains(t, withMachine, name, "control: an ordinary chat is offered %s", name)
	}

	f := setupNoMachineFixture(t, true)
	offered := f.offeredTools(t, preloaded, []string{"*"})

	require.NotEmpty(t, offered)
	for _, name := range offered {
		assert.False(t, tools.NeedsMachine(name), "a no-machine run was offered %q, which needs a machine", name)
	}
	assert.Contains(t, offered, tools.ToolFetch)
	assert.Contains(t, offered, tools.ToolWebSearch)
	assert.Contains(t, offered, tools.ToolLoadTool, "loadable reach is narrowed, not removed")
	assert.Zero(t, f.mcp.calls.Load(), "a no-machine run must not ask any MCP server for its tools")

	// load_tool's reach is narrowed the same way, so it cannot hand the model
	// a machine tool on the next turn either.
	scope := tools.Scope(f.chat.ID, f.chat.ID)
	assert.False(t, tools.GetLoadedToolsStore().CanLoadTool(scope, tools.ShellToolName))
	assert.False(t, tools.GetLoadedToolsStore().CanLoadTool(scope, tools.ToolView))
	assert.True(t, tools.GetLoadedToolsStore().CanLoadTool(scope, tools.ToolGenerateImage))
}

// A machine tool named anyway (from history, or a hallucination) is refused
// before it reaches the executor, and the refusal is not a daemon-offline
// signal: the breaker that pauses a run after three "no daemon connected"
// results must never see one from a run that has no machine by design.
func TestExecuteTools_NoMachineRunRefusesMachineToolsWithoutTrippingTheBreaker(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	executor := newMockToolExecutor()
	activity := NewExecuteToolsActivity(f.h.Repo(), executor)

	var output ExecuteToolsOutput
	require.NoError(t, f.h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
		ChatID: f.chat.ID,
		Thread: f.chat.ID,
		ToolCalls: []message.ToolCall{
			{ID: "call-shell", Name: tools.ShellToolName, Input: `{"command":"ls"}`},
			{ID: "call-view", Name: tools.ToolView, Input: `{"file_path":"README.md"}`},
		},
	}, &output))

	results := output.GetToolResults()
	require.Len(t, results, 2)
	for _, r := range results {
		assert.True(t, r.GetIsError(), "%s must be refused", r.GetName())
		assert.Equal(t, nomachine.Refusal(r.GetName()), r.GetContent())
		assert.False(t, daemonoffline.IsToolResultContent(r.GetContent()),
			"the refusal must not read as a daemon-offline result")
		assert.False(t, strings.Contains(r.GetContent(), daemonoffline.ErrorSubstring))
	}
	assert.Zero(t, executor.GetExecutionCount("call-shell"), "a refused tool must never reach the executor")
	assert.Zero(t, executor.GetExecutionCount("call-view"))
}

// A server tool in a no-machine run executes, and the executor sees a context
// marked as having no machine, so the transport layers below refuse to reach a
// daemon for it.
func TestExecuteTools_NoMachineRunStillRunsServerToolsOnAMarkedContext(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	executor := &contextRecordingExecutor{}
	activity := NewExecuteToolsActivity(f.h.Repo(), executor)

	var output ExecuteToolsOutput
	require.NoError(t, f.h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
		ChatID:    f.chat.ID,
		Thread:    f.chat.ID,
		ToolCalls: []message.ToolCall{{ID: "call-fetch", Name: tools.ToolFetch, Input: `{"url":"https://example.com","format":"text"}`}},
	}, &output))

	require.Len(t, output.GetToolResults(), 1)
	assert.False(t, output.GetToolResults()[0].GetIsError())
	assert.True(t, executor.sawNoMachine.Load(), "the executor's context must carry the no-machine mark")
}

type contextRecordingExecutor struct{ sawNoMachine atomic.Bool }

func (e *contextRecordingExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	e.sawNoMachine.Store(nomachine.Is(ctx))
	return &toolexec.ToolResult{Success: true, Content: "ok"}, nil
}

func (e *contextRecordingExecutor) Close() error { return nil }
