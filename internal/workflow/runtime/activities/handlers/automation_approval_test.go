// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A refusal says why, in its classification's words, and never echoes the
// input: "could not be validated; check setup in the app" left agents and
// owners with nothing to fix.
func TestAutomationApprovalRefusalNamesTheReason(t *testing.T) {
	a := NewAutomationApprovalActivity(tools.NewToolsFactory(nil))
	out, err := a.Execute(context.Background(), types.AutomationApprovalInput{
		ChatID: "chat", Thread: "thread", ToolCallID: "call",
		ToolName:  tools.ToolCreateTriggerActivation,
		ToolInput: `{"name":"n","workflow":"pr-review","workflow_trigger":"queued","params":{"token":"secret-value"}}`,
	})
	require.NoError(t, err)
	require.Empty(t, out.Hash)
	require.Equal(t, "Activation could not be validated for approval: the calling chat could not be resolved. Nothing changed.", out.Refusal)
	require.NotContains(t, out.Refusal, "secret-value")
}
