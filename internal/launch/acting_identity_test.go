// Copyright (c) 2025 Reliant Labs
package launch

import (
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

func launchAs(t *testing.T, ev Event, spec Spec) map[string]any {
	t.Helper()
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	spec.OwnerUserID, spec.ProjectID, spec.Workflow = launchTestUserID, projectID, "builtin://agent"
	spec.Params, spec.Messages = mockModelParams(t), userSeed("go")
	_, err := launcher.Launch(ctx, ev, spec)
	require.NoError(t, err)
	_, input := starter.rootRun(t)
	return input.Inputs
}

func triggerEvent(kind core.TriggerEventKind) Event {
	return Event{Kind: kind, DedupeKey: "evt-" + uuid.NewString()}
}

// An automation whose activation chose the App records it, and the automation's
// name, on the run it starts.
func TestLaunch_ActingAsAppIsRecordedOnTheRun(t *testing.T) {
	testkit.Slow(t, "launches a DB-backed run per trigger kind")
	for _, kind := range []core.TriggerEventKind{
		core.TriggerEventKindSchedule, core.TriggerEventKindWebhook, core.TriggerEventKindIntegration, core.TriggerEventKindWorkflowEvent,
	} {
		t.Run(string(kind), func(t *testing.T) {
			inputs := launchAs(t, triggerEvent(kind), Spec{ActingIdentity: core.ActingIdentityApp, Automation: "pr-review"})
			assert.Equal(t, "app", v2.ActingIdentity(inputs))
			assert.Equal(t, "pr-review", inputs[v2.InputKeyAutomation])
		})
	}
}

// The default, and every owner activation, records nothing: absence IS the owner.
func TestLaunch_OwnerIdentityRecordsNothing(t *testing.T) {
	for name, identity := range map[string]core.ActingIdentity{"unset": "", "owner": core.ActingIdentityOwner} {
		t.Run(name, func(t *testing.T) {
			inputs := launchAs(t, triggerEvent(core.TriggerEventKindSchedule), Spec{ActingIdentity: identity, Automation: "nightly"})
			assert.NotContains(t, inputs, v2.InputKeyActingIdentity)
			assert.NotContains(t, inputs, v2.InputKeyAutomation)
		})
	}
}

// The bot is an AUTOMATION's identity. A person's own chat start never carries
// it, even if a caller set the field: only an unattended launch can speak as it.
func TestLaunch_AHumanChatStartNeverCarriesTheBotIdentity(t *testing.T) {
	inputs := launchAs(t, chatStartEvent(), Spec{ActingIdentity: core.ActingIdentityApp, Automation: "pr-review"})
	assert.NotContains(t, inputs, v2.InputKeyActingIdentity)
	assert.NotContains(t, inputs, v2.InputKeyAutomation)
	assert.False(t, v2.IsUnattended(inputs))
}

// A client cannot claim the identity through request params: it is an
// engine-injected input, refused from a client like `unattended`.
func TestLaunch_ClientParamsCannotWriteTheBotIdentity(t *testing.T) {
	params := mockModelParams(t)
	params[v2.InputKeyActingIdentity] = structpb.NewStringValue("app")
	params[v2.InputKeyAutomation] = structpb.NewStringValue("forged")

	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: params, Messages: userSeed("hi"),
	})
	require.NoError(t, err)
	_, input := starter.rootRun(t)
	assert.NotContains(t, input.Inputs, v2.InputKeyActingIdentity)
	assert.NotContains(t, input.Inputs, v2.InputKeyAutomation)
}

// The runtime validates inputs again inside the workflow; both keys are
// declared engine-injected so a bot-identity launch is not rejected as unknown.
func TestLaunch_ActingIdentityKeysAreEngineInjectedInputs(t *testing.T) {
	assert.True(t, workflowRuntimeInjected(v2.InputKeyActingIdentity))
	assert.True(t, workflowRuntimeInjected(v2.InputKeyAutomation))
}

func workflowRuntimeInjected(key string) bool { return workflow.RuntimeInjectedInputs[key] }
