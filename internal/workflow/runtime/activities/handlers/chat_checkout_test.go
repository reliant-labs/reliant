package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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
	assert.Contains(t, result.GetContent(), "Retrying will not help: stop calling tools that need the machine",
		"the model is told to stop and ask the user, not to loop on the refusal")
	assert.NotContains(t, result.GetContent(), "the next call checks again")
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

// A thread bound to the project's MAIN worktree row (spawn(worktree:
// "main")) runs in the main checkout of the machine the call goes to. The
// row's path mirrors projects.path, the creating machine's: sent as the
// thread's workspace it named a directory the cloud machine does not have,
// and the agent was told its workspace was missing while its calls ran in
// the main checkout anyway.
func TestThreadBoundToTheMainRowRunsInTheMachinesOwnCheckout(t *testing.T) {
	t.Parallel()

	f := newSpawnWorktreeFixture(t)
	ctx := context.Background()
	const cloudCheckout = "/srv/b/p"
	f.installOn(t, "daemon-b", cloudCheckout)
	pinned := "daemon-b"
	require.NoError(t, f.repo.UpdateChatActiveDaemon(ctx, f.chatID, &pinned))
	now := time.Now().UTC()
	mainRow := &db.Worktree{
		ID: uuid.NewString(), Name: "main", Path: "/home/u/projects/p", Branch: "main", BaseBranch: "main",
		ProjectID: f.projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, f.repo.CreateWorktree(ctx, mainRow))

	spawned := f.spawn(t, "main", "")
	require.Empty(t, spawned.Refusal)
	assert.Empty(t, spawned.WorktreePath, "the main row's path is not the child's working directory")

	// The child inherited its parent's path: a branch workspace.
	inherited := "/home/u/.reliant/worktrees/p/feature"
	executor := &daemonLikeExecutor{}
	a := NewExecuteToolsActivity(f.repo, executor)
	rtx := &types.RuntimeContext{ChatID: f.chatID, Thread: spawned.ThreadID, ProjectPath: inherited}
	got := a.ensureBatchWorkspace(ctx, rtx, []message.ToolCall{bashCall("pwd")}, func(message.ToolCall) bool { return false })
	require.Equal(t, 1, executor.ensureCount())
	assert.Equal(t, cloudCheckout, executor.ensures[0].Path, "checked at the cloud machine's checkout")
	assert.Equal(t, cloudCheckout, got.path, "and every call of the batch runs there")
	require.NotNil(t, executor.selectors[0])
	assert.Equal(t, "daemon-b", executor.selectors[0].ID)
	assert.Empty(t, got.note, "no 'workspace missing' notice for a checkout that is there")

	exec := &capturingExecutor{mockToolExecutor: *newMockToolExecutor()}
	act := NewExecuteToolsActivity(f.repo, exec)
	fn := func(ctx context.Context) (message.ToolResult, error) {
		return act.executeSingleTool(ctx, nil, f.chatID, spawned.ThreadID, "shell", `{"command":"pwd"}`,
			"tc-"+uuid.NewString(), "act", "run", 1, inherited, nil), nil
	}
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(fn)
	_, err := env.ExecuteActivity(fn)
	require.NoError(t, err)
	require.NotNil(t, exec.got)
	assert.Equal(t, cloudCheckout, exec.got.WorktreePath)
	assert.Equal(t, cloudCheckout, exec.got.ProjectPath)
	require.NotNil(t, exec.got.DaemonSelector)
	assert.Equal(t, "daemon-b", exec.got.DaemonSelector.ID)
}

// checkoutRows is a projectcheckout.Reader over fixed rows, every machine the
// user's.
type checkoutRows []*core.ProjectDaemon

func (r checkoutRows) ListProjectDaemonsForProject(context.Context, string) ([]*core.ProjectDaemon, error) {
	return r, nil
}

func (checkoutRows) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	return &db.Daemon{ID: id, UserID: "u"}, nil
}

// With no machine chosen up front (no router, no selector) the call is pinned
// to the machine its directory was resolved from: left to default resolution
// at dispatch, it could reach a machine that connected in between with this
// one's path.
func TestResolveCallCheckout_NoMachineChosenPinsTheCallToTheCheckoutsMachine(t *testing.T) {
	t.Parallel()
	rows := checkoutRows{{DaemonID: "cloud", Path: "/srv/p", InstallState: core.ProjectInstallInstalled}}
	project := &db.Project{ID: "p", UserID: "u", Name: "p", Path: "/Users/creator/p"}

	c := resolveCallCheckout(context.Background(), rows, nil, project, nil, true)
	require.NoError(t, c.missing)
	assert.Equal(t, "/srv/p", c.main.Path)
	sel := c.pin(nil)
	require.NotNil(t, sel)
	assert.Equal(t, "cloud", sel.ID)

	// A selector by name is the caller's choice: it is never replaced by
	// the oldest install's machine.
	byName := &toolexec.DaemonSelector{Name: "build-box"}
	assert.Same(t, byName, resolveCallCheckout(context.Background(), rows, nil, project, byName, true).pin(byName))
}

// namedMachineRouter resolves a selector by name the way the real router does.
type namedMachineRouter struct {
	toolexec.DaemonRouter
	byName map[string]string
}

func (r namedMachineRouter) ResolveDaemonIDForSelector(_ context.Context, _ string, sel *toolexec.DaemonSelector) (string, error) {
	if sel != nil && sel.ID != "" {
		return sel.ID, nil
	}
	if id, ok := r.byName[sel.Name]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no machine named %q", sel.Name)
}

// A run node that names its machine by name runs in THAT machine's checkout,
// pinned to it: not the oldest install's path while the command routes by
// name to another machine.
func TestResolveRunCheckout_AMachineNamedByNameGetsItsOwnCheckout(t *testing.T) {
	t.Parallel()
	rows := checkoutRows{
		{DaemonID: "laptop", Path: "/Users/creator/p", InstallState: core.ProjectInstallInstalled},
		{DaemonID: "build-machine", Path: "/srv/build/p", InstallState: core.ProjectInstallInstalled},
	}
	execCtx := RunExecutorContext{UserID: "u", project: &db.Project{ID: "p", UserID: "u", Name: "p"},
		DaemonSelector: &toolexec.DaemonSelector{Name: "build-box"}}
	router := namedMachineRouter{byName: map[string]string{"build-box": "build-machine"}}

	require.NoError(t, resolveRunCheckout(context.Background(), rows, router, &execCtx))
	assert.Equal(t, "/srv/build/p", execCtx.ProjectPath)
	require.NotNil(t, execCtx.DaemonSelector)
	assert.Equal(t, "build-machine", execCtx.DaemonSelector.ID, "pinned to the machine whose path it carries")
}
