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
	// caps is the capability set the last offeredTools turn recorded.
	caps *tools.Capabilities
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
	f.caps = tools.CapabilitiesFromProto(output.GetCapabilities())
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
	// a machine tool on the next turn either — and the recorded set is exactly
	// what the model was handed.
	require.NotNil(t, f.caps, "call_llm records the turn's capability set")
	assert.ElementsMatch(t, offered, f.caps.Offered)
	assert.True(t, f.caps.NoMachine)
	assert.False(t, f.caps.CanLoad(tools.ShellToolName))
	assert.False(t, f.caps.CanLoad(tools.ToolView))
	assert.True(t, f.caps.CanLoad(tools.ToolGenerateImage))
}

// request_machine (research/NO_MACHINE_CHATS.md §3) is how a no-machine run
// asks for the user's computer. It is handed to a no-machine run that was given
// any tools, and never reaches a run on a machine — not by naming it, not by a
// glob, not through load_tool's reach or its advertised list.
func TestCallLLM_RequestMachineIsOfferedOnlyToARunWithNoMachine(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	assert.Contains(t, f.offeredTools(t, []string{"tag:coding:default"}, nil), tools.ToolRequestMachine)
	// A node whose only tools need a machine is exactly the one that may need
	// to ask for one.
	assert.Equal(t, []string{tools.ToolRequestMachine}, f.offeredTools(t, []string{tools.ShellToolName, tools.ToolEdit}, nil))

	ordinary := setupNoMachineFixture(t, false)
	onMachine := ordinary.offeredTools(t,
		[]string{"tag:coding:default", tools.ToolRequestMachine, "request_*"}, []string{"*"})
	assert.Contains(t, onMachine, tools.ShellToolName, "control: the machine run is offered its tools")
	assert.NotContains(t, onMachine, tools.ToolRequestMachine, "a run on a machine is never offered request_machine")

	require.NotNil(t, ordinary.caps)
	assert.NotContains(t, ordinary.caps.Deferred(), tools.ToolRequestMachine,
		"load_tool must not advertise it on a machine")
	assert.False(t, ordinary.caps.CanLoad(tools.ToolRequestMachine), "nor load it")
}

// The no-machine note and the GitHub repository note agree on what to do when
// the task needs the user's computer: both point at request_machine when the
// turn was offered it, and neither names it when it was not.
func TestNoMachineNotes_NameRequestMachineOnlyWhenOffered(t *testing.T) {
	repos := []githubRepo{{Owner: "acme", Name: "widgets"}}
	offered := &tools.Capabilities{Permission: tools.PermissionMutating, NoMachine: true,
		Offered: []string{tools.ToolRequestMachine}}
	notOffered := &tools.Capabilities{Permission: tools.PermissionMutating, NoMachine: true}

	with := noMachineNotes(offered, repos)
	require.Len(t, with, 2)
	assert.Equal(t, noMachineSystemNote, with[0])
	assert.Contains(t, with[1], "call request_machine")

	without := noMachineNotes(notOffered, repos)
	require.Len(t, without, 2)
	assert.Equal(t, noMachineSystemNoteWithoutRequest, without[0])
	for _, note := range without {
		assert.NotContains(t, note, "request_machine", "never name a tool the turn does not have")
	}

	assert.Len(t, noMachineNotes(offered, nil), 1, "no GitHub remote, no repository note")
}

// End to end: a no-machine turn in a project on GitHub, with GitHub not
// connected, is told both that it has no machine and that the code cannot be
// read from here, and both notes send it to request_machine.
func TestCallLLM_NoMachineNotesComposeAroundRequestMachine(t *testing.T) {
	f := setupIntegrationFixture(t, true, &ownerConnections{usable: map[string]bool{}})
	f.setProjectRemote(t, "https://github.com/acme/widgets.git")

	offered := f.offeredTools(t, []string{"tag:web"}, []string{"*"})
	require.Contains(t, offered, tools.ToolRequestMachine)

	prompt := f.systemPrompt()
	assert.Contains(t, prompt, noMachineSystemNote)
	assert.Contains(t, prompt, "No GitHub tools are available")
	assert.Contains(t, prompt, "if the task needs it, call request_machine")
	assert.NotContains(t, prompt, "if the task needs it, say so")
}

// A node given no tools at all (a title or a summary call) is not handed one.
func TestCallLLM_NoMachineNodeWithNoToolsIsHandedNone(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	assert.Empty(t, f.offeredTools(t, []string{}, nil))
}

// "Connect a machine" (SetChatDaemon → UpdateChatActiveDaemon) takes effect on
// the running workflow's next turn: call_llm re-reads the chat row every turn,
// so the same chat is offered the machine tools and no longer request_machine.
func TestCallLLM_ConnectingAMachineGivesTheNextTurnTheFullToolSet(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	preloaded := []string{"tag:coding:default"}

	before := f.offeredTools(t, preloaded, nil)
	assert.NotContains(t, before, tools.ShellToolName)
	assert.Contains(t, before, tools.ToolRequestMachine)

	daemonID := "daemon-connected"
	require.NoError(t, f.h.Repo().UpdateChatActiveDaemon(context.Background(), f.chat.ID, &daemonID))

	after := f.offeredTools(t, preloaded, nil)
	assert.Contains(t, after, tools.ShellToolName, "the next turn has the machine's tools")
	assert.Contains(t, after, tools.ToolView)
	assert.NotContains(t, after, tools.ToolRequestMachine)
}

// A machine tool named anyway (from history, or a hallucination) is refused
// before it reaches the executor, and the refusal is not a daemon-offline
// signal: the breaker that pauses a run after three "no daemon connected"
// results must never see one from a run that has no machine by design.
//
// Both with the turn's recorded capability set (which never offered them) and
// without one (a batch from before the set existed, where the chat row alone
// refuses them).
func TestExecuteTools_NoMachineRunRefusesMachineToolsWithoutTrippingTheBreaker(t *testing.T) {
	f := setupNoMachineFixture(t, true)
	f.offeredTools(t, []string{"tag:coding:default"}, []string{"*"})
	require.NotNil(t, f.caps)

	for _, tc := range []struct {
		name string
		caps *reliantv1.ToolCapabilities
	}{
		{"recorded set", f.caps.Proto()},
		{"no recorded set", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newMockToolExecutor()
			activity := NewExecuteToolsActivity(f.h.Repo(), executor)
			shellID, viewID := "call-shell-"+uuid.NewString(), "call-view-"+uuid.NewString()

			var output ExecuteToolsOutput
			require.NoError(t, f.h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
				ChatID:       f.chat.ID,
				Thread:       f.chat.ID,
				Capabilities: tc.caps,
				ToolCalls: []message.ToolCall{
					{ID: shellID, Name: tools.ShellToolName, Input: `{"command":"ls"}`},
					{ID: viewID, Name: tools.ToolView, Input: `{"file_path":"README.md"}`},
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
			assert.Zero(t, executor.GetExecutionCount(shellID), "a refused tool must never reach the executor")
			assert.Zero(t, executor.GetExecutionCount(viewID))
		})
	}
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
