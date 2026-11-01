package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// chatCheckoutFixture is a chat whose project was created on testMachine at
// creationDir, plus a second machine of the same user.
type chatCheckoutFixture struct {
	h           *IdempotencyTestHelper
	user        string
	project     string
	chat        string
	creationDir string
}

func newChatCheckoutFixture(t *testing.T) *chatCheckoutFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	f := &chatCheckoutFixture{h: h, user: uuid.NewString(), project: uuid.NewString(), chat: uuid.NewString(), creationDir: t.TempDir()}
	ctx := context.Background()
	h.CreateTestProjectWithPath(ctx, f.project, f.user, f.creationDir)
	h.CreateTestChat(ctx, f.chat, f.project, f.user)
	return f
}

// machine registers a machine of the user's, holding the project at
// checkout ("" for none).
func (f *chatCheckoutFixture) machine(t *testing.T, id, checkout string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.h.Repo().UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: f.user}))
	if checkout != "" {
		require.NoError(t, f.h.Repo().UpsertProjectDaemon(ctx, f.project, id, checkout, nil))
	}
}

func (f *chatCheckoutFixture) pin(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, f.h.Repo().UpdateChatActiveDaemon(context.Background(), f.chat, &id))
}

// run executes one bash call as an in-flight run whose path was computed on
// the machine that created the project.
func (f *chatCheckoutFixture) run(t *testing.T, executor *daemonLikeExecutor) *reliantv1.ToolResultMsg {
	t.Helper()
	executor.requests, executor.ensures, executor.selectors = nil, nil, nil
	var output ExecuteToolsOutput
	err := f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), executor).Execute, ExecuteToolsInput{
		ChatID: f.chat, Thread: f.chat, ProjectPath: f.creationDir,
		ToolCalls: []message.ToolCall{bashCall("pwd")},
	}, &output)
	require.NoError(t, err, "a missing checkout never fails the activity: the conversation carries on")
	require.Len(t, output.ToolResults, 1)
	return output.ToolResults[0]
}

// A chat that moved to another machine runs at THAT machine's checkout: the
// run's path, computed where the project was created, is rebased onto it, and
// the call is pinned to the machine whose path it carries.
func TestExecuteTools_AChatThatMovedMachinesRunsAtTheNewMachinesCheckout(t *testing.T) {
	f := newChatCheckoutFixture(t)
	cloudDir := t.TempDir()
	f.machine(t, "cloud-box", cloudDir)
	f.pin(t, "cloud-box")

	executor := &daemonLikeExecutor{}
	result := f.run(t, executor)
	require.False(t, result.GetIsError(), result.GetContent())

	require.Len(t, executor.ensures, 1)
	assert.Equal(t, cloudDir, executor.ensures[0].Path, "the workspace check looks at the cloud machine's checkout")
	require.Len(t, executor.requests, 1)
	req := executor.requests[0]
	assert.Equal(t, cloudDir, req.WorktreePath, "rebased off the creating machine's path")
	assert.Equal(t, cloudDir, req.ProjectPath, "the project root a daemon-side tool sees is its own")
	require.NotNil(t, req.DaemonSelector)
	assert.Equal(t, "cloud-box", req.DaemonSelector.ID)
}

// A chat on a machine with no checkout is not a stuck run: each machine call
// answers with the reason and the way out, nothing runs, and putting the
// project there unblocks the very next call with no restart.
func TestExecuteTools_NoCheckoutOnTheChatsMachineRefusesWithTheWayOutThenRecovers(t *testing.T) {
	f := newChatCheckoutFixture(t)
	f.machine(t, "bare-box", "")
	f.pin(t, "bare-box")

	executor := &daemonLikeExecutor{}
	result := f.run(t, executor)
	assert.True(t, result.GetIsError())
	assert.Contains(t, result.GetContent(), "isn't checked out on")
	assert.Contains(t, result.GetContent(), "bare-box")
	assert.Contains(t, result.GetContent(), "switch to", "names the machine that has it")
	assert.Contains(t, result.GetContent(), "the next call checks again")
	assert.Empty(t, executor.requests, "no call reached a machine")
	assert.NotContains(t, result.GetContent(), f.creationDir, "the creating machine's path is never offered as this machine's")

	cloned := t.TempDir()
	require.NoError(t, f.h.Repo().UpsertProjectDaemon(context.Background(), f.project, "bare-box", cloned, nil))
	result = f.run(t, executor)
	require.False(t, result.GetIsError(), result.GetContent())
	require.Len(t, executor.requests, 1)
	assert.Equal(t, cloned, executor.requests[0].WorktreePath)
}

// A run step on a machine with no checkout fails as a command with the reason
// (exit -1), never a stuck or retried step, and never runs anywhere.
func TestRunStep_NoCheckoutOnTheRunsMachineFailsTheCommandWithTheWayOut(t *testing.T) {
	f := newChatCheckoutFixture(t)
	f.machine(t, "bare-box", "")
	f.pin(t, "bare-box")

	executor := &mockRunExecutor{}
	act := NewExecuteRunStepActivity(f.h.Repo(), nil, executor)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(act.Execute)
	val, err := env.ExecuteActivity(act.Execute, ExecuteRunStepInput{WorkflowID: "wf-1", ChatID: f.chat, StepID: "gate", Command: "task ci"})
	require.NoError(t, err)
	var out ExecuteRunStepOutput
	require.NoError(t, val.Get(&out))

	assert.Equal(t, -1, out.ExitCode)
	assert.Contains(t, out.Output, "isn't checked out on")
	assert.Contains(t, out.Output, "bare-box")
	assert.Empty(t, executor.calls, "the command never ran")
}

// A run step runs in its machine's own checkout, pinned to that machine.
func TestRunStep_RunsInTheMachinesOwnCheckout(t *testing.T) {
	f := newChatCheckoutFixture(t)
	cloudDir := t.TempDir()
	f.machine(t, "cloud-box", cloudDir)
	f.pin(t, "cloud-box")

	executor := &mockRunExecutor{responses: []mockRunResponse{{stdout: "ok"}}}
	act := NewExecuteRunStepActivity(f.h.Repo(), nil, executor)
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(act.Execute)
	_, err := env.ExecuteActivity(act.Execute, ExecuteRunStepInput{WorkflowID: "wf-1", ChatID: f.chat, StepID: "gate", Command: "task ci"})
	require.NoError(t, err)

	require.NotEmpty(t, executor.calls)
	assert.Equal(t, cloudDir, executor.calls[0].workingDir)
}
