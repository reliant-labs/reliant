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
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
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

const reposWorkflowYAML = `
name: release
apiVersion: v2
inputs:
  model:
    type: model
  repos:
    type: github.repository
    multi: true
    required: true
    min_items: 1
  repo:
    type: string
    default: ''
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`

// Chat 34cf88e4 (2026-10-31): `reliant workflow run release -i
// repos=reliant-labs/control-plane` sent the repository as a string for a
// list-of-repositories input, and the run failed in its first second. A
// single valid item is a one-item list at the launch boundary; a value that is
// not a valid item is still refused before any run exists.
func TestLaunchWrapsASingleValidItemForAListInput(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "release", reposWorkflowYAML)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	params := mockModelParams(t)
	params["repos"] = structpb.NewStringValue("reliant-labs/control-plane")
	params["repo"] = structpb.NewStringValue("reliant-labs/control-plane")
	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindCLIRun, DedupeKey: "cli-repos", OccurredAt: time.Now().UTC()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "release",
		Params: params, Messages: userSeed("release control-plane"),
	})
	require.NoError(t, err)
	require.Len(t, starter.calls, 1)
	root, ok := starter.calls[0].args[0].(v2.WorkflowInput)
	require.True(t, ok, "the root start carries the run's inputs")
	assert.Equal(t, []any{"reliant-labs/control-plane"}, root.Inputs["repos"])
	assert.Equal(t, "reliant-labs/control-plane", root.Inputs["repo"], "a string input is left alone")

	params["repos"] = structpb.NewStringValue("not a repository")
	_, err = launcher.Launch(ctx, Event{Kind: core.TriggerEventKindCLIRun, DedupeKey: "cli-repos-bad", OccurredAt: time.Now().UTC()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "release",
		Params: params, Messages: userSeed("release control-plane"),
	})
	var validation *ValidationError
	require.ErrorAs(t, err, &validation)
	assert.Contains(t, validation.Error(), "input 'repos' expects an array of github.repository values, got string")
	assert.Len(t, starter.calls, 1, "a refused launch starts nothing")
	chats, err := repo.ListChats(ctx, db.ChatFilters{UserID: launchTestUserID, ProjectID: &projectID})
	require.NoError(t, err)
	assert.Len(t, chats, 1, "a refused launch writes no run")
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
