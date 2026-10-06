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
