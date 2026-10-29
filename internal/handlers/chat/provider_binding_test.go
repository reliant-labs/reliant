// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

type bindingLocalDirectory struct{}

func (bindingLocalDirectory) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return []local.DaemonInventory{{DaemonID: "d1", Online: true, Inventory: &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{Id: "ollama", Kind: "ollama", Models: []*reliantv1.LocalModelInfo{{Name: "qwen", SupportsChat: true}}}}}}}, nil
}

func TestLocalRootPickerClearsBindingAndResumeReconcilesIt(t *testing.T) {
	ctx, fx, repo, temporal, svc := selectionFixture(t, db.Active())
	svc.deps.LocalModels = bindingLocalDirectory{}
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	_, err = svc.storeChatProvider(ctx, "test-user", chat, "builtin://agent", "codex")
	require.NoError(t, err)
	_, err = svc.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"id": "qwen@local", "providers": []any{"local:d1"}}})))
	require.NoError(t, err)
	require.Empty(t, svc.chatProvider(ctx, fx.chatID))
	require.Equal(t, "", temporal.signals[0].Arg.(map[string]interface{})[v2.InputKeyProviderSwitch])
	replayUpdate := map[string]interface{}{"model": map[string]any{"id": "qwen@local"}}
	svc.refreshLiveChatProvider(ctx, "test-user", chat, "builtin://agent", replayUpdate)
	require.Equal(t, "", replayUpdate[v2.InputKeyProviderSwitch], "reset must explicitly remove the history's old binding")
}

func TestOrdinaryBoundSendCannotMoveModelsOrClearBinding(t *testing.T) {
	ctx, fx, repo, _, svc := selectionFixture(t, db.Active())
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	_, err = svc.storeChatProvider(ctx, "test-user", chat, "builtin://agent", "codex")
	require.NoError(t, err)
	for _, id := range []string{sonnetOnClaude, "qwen@local"} {
		inputs := map[string]interface{}{"model": map[string]any{"id": id}, v2.InputKeyProviderSwitch: "codex"}
		require.Empty(t, svc.moveUnservableModels(ctx, "test-user", chat, "builtin://agent", fx.rootThreadID, inputs))
		svc.refreshChatProvider(ctx, "test-user", chat, "builtin://agent", inputs)
		require.Equal(t, "codex", inputs[v2.InputKeyProviderSwitch])
		require.Equal(t, "codex", svc.chatProvider(ctx, fx.chatID))
	}
}

func TestUnavailableTaggedProviderPickIsRejected(t *testing.T) {
	ctx, fx, _, temporal, svc := selectionFixture(t, db.Active())
	_, err := svc.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"tags": []any{"fast"}, "providers": []any{"codex"}}})))
	require.Error(t, err)
	require.Empty(t, temporal.signals, "tag/provider selection must not fall through to the connected anthropic account")
}

func TestPickerVersionConflictCannotPartiallyChangeBinding(t *testing.T) {
	ctx, fx, repo, _, svc := selectionFixture(t, db.Active())
	db.SeedProviderAccount(t, repo, "test-user", "codex", "codex-key")
	original := setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": opusOnClaude}})
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "auto"})
	req := updateParams(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"id": "gpt-6.1-sol@codex"}}))
	req.Msg.ExpectedVersion = &original.Version
	_, err := svc.UpdateWorkflowParams(ctx, req)
	require.Error(t, err)
	selected, err := repo.GetChatSelection(ctx, fx.chatID)
	require.NoError(t, err)
	require.Empty(t, selected.ChatProvider)
	require.Equal(t, opusOnClaude, selected.Params["model"].(map[string]interface{})["id"])
}

func TestFreshStoredModelDoesNotBindChat(t *testing.T) {
	ctx, fx, repo, _, svc := selectionFixture(t, db.Completed())
	for _, model := range []any{map[string]any{"id": opusOnClaude}, map[string]any{"tags": []any{"fast"}}, map[string]any{"id": "my-model@local"}} {
		setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": model})
		_, err := chatselection.New(repo).SetPolicy(ctx, fx.chatID, "test-user", "builtin://agent", chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitFallback, FallbackModel: "gpt-6.1-sol@codex"}, 0)
		require.NoError(t, err)
		chat, err := repo.GetChat(ctx, fx.chatID)
		require.NoError(t, err)
		inputs := svc.launcher().BuildChatWorkflowInputs(ctx, "test-user", chat, "builtin://agent", nil, nil)
		require.NotContains(t, inputs, v2.InputKeyProviderSwitch)
		require.Equal(t, "fallback", v2.ProviderPolicy(inputs).OnLimit)
	}
}

func TestExplicitSwitchBindingSurvivesSelectionWritesAndFreshLaunch(t *testing.T) {
	ctx, fx, repo, _, svc := selectionFixture(t, db.Active())
	db.SeedProviderAccount(t, repo, "test-user", "codex", "test-codex")
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": opusOnClaude}})
	_, err := svc.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"id": "gpt-6.1-sol@codex"}})))
	require.NoError(t, err)
	require.Equal(t, "codex", svc.chatProvider(ctx, fx.chatID))
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"mode": "auto"})
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	inputs := svc.launcher().BuildChatWorkflowInputs(ctx, "test-user", chat, "builtin://agent", nil, nil)
	require.Equal(t, "codex", inputs[v2.InputKeyProviderSwitch])
}

func TestTagOnlyRootPickDoesNotBindChat(t *testing.T) {
	ctx, fx, repo, temporal, svc := selectionFixture(t, db.Active())
	setSelection(t, ctx, repo, fx.chatID, map[string]any{"model": map[string]any{"id": opusOnClaude}})
	_, err := svc.UpdateWorkflowParams(ctx, updateParams(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"tags": []any{"fast"}}})))
	require.NoError(t, err)
	require.Empty(t, svc.chatProvider(ctx, fx.chatID))
	for _, sig := range temporal.signals {
		if update, ok := sig.Arg.(map[string]interface{}); ok {
			require.NotContains(t, update, v2.InputKeyProviderSwitch)
		}
	}
}
