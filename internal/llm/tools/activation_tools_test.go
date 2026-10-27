package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

type activationBackend struct {
	calls int
	user  string
}

func (b *activationBackend) PrepareActivation(context.Context, string, string, ActivationParams) (ActivationPreview, error) {
	return ActivationPreview{Title: "Owner, cicd (machine-1), workflow, privileges", Hash: "preview"}, nil
}
func (b *activationBackend) ManageActivation(_ context.Context, user, op string, p ActivationParams, hash string) (TriggerSummary, error) {
	b.calls++
	b.user = user
	return TriggerSummary{ID: "t", Name: "nightly", DaemonID: "machine-1", DaemonName: "cicd"}, nil
}
func TestActivationToolsAuthorization(t *testing.T) {
	for _, name := range []string{ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger, ToolPauseTrigger} {
		t.Run(name, func(t *testing.T) {
			b := &activationBackend{}
			tool := NewActivationTool(triggerCallerStore{chat: &db.Chat{UserID: "owner"}}, b, name)
			ctx := rctx.NewToolContext(context.Background(), "chat", "thread", nil, nil)
			call := ToolCall{ID: "call", Name: name, Input: `{"trigger_id":"t"}`}
			preview, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(ctx, call, false)
			require.NoError(t, err)
			require.True(t, ask)
			require.Contains(t, preview.Title, "cicd")
			require.Zero(t, b.calls)
			resp, err := tool.Run(ctx, call)
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Zero(t, b.calls)
			_, _, err = tool.(AutomationApprovalTool).PrepareAutomation(ctx, call, true)
			require.ErrorContains(t, err, "Unattended")
			require.Zero(t, b.calls)
			ctx.Context = automationpolicy.WithExecution(ctx.Context, automationpolicy.Execution{Unattended: true, Approvals: map[string]string{"call": "preview"}})
			resp, err = tool.Run(ctx, call)
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Zero(t, b.calls, "even an approved call cannot escalate an unattended run")
			ctx.Context = automationpolicy.WithExecution(ctx.Context, automationpolicy.Execution{Approvals: map[string]string{"other": "preview"}})
			resp, err = tool.Run(ctx, call)
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Zero(t, b.calls, "approval is call-scoped")
			ctx.Context = automationpolicy.WithExecution(ctx.Context, automationpolicy.Execution{Approvals: map[string]string{"call": "preview"}})
			resp, err = tool.Run(ctx, call)
			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)
			require.Equal(t, 1, b.calls)
			require.Equal(t, "owner", b.user)
			require.Contains(t, resp.Content, "cicd (id machine-1)")
		})
	}
}
func TestActivationToolsUnattendedCannotOptIn(t *testing.T) {
	for _, name := range []string{ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger, ToolPauseTrigger} {
		caps := &Capabilities{Unattended: true, UnattendedOptIn: []string{name}}
		require.NotEmpty(t, caps.withheldUnattended(name))
	}
}

func TestActivationToolsRegistered(t *testing.T) {
	factory := NewToolsFactory(nil)
	for _, name := range []string{ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger, ToolPauseTrigger} {
		tool := factory.GetToolByName(name, nil)
		require.NotNil(t, tool)
		require.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(name))
		require.NotNil(t, tool.ParamSchema())
	}
}
