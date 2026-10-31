package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// activationBackend stands in for the trigger service: it applies a change
// only when its policy (card) allows it, as ManageActivation does.
type activationBackend struct {
	calls int // applied changes
	user  string
	// card: the owner's approval setting needs a card for this change.
	card bool
}

func (b *activationBackend) PrepareActivation(context.Context, string, string, ActivationParams) (ActivationPreview, error) {
	return ActivationPreview{Title: "Owner, cicd (machine-1), workflow, privileges", Hash: "preview", CardRequired: b.card, Reason: "widens: identity:owner"}, nil
}
func (b *activationBackend) ManageActivation(ctx context.Context, user, op string, p ActivationParams, hash string) (TriggerSummary, error) {
	if b.card {
		if automationpolicy.From(ctx).Unattended {
			return TriggerSummary{}, svcerr.PermissionDenied(automationpolicy.Refusal())
		}
		if hash != "preview" {
			return TriggerSummary{}, svcerr.PermissionDenied(ApprovalRequiredMessage)
		}
	}
	b.calls++
	b.user = user
	return TriggerSummary{ID: "t", Name: "nightly", DaemonID: "machine-1", DaemonName: "cicd"}, nil
}

// A change the policy says needs a card asks for one, applies only with that
// call's approval, and is refused in an unattended run.
func TestActivationToolsCardRequired(t *testing.T) {
	for _, name := range []string{ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger} {
		t.Run(name, func(t *testing.T) {
			b := &activationBackend{card: true}
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
			require.Contains(t, resp.Content, ApprovalRequiredMessage)
			require.Zero(t, b.calls)
			_, _, err = tool.(AutomationApprovalTool).PrepareAutomation(ctx, call, true)
			require.ErrorContains(t, err, "an unattended run cannot show one")
			require.Contains(t, ActivationRefusalReason(err), "approval card")
			require.Zero(t, b.calls)
			ctx.Context = automationpolicy.WithExecution(ctx.Context, automationpolicy.Execution{Unattended: true, Approvals: map[string]string{"call": "preview"}})
			resp, err = tool.Run(ctx, call)
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, "unattended run cannot show one")
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

// A change the policy allows without a card (a pause, or one that does not
// widen the activation's power) asks for none and applies, attended or not.
func TestActivationToolsCardFree(t *testing.T) {
	for _, name := range []string{ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger, ToolPauseTrigger} {
		t.Run(name, func(t *testing.T) {
			for _, unattended := range []bool{false, true} {
				b := &activationBackend{}
				tool := NewActivationTool(triggerCallerStore{chat: &db.Chat{UserID: "owner"}}, b, name)
				ctx := rctx.NewToolContext(context.Background(), "chat", "thread", nil, nil)
				call := ToolCall{ID: "call", Name: name, Input: `{"trigger_id":"t"}`}
				_, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(ctx, call, unattended)
				require.NoError(t, err)
				require.False(t, ask, "no card")
				ctx.Context = automationpolicy.WithExecution(ctx.Context, automationpolicy.Execution{Unattended: unattended})
				resp, err := tool.Run(ctx, call)
				require.NoError(t, err)
				require.False(t, resp.IsError, resp.Content)
				require.Equal(t, 1, b.calls)
			}
		})
	}
}

// An unattended run reaches an activation tool only when its step names it;
// a tag or "*" does not sweep one in.
func TestActivationToolsUnattendedOptIn(t *testing.T) {
	for _, name := range []string{ToolCreateTriggerActivation, ToolUpdateTriggerActivation, ToolEnableTrigger, ToolPauseTrigger} {
		require.NotEmpty(t, (&Capabilities{Unattended: true}).withheldUnattended(name))
		require.Empty(t, (&Capabilities{Unattended: true, UnattendedOptIn: []string{name}}).withheldUnattended(name))
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

type recordingActivationBackend struct {
	activationBackend
	params ActivationParams
}

func (b *recordingActivationBackend) PrepareActivation(_ context.Context, _ string, _ string, p ActivationParams) (ActivationPreview, error) {
	b.params = p
	return ActivationPreview{Title: "t", Hash: "h", CardRequired: true}, nil
}

// The preview decodes a call exactly as Run does. A model that sends the
// open `params` object as a JSON string (Claude does) was refused at approval
// with no reason, though Run would have repaired it.
func TestActivationPrepareRepairsStringifiedParams(t *testing.T) {
	b := &recordingActivationBackend{}
	tool := NewActivationTool(triggerCallerStore{chat: &db.Chat{UserID: "owner"}}, b, ToolCreateTriggerActivation)
	ctx := rctx.NewToolContext(context.Background(), "chat", "thread", nil, nil)
	input := `{"name":"n","workflow":"pr-review","workflow_trigger":"queued","project_id":"p","machine_id":"m","params":"{\"repos\": [\"o/a\", \"o/b\"]}"}`
	_, ask, err := tool.(AutomationApprovalTool).PrepareAutomation(ctx, ToolCall{ID: "call", Name: ToolCreateTriggerActivation, Input: input}, false)
	require.NoError(t, err)
	require.True(t, ask)
	require.Equal(t, map[string]any{"repos": []any{"o/a", "o/b"}}, b.params.Params)
}

// A refusal names the field and the type it needs, never the value sent.
func TestActivationPrepareClassifiesBadInput(t *testing.T) {
	tool := NewActivationTool(triggerCallerStore{chat: &db.Chat{UserID: "owner"}}, &activationBackend{}, ToolCreateTriggerActivation)
	ctx := rctx.NewToolContext(context.Background(), "chat", "thread", nil, nil)
	_, _, err := tool.(AutomationApprovalTool).PrepareAutomation(ctx, ToolCall{ID: "call", Name: ToolCreateTriggerActivation, Input: `{"name":"n","params":["secret-value"]}`}, false)
	require.Error(t, err)
	reason := ActivationRefusalReason(err)
	require.Equal(t, "tool input field params must be a JSON object", reason)
	require.NotContains(t, reason, "secret-value")
}
