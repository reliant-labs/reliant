// Copyright (c) 2025 Reliant Labs
package launch

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// A no-machine launch records the fact on the chat — where every activity
// reads it — and pins no daemon, so nothing downstream resolves or wakes one.
func TestLaunchNoMachineRecordsItOnTheChat(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("summarise the news"), NoMachine: true,
	})
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	assert.True(t, chat.NoMachine)
	assert.Nil(t, chat.ActiveDaemonID)
	_, input := starter.rootRun(t)
	assert.NotContains(t, input.Inputs, "session_daemon_id")
}

// A workflow that cannot run at all without a machine is refused before
// anything is written, with the reason, rather than started and left to fail.
func TestLaunchNoMachineRefusesAWorkflowThatHardRequiresAMachine(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://parallel-compete",
		Params: mockModelParams(t), Messages: userSeed("go"), NoMachine: true,
	})
	require.Error(t, err)
	var validation *ValidationError
	require.True(t, errors.As(err, &validation), "got %T: %v", err, err)
	assert.Equal(t, ValidationFailedPrecondition, validation.Kind)
	assert.Contains(t, validation.Error(), "needs a machine")
	assert.Empty(t, starter.calls, "a refused launch starts nothing")
}

// A client cannot write an input the engine injects. Before, every key in
// RuntimeInjectedInputs passed straight from request params into the run's
// inputs (validation skips them because the engine sets them), so a StartChat
// could claim a session daemon or a trigger event — and `unattended`, which
// must be exempt from validation for trigger runs to start at all, would have
// let any chat declare itself unattended.
func TestLaunchClientParamsCannotWriteEngineInjectedInputs(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	params := mockModelParams(t)
	params["unattended"] = structpb.NewBoolValue(true)
	params["session_daemon_id"] = structpb.NewStringValue("someone-elses-daemon")
	params["__trigger"] = structpb.NewStringValue("forged")

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: params, Messages: userSeed("hi"),
	})
	require.NoError(t, err)
	_, input := starter.rootRun(t)
	assert.NotContains(t, input.Inputs, "unattended", "only a trigger launch makes a run unattended")
	assert.NotContains(t, input.Inputs, "session_daemon_id")
	assert.NotContains(t, input.Inputs, "__trigger")
}

// An unattended launch — every trigger fire — carries the flag, and the
// runtime's own input validation accepts it (it is engine-injected, like
// session_daemon_id). Before, it was rejected there as an unknown input, so
// every trigger fire of a workflow with declared inputs failed.
func TestLaunchUnattendedIsAnEngineInjectedInput(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, Event{Kind: core.TriggerEventKindSchedule, DedupeKey: uuid.NewString()}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"), Unattended: true,
	})
	require.NoError(t, err)
	_, input := starter.rootRun(t)
	assert.Equal(t, true, input.Inputs["unattended"])
	assert.True(t, workflow.RuntimeInjectedInputs["unattended"],
		"the runtime's input validation must not reject what the launcher injects")
}

// NoMachine and DaemonID contradict each other.
func TestLaunchNoMachineWithADaemonIsInvalid(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("go"), NoMachine: true, DaemonID: "daemon-x",
	})
	var validation *ValidationError
	require.True(t, errors.As(err, &validation), "got %T: %v", err, err)
	assert.Equal(t, ValidationInvalidArgument, validation.Kind)
}

// A branch worktree is a checkout on one machine, so a chat with no machine
// cannot run in one; it binds to the project's main checkout, which carries no
// daemon, as every no-machine chat does.
func TestLaunchNoMachineRefusesAMachineBoundWorktree(t *testing.T) {
	repo, ctx, projectID, mainWorktreeID := launchFixture(t)
	launcher, _ := newTestLauncher(t, repo, &fakeStarter{})

	daemonID := "daemon-" + uuid.NewString()
	branchWorktreeID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: branchWorktreeID, Name: "feature", Path: t.TempDir(), Branch: "feature", BaseBranch: "main",
		ProjectID: projectID, DaemonID: &daemonID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent", WorktreeID: &branchWorktreeID,
		Params: mockModelParams(t), Messages: userSeed("go"), NoMachine: true,
	})
	var validation *ValidationError
	require.True(t, errors.As(err, &validation), "got %T: %v", err, err)
	assert.Contains(t, validation.Error(), "lives on a machine")

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent", WorktreeID: &mainWorktreeID,
		Params: mockModelParams(t), Messages: userSeed("go"), NoMachine: true,
	})
	require.NoError(t, err, "the main checkout carries no daemon")
	assert.Equal(t, mainWorktreeID, *result.Chat.WorktreeID)
}

// A trigger's no-machine run is checked against the workflow it will run: the
// project's, which an automation resolves first, not the owner's account
// workflow of the same name. The account one here runs a shell command and so
// needs a machine; the project one does not, and the fire must start.
func TestLaunchNoMachineAutomationChecksTheProjectWorkflowItRuns(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "digest", `
name: digest
apiVersion: v2
entry: [sh]
nodes:
  - id: sh
    type: run
    args:
      command: echo account
`)
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{
		Slug: "digest", Name: "digest", RelativePath: ".reliant/workflows/digest.yaml",
		YAMLContent: "name: digest\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: project}\n",
	}})
	require.NoError(t, err)
	laptop := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: laptop, UserID: launchTestUserID}))
	_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: laptop, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, true)
	require.NoError(t, err)
	now := time.Now().UTC()
	triggerID := uuid.NewString()
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: launchTestUserID, ProjectID: projectID, Name: "digest",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "digest",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: now, UpdatedAt: now,
	}))

	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	_, err = launcher.Launch(ctx, Event{
		Kind: core.TriggerEventKindSchedule, TriggerID: triggerID, DedupeKey: "fire-" + uuid.NewString(), OccurredAt: now,
	}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "digest",
		Messages: userSeed("go"), Unattended: true, NoMachine: true,
	})
	require.NoError(t, err, "the project workflow that runs needs no machine")
	assert.Len(t, starter.calls, 1)

	// A chat start keeps the account-first rule, and so the account
	// workflow's machine requirement.
	_, err = launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "digest",
		Messages: userSeed("go"), NoMachine: true,
	})
	var validation *ValidationError
	require.ErrorAs(t, err, &validation)
	assert.Contains(t, validation.Error(), "needs a machine")
}

// An automation's precedence is read from how its chat was launched, which
// survives deleting the trigger; the trigger id does not.
func TestAutomationPrecedenceSurvivesTriggerDeletion(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	now := time.Now().UTC()
	triggerID := uuid.NewString()
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: launchTestUserID, ProjectID: projectID, Name: "nightly",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: now, UpdatedAt: now,
	}))
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	result, err := launcher.Launch(ctx, Event{
		Kind: core.TriggerEventKindSchedule, TriggerID: triggerID, DedupeKey: "fire-" + uuid.NewString(), OccurredAt: now,
	}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("go"), Unattended: true,
	})
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	assert.True(t, AutomationPrecedence(chat))
	require.NoError(t, repo.DeleteTrigger(ctx, triggerID))
	chat, err = repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	require.Nil(t, chat.TriggerID, "deleting the trigger clears the chat's trigger id")
	assert.True(t, AutomationPrecedence(chat), "and leaves the run an automation")
}

// A trigger's first launch builds and validates its inputs against the
// project's workflow, as tree admission and the run's own loads read it: the
// owner's same-named account workflow, which requires an input the project's
// defaults, must neither reject the fire nor supply its defaults.
func TestLaunchAutomationBuildsInputsFromTheProjectWorkflow(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	seedCompleteWorkflow(t, ctx, repo, "deploy", "name: deploy\napiVersion: v2\ninputs:\n  env: {type: string, required: true}\n  owner: {type: string, default: mine}\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: account}\n")
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{
		Slug: "deploy", Name: "deploy", RelativePath: ".reliant/workflows/deploy.yaml",
		YAMLContent: "name: deploy\napiVersion: v2\ninputs:\n  env: {type: string, default: prod}\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: project}\n",
	}})
	require.NoError(t, err)
	laptop := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: laptop, UserID: launchTestUserID}))
	_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: laptop, ProjectWorkflowsJSON: wj, PushedAt: time.Now().UTC(),
	}, true)
	require.NoError(t, err)
	now := time.Now().UTC()
	triggerID := uuid.NewString()
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: launchTestUserID, ProjectID: projectID, Name: "deploy",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "deploy",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: now, UpdatedAt: now,
	}))

	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	_, err = launcher.Launch(ctx, Event{
		Kind: core.TriggerEventKindSchedule, TriggerID: triggerID, DedupeKey: "fire-" + uuid.NewString(), OccurredAt: now,
	}, Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "deploy",
		Messages: userSeed("go"), Unattended: true,
	})
	require.NoError(t, err, "the account workflow's required env must not reject the project workflow's fire")
	inputs := starter.lastRootInput(t).Inputs
	assert.Equal(t, "prod", inputs["env"], "the project workflow's default")
	assert.NotContains(t, inputs, "owner", "the account workflow's defaults are not the run's")
}
