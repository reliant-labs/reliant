package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// AutomationApprovalActivity previews through the exact tool factory that executes.
type AutomationApprovalActivity struct {
	factory  *tools.ToolsFactory
	executor *ExecuteToolsActivity
}

func (a *AutomationApprovalActivity) WithExecutionStore(repo ExecuteToolsStore) *AutomationApprovalActivity {
	a.executor = NewExecuteToolsActivity(repo, nil)
	return a
}

func NewAutomationApprovalActivity(f *tools.ToolsFactory) *AutomationApprovalActivity {
	return &AutomationApprovalActivity{factory: f}
}
func (*AutomationApprovalActivity) Name() string        { return "AutomationApprovalPreview" }
func (*AutomationApprovalActivity) DisplayName() string { return "Preview automation approval" }
func (*AutomationApprovalActivity) Description() string {
	return "Validate and describe an activation before asking its owner"
}
func (*AutomationApprovalActivity) Category() schema.ActivityCategory { return schema.CategoryAgentic }
func (a *AutomationApprovalActivity) Execute(ctx context.Context, in types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
	refuse := func(err error) (types.AutomationApprovalPreview, error) {
		// Validation errors can quote arbitrary input/graph values. Do not copy
		// those into another durable activity result or approval surface.
		return types.AutomationApprovalPreview{Refusal: "Activation could not be validated for approval; nothing changed. Check setup in the app."}, nil
	}
	if a.factory == nil {
		return refuse(fmt.Errorf("activation management unavailable"))
	}
	caps := tools.CapabilitiesFromProto(in.Capabilities)
	if caps.Binds(in.ToolName) {
		if a.executor == nil {
			return refuse(fmt.Errorf("activation preview binding resolver is unavailable"))
		}
		tec, reason := a.executor.loadToolExecutionContext(ctx, in.ChatID, in.Thread, in.ToolName, in.ToolInput, in.ToolCallID, "")
		if reason != "" {
			return refuse(fmt.Errorf("%s", reason))
		}
		input, reason := a.executor.boundToolInput(ctx, caps, tec)
		if reason != "" {
			return refuse(fmt.Errorf("%s", reason))
		}
		in.ToolInput = input
	}
	tool := a.factory.GetToolByName(in.ToolName, nil)
	approval, ok := tool.(tools.AutomationApprovalTool)
	if !ok {
		return refuse(fmt.Errorf("tool does not support activation approval"))
	}
	preview, _, err := approval.PrepareAutomation(rctx.NewToolContext(ctx, in.ChatID, in.Thread, nil, nil), tools.ToolCall{ID: in.ToolCallID, Name: in.ToolName, Input: in.ToolInput}, in.Unattended)
	if err != nil {
		return refuse(err)
	}
	return types.AutomationApprovalPreview{Title: preview.Title, Hash: preview.Hash}, nil
}
