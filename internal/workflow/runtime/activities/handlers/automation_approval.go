package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
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
		// Validation errors can quote arbitrary input/graph values, so the
		// cause goes to the server log only (with the names of what was
		// activated, never the input). The caller gets the classification,
		// which names fields and inputs, never their values.
		reason := tools.ActivationRefusalReason(err)
		if reason == "" {
			reason = "unclassified " + strings.ToLower(svcerr.Code(err).String()) + " error (logged server-side)"
		}
		// Only the names are logged. Unmarshal fills them even when another
		// field has the wrong type.
		var names tools.ActivationParams
		_ = json.Unmarshal([]byte(in.ToolInput), &names)
		logging.Warn("Activation approval refused",
			"tool", in.ToolName, "chat_id", in.ChatID, "tool_call_id", in.ToolCallID,
			"workflow_ref", names.Workflow, "workflow_trigger", names.WorkflowTrigger, "trigger_id", names.TriggerID,
			"reason", reason, "code", svcerr.Code(err).String(), "error", err)
		subject := "Activation"
		if in.ToolName == tools.ToolManageMachine {
			subject = "Machine change"
		}
		return types.AutomationApprovalPreview{Refusal: subject + " could not be validated for approval: " + reason + ". Nothing changed."}, nil
	}
	if a.factory == nil {
		return refuse(tools.RefuseActivation("activation management is unavailable on this server", fmt.Errorf("no tools factory")))
	}
	caps := tools.CapabilitiesFromProto(in.Capabilities)
	if caps.Binds(in.ToolName) {
		if a.executor == nil {
			return refuse(tools.RefuseActivation("tool bindings cannot be resolved on this server", fmt.Errorf("no binding resolver")))
		}
		tec, reason := a.executor.loadToolExecutionContext(ctx, in.ChatID, in.Thread, in.ToolName, in.ToolInput, in.ToolCallID, "")
		if reason != "" {
			return refuse(tools.RefuseActivation("tool bindings could not be resolved", fmt.Errorf("%s", reason)))
		}
		input, reason := a.executor.boundToolInput(ctx, caps, tec)
		if reason != "" {
			return refuse(tools.RefuseActivation("tool bindings could not be resolved", fmt.Errorf("%s", reason)))
		}
		in.ToolInput = input
	}
	tool := a.factory.GetToolByName(in.ToolName, nil)
	approval, ok := tool.(tools.AutomationApprovalTool)
	if !ok {
		return refuse(tools.RefuseActivation("this tool does not support activation approval", fmt.Errorf("%s is not an AutomationApprovalTool", in.ToolName)))
	}
	preview, needsApproval, err := approval.PrepareAutomation(rctx.NewToolContext(ctx, in.ChatID, in.Thread, nil, nil), tools.ToolCall{ID: in.ToolCallID, Name: in.ToolName, Input: in.ToolInput}, in.Unattended)
	if err != nil {
		return refuse(err)
	}
	return types.AutomationApprovalPreview{Title: preview.Title, Hash: preview.Hash, NoApproval: !needsApproval}, nil
}
