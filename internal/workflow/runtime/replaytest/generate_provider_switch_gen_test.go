// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// A usage-waiting spawn receives a root model-picker update and the #1371
// wake. The new workflow retries on the chosen provider without scheduling a
// concrete-model lease notice; the descendant keeps its original tags. The
// pre-fix history with that extra notice is frozen for replay coverage.
func TestGenerateFixture_ProviderSwitchWaitingSpawn(t *testing.T) {
	call := ToolCall("provider-switch-spawn", "spawn", `{"preset":"general","prompt":"`+subAgentFailureMarker+` Implement the change."}`)
	var capacity atomic.Bool
	script := NewScriptedLLM(
		Turn{Text: "Delegating.", ToolCalls: []message.ToolCall{call}},
		Turn{Text: "Waiting for the agent."},
		Turn{Text: "The agent finished."},
		Turn{Text: "All done."},
	)
	script.FailWhen(func(msgs []message.Message) error {
		if !conversationMentions(msgs, subAgentFailureMarker) || capacity.Load() {
			return nil
		}
		return fmt.Errorf("%s", chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit,
			time.Now().Add(4*time.Hour).UTC().Format(time.RFC3339),
			chatmarkers.ProviderUsageLimitLead+" (Anthropic)"))
	})
	h := newHarness(t, script)
	created := h.StartChat("builtin://agent", "Delegate this work.", map[string]any{"mode": "auto"})
	thread := runtime.DeterministicWorkflowID(created.WorkflowId, call.ID)
	h.eventually("the spawned agent's usage wait", func() (bool, string) {
		marks, err := h.Stack.Repo.ProviderBackoffByChat(h.Ctx, created.Chat.Id)
		return err == nil && marks[thread].Waiting(), fmt.Sprintf("markers: %v, error: %v", marks, err)
	})
	capacity.Store(true)
	require.NoError(t, h.Stack.Temporal.SignalWorkflow(h.Ctx, created.WorkflowId, "", "update_workflow_state",
		map[string]interface{}{"model": map[string]interface{}{"id": "gpt-6.1-sol@codex"}, runtime.InputKeyProviderSwitch: "codex"}))
	require.NoError(t, h.Stack.Temporal.SignalWorkflow(h.Ctx, created.WorkflowId, "", runtime.CapacityWakeSignalName,
		runtime.CapacityWakeSignal{UseModel: "gpt-6.1-sol@codex"}))
	h.WaitTemporalWorkflowDone(created.WorkflowId)
	h.WaitWorkflowStatus(created.WorkflowId, db.Completed())
	h.ExportHistory(created.WorkflowId, "provider_switch_waiting_spawn")
}
