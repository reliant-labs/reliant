// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The fixture's user has only the Anthropic API key connected, so "anthropic"
// is the run's primary provider and the fallback goes to Codex.
func setPolicy(t *testing.T, ctx context.Context, service *Service, chatID string, onLimit reliantv1.ProviderPolicy_OnLimit, fallback string) {
	t.Helper()
	policy := &reliantv1.ProviderPolicy{OnLimit: onLimit}
	if fallback != "" {
		policy.FallbackModel = &fallback
	}
	_, err := service.UpdateChatProviderPolicy(ctx, connect.NewRequest(&reliantv1.UpdateChatProviderPolicyRequest{ChatId: chatID, ProviderPolicy: policy}))
	require.NoError(t, err)
}

func policyDefaults(t *testing.T, ctx context.Context, repo *db.Repo, value string) {
	t.Helper()
	require.NoError(t, repo.SetString(ctx, "test-user", nil, launch.ProviderPolicyDefaultsKey, value))
}

// A chat nobody configured runs with no policy at all: cross-provider fallback
// is OFF and the run is what it was before this feature.
func TestSendMessage_NoPolicyInjectsNothing(t *testing.T) {
	ctx, fx, _, temporal, service := selectionFixture(t, db.Completed())
	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)
	assert.NotContains(t, temporal.lastStart(t).Inputs, v2.InputKeyProviderPolicy)
}

// "This chat falls back to Codex": a run built from the chat carries the
// policy, whatever device or path built it.
func TestSendMessage_ChatPolicyReachesTheRun(t *testing.T) {
	ctx, fx, _, temporal, service := selectionFixture(t, db.Completed())
	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, codexOnlyModel)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)

	got := v2.ProviderPolicy(temporal.lastStart(t).Inputs)
	assert.Equal(t, types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: codexOnlyModel}, got)
}

// The Settings default applies when the chat made no choice, for the provider
// the run is on; the chat's own choice beats it.
func TestSendMessage_SettingsDefaultAppliesUnlessTheChatChose(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	policyDefaults(t, ctx, repo, `{"anthropic": {"on_limit":"fallback","fallback_model":"`+codexOnlyModel+`"}}`)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)
	assert.Equal(t, types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: codexOnlyModel}, v2.ProviderPolicy(temporal.lastStart(t).Inputs))

	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_WAIT, "")
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)
	assert.Equal(t, types.RunProviderPolicy{OnLimit: "wait"}, v2.ProviderPolicy(temporal.lastStart(t).Inputs))
}

// The default is for ANOTHER provider: this chat's provider has none, so it waits.
func TestSendMessage_AnotherProvidersDefaultDoesNotApply(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	policyDefaults(t, ctx, repo, `{"codex": {"on_limit":"fallback","fallback_model":"claude-5.5-opus@anthropic"}}`)

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, nil))
	require.NoError(t, err)
	assert.NotContains(t, temporal.lastStart(t).Inputs, v2.InputKeyProviderPolicy)
}

// A client cannot smuggle a policy in through a send's params.
func TestSendMessage_ClientCannotSetTheRunPolicy(t *testing.T) {
	ctx, fx, _, temporal, service := selectionFixture(t, db.Completed())
	forged := paramsOf(t, map[string]any{v2.InputKeyProviderPolicy: map[string]any{"on_limit": "fallback", "fallback_model": codexOnlyModel}})

	_, err := service.SendMessage(ctx, sendContinue(fx.chatID, forged))
	require.NoError(t, err)
	assert.NotContains(t, temporal.lastStart(t).Inputs, v2.InputKeyProviderPolicy)
}

// A live run is told when the chat's policy changes, so the next limit it meets
// follows the new one. A run that already has it is not signalled.
func TestUpdateChatProviderPolicy_SignalsTheLiveRun(t *testing.T) {
	ctx, fx, _, temporal, service := selectionFixture(t, db.Active())

	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, codexOnlyModel)

	// The policy, then a capacity wake so every thread blocked on a provider
	// re-decides under it: a live run's blocked threads are not visible from
	// the chat, and a wake that finds none is a no-op.
	require.Len(t, temporal.signals, 2, "the live run must hear about the policy, then be woken")
	assert.Equal(t, v2.CapacityWakeSignalName, temporal.signals[1].Name)
	last := temporal.signals[0]
	assert.Equal(t, workflow.SignalUpdateWorkflowState, last.Name)
	update, ok := last.Arg.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: codexOnlyModel},
		types.RunProviderPolicyFromValue(update[v2.InputKeyProviderPolicy]))
	assert.Len(t, update, 1, "only the policy is signalled")
}

// With no live run there is nothing to signal; the policy waits in the
// selection for the next run.
func TestUpdateChatProviderPolicy_NoLiveRunNoSignal(t *testing.T) {
	ctx, fx, _, temporal, service := selectionFixture(t, db.Completed())
	setPolicy(t, ctx, service, fx.chatID, reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK, codexOnlyModel)
	assert.Empty(t, temporal.signals)
}
