// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Story 13: an automation with NO MACHINE fires end to end.
//
// The user creates a scheduled trigger on the builtin agent with
// no_machine: true and tools: ["tag:web", "tag:planning"], and presses
// "run now". The fire goes through the real TriggerService, the Temporal fire
// workflow, the Firer, the launcher and DynamicWorkflow, with no daemon
// transport anywhere (DaemonRouter: nil; the tool executor is the production
// RemoteExecutor with no router).
//
// The agent is offered only tools that run on the server. It fetches a page
// from an httptest server, then — as a model working from habit might — names
// the shell. That call is refused before dispatch, the refusal is not a
// daemon-offline result, so the breaker never pauses the run, and the agent
// finishes. Before this change the same run was offered the shell and the file
// tools, every one of them failed with "no daemon connected", and three of
// those paused it.
func TestStory13_NoMachineAutomationFiresEndToEnd(t *testing.T) {
	t.Parallel()

	const page = "reliant-no-machine-story-13-marker"
	var hits atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, page)
	}))
	t.Cleanup(site.Close)

	script := NewScriptedLLM(
		Turn{ToolCalls: []message.ToolCall{
			ToolCall("call-fetch", tools.ToolFetch, fmt.Sprintf(`{"url":%q,"format":"text"}`, site.URL)),
		}},
		Turn{ToolCalls: []message.ToolCall{
			ToolCall("call-shell", tools.ShellToolName, `{"command":"ls"}`),
		}},
		Turn{Text: "Summary written."},
	)

	// The production executor with no daemon transport at all: server- and
	// any-placed tools run in-process, and nothing can reach a daemon.
	executor := toolexec.NewRemoteExecutor(nil)
	server := toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	executor.SetServerExecutor(server)

	// Counts every bind, so the story sees that a no-machine run never asked
	// for MCP tools — and does not depend on what MCP servers the host has.
	var mcpBinds atomic.Int32
	binder := toolexec.MCPContextBinderFunc(func(tc *rctx.ToolContext) *rctx.ToolContext {
		if !nomachine.Is(tc.Context) {
			mcpBinds.Add(1)
		}
		return tc
	})

	h := newHarness(t, script, WithToolExecutor(executor), WithMCPBinder(binder))

	toolsParam, err := structpb.NewValue([]any{"tag:web", "tag:planning"})
	require.NoError(t, err)
	modelParam, err := structpb.NewValue(map[string]any{"id": "mock"})
	require.NoError(t, err)
	modeParam := structpb.NewStringValue("auto")
	every := "1h"

	created, err := h.TriggerSvc.CreateTrigger(h.Ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: &reliantv1.TriggerDefinition{
			Name:      "morning digest",
			ProjectId: h.ProjectID,
			NoMachine: true,
			Workflow:  "builtin://agent",
			Message:   "Read the status page and summarise it.",
			Params:    map[string]*structpb.Value{"model": modelParam, "tools": toolsParam, "mode": modeParam},
			Source:    &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{Interval: &every}},
		},
	}))
	require.NoError(t, err, "a no-machine trigger on a server-only agent must be accepted")
	trigger := created.Msg.GetTrigger()
	require.True(t, trigger.GetNoMachine())
	require.Empty(t, trigger.GetDaemonId())

	_, err = h.TriggerSvc.FireTrigger(h.Ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: trigger.GetId()}))
	require.NoError(t, err)

	launched := core.TriggerEventLaunched
	var event *core.TriggerEvent
	require.Eventually(t, func() bool {
		ev, err := h.Stack.Repo.GetLatestTriggerEvent(h.Ctx, trigger.GetId(), &launched)
		if err == nil && ev != nil && ev.ChatID != nil {
			event = ev
			return true
		}
		return false
	}, 60*time.Second, 100*time.Millisecond, "the fire never launched a run")

	chat := h.Chat(*event.ChatID)
	assert.True(t, chat.NoMachine, "the launched chat records that it has no machine")
	assert.Nil(t, chat.ActiveDaemonID)

	require.NotNil(t, chat.WorkflowID)
	h.WaitTemporalWorkflowDone(*chat.WorkflowID)
	h.WaitWorkflowStatus(*chat.WorkflowID, db.Completed())

	// The menu: exactly what the trigger asked for, minus nothing, because
	// every web and planning tool runs without a machine — and no machine
	// tool at all, on any turn.
	calls := h.LLM.StreamCalls()
	require.Len(t, calls, 3, "fetch turn, refused-shell turn, final turn — and no pause in between")
	for i, call := range calls {
		for _, name := range call.ToolNames {
			// spawn is a workflow-level, schema-only tool the runtime handles
			// itself: a spawned agent shares this chat, so it inherits
			// no_machine and its own menu is filtered the same way.
			if name == "spawn" {
				continue
			}
			assert.False(t, tools.NeedsMachine(name), "turn %d offered %q, which needs a machine", i, name)
		}
		assert.Contains(t, call.ToolNames, tools.ToolFetch)
		assert.Contains(t, call.ToolNames, tools.ToolWebSearch)
		assert.Contains(t, call.ToolNames, tools.ToolCreatePlan)
		assert.NotContains(t, call.ToolNames, tools.ShellToolName)
	}
	assert.Zero(t, mcpBinds.Load(), "a no-machine run must never ask for MCP tools")

	// fetch ran on the server against the real HTTP server, and its body fed
	// the next turn.
	assert.EqualValues(t, 1, hits.Load())
	assert.True(t, historyHasToolResult(calls[1].Messages, page), "the fetched page must reach the second turn")

	// The shell call was refused, not "no daemon connected".
	var refusal string
	for _, m := range h.Messages(chat.ID, *chat.WorkflowID) {
		for _, b := range m.Blocks {
			if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && b.Content != nil &&
				strings.Contains(*b.Content, "Tool '"+tools.ShellToolName+"'") {
				refusal = *b.Content
			}
		}
	}
	require.NotEmpty(t, refusal, "the shell call must have a recorded result")
	assert.Equal(t, nomachine.Refusal(tools.ShellToolName), refusal)
	assert.False(t, daemonoffline.IsToolResultContent(refusal))

	assert.False(t, h.LLM.Exhausted(), "the agent must not request more turns than scripted")
}

// The chat half: a no-machine chat on the DEFAULT agent, whose tools are
// tag:coding:default (shell, view, edit, …). Unlike an automation it is not
// refused for that — someone is watching — but every turn is offered only the
// subset that runs without a machine, the model is told why, and nothing asks
// a machine or an MCP server for anything. This is the regression the whole
// change exists for: before, this run was offered the shell and the file tools.
func TestStory13_NoMachineChatIsOfferedOnlyServerTools(t *testing.T) {
	t.Parallel()

	executor := toolexec.NewRemoteExecutor(nil)
	executor.SetServerExecutor(toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	var mcpBinds atomic.Int32
	binder := toolexec.MCPContextBinderFunc(func(tc *rctx.ToolContext) *rctx.ToolContext {
		if !nomachine.Is(tc.Context) {
			mcpBinds.Add(1)
		}
		return tc
	})

	h := newHarness(t, NewScriptedLLM(Turn{Text: "Here is the plan."}), WithToolExecutor(executor), WithMCPBinder(binder))
	started := h.StartNoMachineChat("builtin://agent", "plan the launch", map[string]any{"mode": "auto"})
	require.True(t, started.Chat.GetNoMachine())

	h.WaitTemporalWorkflowDone(started.WorkflowId)
	h.WaitWorkflowStatus(started.WorkflowId, db.Completed())

	calls := h.LLM.StreamCalls()
	require.Len(t, calls, 1)
	offered := calls[0].ToolNames
	for _, name := range offered {
		if name == "spawn" {
			continue // schema-only; a spawned agent inherits no_machine
		}
		assert.False(t, tools.NeedsMachine(name), "a no-machine chat was offered %q, which needs a machine", name)
	}
	assert.Contains(t, offered, tools.ToolFetch)
	assert.Contains(t, offered, tools.ToolCreatePlan)
	for _, gone := range []string{tools.ShellToolName, tools.ToolView, tools.ToolEdit, tools.ToolWrite, tools.ToolCodeContext} {
		assert.NotContains(t, offered, gone)
	}
	assert.Zero(t, mcpBinds.Load(), "a no-machine chat must never ask for MCP tools")

	var told bool
	for _, p := range calls[0].Prompts {
		told = told || strings.Contains(p, "This run has no machine")
	}
	assert.True(t, told, "the model is told it has no machine")
}
