// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	wfruntime "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// creditExhaustedFailure is a provider refusal no retry fixes: the account's
// credit is spent. CallLLM fails it at once (ProviderCreditExhaustion), so a
// sub-agent that meets it stops and reports to its parent.
func creditExhaustedFailure() error {
	return errors.New(`POST "https://api.example.com/v1/messages": 429 Too Many Requests: You exceeded your current quota, please check your plan and billing details (insufficient_quota)`)
}

// TestGenerateFixture_CapacityWakeResumesStoppedAgent pins the chat-wide
// capacity wake (capacity-wake-resumes-chat): a sub-agent stops on spent credit
// and reports to its parent; while the parent reads the failure the user's
// provider gains capacity and "Retry now" goes through the production
// RetryUsageLimitWait handler, which now wakes any live run. The wake resumes
// the stopped agent on its own thread (its row back to "started"), its next
// CallLLM is served, it reports a completion under a fresh slot, and the parent
// reacts and finishes — no message to the agent.
//
// The shape before the gate (the wake ignored, the agent left stopped) is
// frozen in fixtures/frozen/2026-10-23-capacity-wake-ignored.
func TestGenerateFixture_CapacityWakeResumesStoppedAgent(t *testing.T) {
	spawnCall := ToolCall("call-spawn-1", "spawn", `{"preset":"general","prompt":"`+subAgentFailureMarker+` Implement the change."}`)

	var h *Harness
	var restored atomic.Bool
	chatIDs := make(chan string, 1)
	parentWaiting := make(chan struct{})
	woke := make(chan error, 1)
	script := NewScriptedLLM(
		// Turn 1: the parent delegates.
		Turn{Text: "I'll delegate this to a sub-agent.", ToolCalls: []message.ToolCall{spawnCall}},
		// Turn 2: the parent's exit candidate; it parks on the live agent.
		Turn{Text: "Waiting on the sub-agent.", During: func() { close(parentWaiting) }},
		// Turn 3: the parent reads the failure; capacity returns meanwhile.
		Turn{Text: "The sub-agent ran out of credit.", During: func() {
			restored.Store(true)
			resp, err := h.ChatSvc.RetryUsageLimitWait(h.Ctx, connect.NewRequest(&reliantv1.RetryUsageLimitWaitRequest{ChatId: <-chatIDs}))
			if err == nil && !resp.Msg.Woken {
				err = fmt.Errorf("RetryUsageLimitWait did not wake the run")
			}
			woke <- err
		}},
		// Turn 4: the resumed agent's turn.
		Turn{Text: "Picked up where I left off; the change is done."},
		// Turn 5: the parent reacts to the resumed agent's completion.
		Turn{Text: "The sub-agent finished after the top-up."},
	)
	script.FailWhen(func(msgs []message.Message) error {
		if !conversationMentions(msgs, subAgentFailureMarker) || restored.Load() {
			return nil
		}
		<-parentWaiting
		return creditExhaustedFailure()
	})
	h = newHarness(t, script)

	created := h.StartChat("builtin://agent", "Please delegate a task to a sub-agent", map[string]any{
		"mode": "auto",
	})
	workflowID := created.WorkflowId
	chatIDs <- created.Chat.Id

	require.NoError(t, <-woke, "the capacity wake must reach the run")
	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	// The agent's row: stopped, resumed by the wake, completed. Its spawn call
	// ended once, with the failure, and stays that way.
	h.WaitWorkflowStatus(wfruntime.DeterministicWorkflowID(workflowID, spawnCall.ID), db.Completed())
	h.RequireToolCallStatus(spawnCall.ID, core.ToolCallStatusFailed)

	h.ExportHistory(workflowID, "capacity_wake_resumes_stopped_agent")
}
