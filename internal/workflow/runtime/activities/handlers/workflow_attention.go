package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// recordAttention rechecks the authoritative launch identity, rather than
// trusting an activity payload to turn an interactive turn into an automation.
func (a *WorkflowStatusActivity) recordAttention(ctx context.Context, input WorkflowStatusInput) (core.AttentionResult, error) {
	if input.Attention == nil || input.ParentWorkflowID != "" {
		return core.AttentionResult{}, nil
	}
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return core.AttentionResult{}, err
	}
	if chat == nil {
		return core.AttentionResult{}, fmt.Errorf("attention chat does not exist")
	}
	if !core.TriggerEventKind(chat.LaunchKind).Unattended() {
		return core.AttentionResult{}, nil
	}
	// Starts predate the LaunchRun terminal marker in the existing status input;
	// their policy update itself is root-launch-gated by the runtime. A terminal
	// update must also have that established marker.
	if input.Status != "started" && !input.LaunchRun {
		return core.AttentionResult{}, nil
	}
	update := *input.Attention
	update.ChatID = input.ChatID
	if input.Status == "started" {
		update.Phase = "start"
	} else if input.Status == "completed" || input.Status == "failed" || input.Status == "cancelled" {
		update.Phase = "finish"
	} else {
		return core.AttentionResult{}, nil
	}
	return a.repo.ApplyAttentionUpdate(ctx, update)
}
