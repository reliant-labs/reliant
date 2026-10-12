// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

func TestWithWaitForMachine(t *testing.T) {
	t.Run("empty access is unchanged", func(t *testing.T) {
		assert.Equal(t, tools.ToolAccess{}, withWaitForMachine(tools.ToolAccess{}))
	})
	t.Run("web-only preloaded is unchanged", func(t *testing.T) {
		in := tools.ToolAccess{Preloaded: []string{tools.ToolFetch, tools.ToolWebSearch}}
		assert.Equal(t, in, withWaitForMachine(in))
	})
	t.Run("machine tool preloaded adds it without mutating the input", func(t *testing.T) {
		backing := make([]string, 1, 4)
		backing[0] = tools.ShellToolName
		in := tools.ToolAccess{Preloaded: backing}
		out := withWaitForMachine(in)
		assert.Equal(t, []string{tools.ShellToolName, tools.ToolWaitForMachine}, out.Preloaded)
		assert.Equal(t, []string{tools.ShellToolName}, in.Preloaded)
		assert.Equal(t, []string{tools.ShellToolName}, backing[:cap(backing)][:1])
		assert.Empty(t, backing[:cap(backing)][1], "the shared backing array was not written")
	})
	t.Run("machine tool loadable adds it", func(t *testing.T) {
		out := withWaitForMachine(tools.ToolAccess{Loadable: []string{tools.ToolView}})
		assert.Equal(t, []string{tools.ToolWaitForMachine}, out.Preloaded)
	})
	t.Run("LoadableAll adds it", func(t *testing.T) {
		out := withWaitForMachine(tools.ToolAccess{LoadableAll: true})
		assert.Equal(t, []string{tools.ToolWaitForMachine}, out.Preloaded)
	})
	t.Run("already present is not duplicated", func(t *testing.T) {
		in := tools.ToolAccess{Preloaded: []string{tools.ShellToolName, tools.ToolWaitForMachine}}
		assert.Equal(t, in, withWaitForMachine(in))
	})
}

type fakeMachineObserver struct{}

func (fakeMachineObserver) MachineStatus(context.Context, string) (tools.MachineStatus, error) {
	return tools.MachineStatus{Connected: true}, nil
}

// Only a remote machine can drop mid-turn, so only a factory that observes one
// offers wait_for_machine.
func TestCallLLM_WaitForMachineIsOfferedOnlyWhereTheMachineIsRemote(t *testing.T) {
	preloaded := []string{tools.ShellToolName}

	local := setupNoMachineFixture(t, false)
	assert.NotContains(t, local.offeredTools(t, preloaded, nil), tools.ToolWaitForMachine)

	remote := setupNoMachineFixture(t, false)
	remote.callLLM.toolsFactory = observingFactory(remote)
	require.Contains(t, remote.offeredTools(t, preloaded, nil), tools.ToolWaitForMachine)

	noMachine := setupNoMachineFixture(t, true)
	noMachine.callLLM.toolsFactory = observingFactory(noMachine)
	assert.NotContains(t, noMachine.offeredTools(t, preloaded, nil), tools.ToolWaitForMachine)
}

func observingFactory(f *noMachineFixture) *tools.ToolsFactory {
	return tools.NewToolsFactory(&tools.ToolsOptions{Repo: f.h.Repo(), MachineObserver: fakeMachineObserver{}})
}

const offlineContent = "Failed to execute tool on daemon: unavailable: no daemon connected for user"

// A daemon-offline error gets the wait_for_machine hint appended — in what the
// model reads and in the durable row — only when the turn was offered the tool.
func TestExecuteTools_OfflineResultGetsWaitHintOnlyWhenOffered(t *testing.T) {
	offered := &reliantv1.ToolCapabilities{Offered: []string{tools.ShellToolName, tools.ToolWaitForMachine}, Permission: "mutating"}
	notOffered := &reliantv1.ToolCapabilities{Offered: []string{tools.ShellToolName}, Permission: "mutating"}

	run := func(t *testing.T, caps *reliantv1.ToolCapabilities, result *toolexec.ToolResult) (string, string) {
		f := setupDurableStatusFixture(t)
		defer f.h.Cleanup()
		f.h.CreateTestWorkflow(context.Background(), f.chatID, f.chatID)

		id := "toolu_" + uuid.NewString()
		executor := newMockToolExecutor()
		executor.SetResult(id, result)

		var output ExecuteToolsOutput
		require.NoError(t, f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), executor).Execute, ExecuteToolsInput{
			ChatID:       f.chatID,
			Thread:       f.chatID,
			Capabilities: caps,
			ToolCalls:    []message.ToolCall{{ID: id, Name: tools.ShellToolName, Input: `{"command":"ls"}`}},
		}, &output))
		require.Len(t, output.ToolResults, 1)

		durable := getToolCallResult(t, f.h, id)
		require.NotNil(t, durable)
		return output.ToolResults[0].GetContent(), durable.Content
	}
	offline := func() *toolexec.ToolResult {
		return &toolexec.ToolResult{Success: false, IsError: true, Content: offlineContent}
	}

	t.Run("offered", func(t *testing.T) {
		model, durable := run(t, offered, offline())
		assert.Equal(t, offlineContent+machineOfflineHint, model)
		assert.Equal(t, model, durable, "the durable row carries the same text the model sees")
		assert.Contains(t, model, "no daemon connected", "the breaker's substring survives")
		assert.Contains(t, model, "wait_for_machine")
	})
	t.Run("not offered", func(t *testing.T) {
		model, durable := run(t, notOffered, offline())
		assert.Equal(t, offlineContent, model)
		assert.Equal(t, offlineContent, durable)
	})
	t.Run("no recorded capabilities", func(t *testing.T) {
		model, _ := run(t, nil, offline())
		assert.Equal(t, offlineContent, model)
	})
	t.Run("other errors are unchanged", func(t *testing.T) {
		model, durable := run(t, offered, &toolexec.ToolResult{Success: false, IsError: true, Content: "permission denied"})
		assert.Equal(t, "permission denied", model)
		assert.Equal(t, "permission denied", durable)
	})
}
