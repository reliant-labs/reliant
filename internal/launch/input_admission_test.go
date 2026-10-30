// Copyright (c) 2025 Reliant Labs
package launch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const releaseWorkflowYAML = `
name: release-forge
apiVersion: v2
automation_only: true
inputs:
  model:
    type: model
  asap:
    type: boolean
    default: true
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`

// Bugs fe84af3c / 5b73a559 / run 658dbba8: admission read a boolean input
// sent as the text "true" as a boolean, while the runtime refused it, so the
// run was created and failed in the same second with no node and nothing on
// it to say why. Admission applies the runtime's rule: the start is refused
// before anything is written, and the caller is told why.
func TestLaunchRefusesTextForABooleanInputBeforeCreatingARun(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "release-forge", releaseWorkflowYAML)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	params := mockModelParams(t)
	params["asap"] = structpb.NewStringValue("true")
	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindAgentStartRun, DedupeKey: "manual-asap", OccurredAt: time.Now().UTC()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "release-forge",
		Params: params, Messages: userSeed("release now"),
	})
	require.Error(t, err)
	var validation *ValidationError
	require.True(t, errors.As(err, &validation), "got %T: %v", err, err)
	assert.Contains(t, validation.Error(), `input 'asap' expects boolean, got string "true"`)
	assert.Empty(t, starter.calls, "a refused launch starts nothing")
	chats, err := repo.ListChats(ctx, db.ChatFilters{UserID: launchTestUserID, ProjectID: &projectID})
	require.NoError(t, err)
	assert.Empty(t, chats, "a refused launch writes no run")

	// A boolean is a boolean: the same start goes through, and the run holds it.
	params["asap"] = structpb.NewBoolValue(true)
	_, err = launcher.Launch(ctx, Event{Kind: core.TriggerEventKindAgentStartRun, DedupeKey: "manual-asap-2", OccurredAt: time.Now().UTC()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "release-forge",
		Params: params, Messages: userSeed("release now"),
	})
	require.NoError(t, err)
	require.Len(t, starter.calls, 1)
}

func TestLaunchRefusesOverflowingIntegerAfterDefaults(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	yaml := strings.Replace(releaseWorkflowYAML, "type: boolean\n    default: true", "type: integer\n    default: 1", 1)
	seedCompleteWorkflow(t, ctx, repo, "release-forge", yaml)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	params := mockModelParams(t)
	params["asap"] = structpb.NewNumberValue(0x1p63)
	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindAgentStartRun, DedupeKey: "overflow", OccurredAt: time.Now().UTC()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "release-forge",
		Params: params, Messages: userSeed("release now"),
	})
	var validation *ValidationError
	require.ErrorAs(t, err, &validation)
	require.Contains(t, validation.Error(), "expects integer")
	require.Equal(t, float64(0x1p63), params["asap"].GetNumberValue())
	require.Empty(t, starter.calls)
	chats, err := repo.ListChats(ctx, db.ChatFilters{UserID: launchTestUserID, ProjectID: &projectID})
	require.NoError(t, err)
	require.Empty(t, chats)
}

// Input validation that cannot read the workflow it validates against fails,
// rather than passing inputs nobody checked.
func TestValidateChatWorkflowInputsFailsWhenTheWorkflowDoesNotResolve(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

	errs := launcher.ValidateChatWorkflowInputs(ctx, launchTestUserID, &db.Chat{ProjectID: projectID}, "no-such-workflow", map[string]any{"asap": "true"})
	require.NotEmpty(t, errs, "an unresolvable workflow is not a pass")
	assert.Contains(t, errs[0].Error(), "no-such-workflow")
}
