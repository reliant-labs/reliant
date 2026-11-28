// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// multiProjectChat is a chat on cloud-box whose primary is checked out there
// (primaryDir) with a project "api" attached (apiDir, on cloud-box too).
type multiProjectChat struct {
	*chatCheckoutFixture
	primaryDir string
	api        *db.Project
	apiDir     string
}

func newMultiProjectChat(t *testing.T) *multiProjectChat {
	t.Helper()
	f := &multiProjectChat{chatCheckoutFixture: newChatCheckoutFixture(t), primaryDir: t.TempDir(), apiDir: t.TempDir()}
	f.machine(t, "cloud-box", f.primaryDir)
	f.pin(t, "cloud-box")
	f.api = f.attach(t, "api", "cloud-box", f.apiDir)
	return f
}

// attach creates a project of the user's named name, checks it out on
// machine at dir ("" for nowhere) and attaches it to the chat.
func (f *multiProjectChat) attach(t *testing.T, name, machine, dir string) *db.Project {
	t.Helper()
	ctx := context.Background()
	p := &db.Project{ID: uuid.NewString(), UserID: f.user, Name: name, Path: "/creator/" + name}
	require.NoError(t, f.h.Repo().CreateProject(ctx, p))
	if dir != "" {
		require.NoError(t, f.h.Repo().UpsertProjectDaemon(ctx, p.ID, machine, dir, nil))
	}
	require.NoError(t, f.h.Repo().AttachChatProject(ctx, &core.ChatProject{ChatID: f.chat, ProjectID: p.ID, DaemonID: &machine, CreatedAt: time.Now()}))
	return p
}

// call runs one tool call in the chat and returns its result and the request
// the executor received (nil when none did).
func (f *multiProjectChat) call(t *testing.T, name, input string) (*reliantv1.ToolResultMsg, *message.ToolCall, *toolexec.ToolRequest) {
	t.Helper()
	executor := &daemonLikeExecutor{}
	call := message.ToolCall{ID: "toolu_" + uuid.NewString(), Name: name, Input: input}
	var output ExecuteToolsOutput
	act := NewExecuteToolsActivity(tools.WithChats(f.h.Repo()), workspaceRowsOf(f.h.Repo()), toolCallsOf(f.h.Repo()), executor).WithChatProjects(f.h.Repo())
	require.NoError(t, f.h.ExecuteActivity(act.Execute, ExecuteToolsInput{
		ChatID: f.chat, Thread: f.chat, ProjectPath: f.primaryDir, ToolCalls: []message.ToolCall{call},
	}, &output), "a refusal never fails the activity: the conversation carries on")
	require.Len(t, output.ToolResults, 1)
	if len(executor.requests) == 0 {
		return output.ToolResults[0], &call, nil
	}
	require.Len(t, executor.requests, 1)
	return output.ToolResults[0], &call, executor.requests[0]
}

func TestRouteCallProject_OmittedProjectIsTheSingleProjectRequestExactly(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	input := `{"command": "pwd"}`
	result, _, req := f.call(t, tools.ShellToolName, input)
	require.False(t, result.GetIsError(), result.GetContent())
	require.NotNil(t, req)
	assert.Equal(t, input, req.ToolInput, "byte for byte the model's input")
	assert.Equal(t, f.project, req.ProjectID)
	assert.Equal(t, f.primaryDir, req.ProjectPath)
	assert.Equal(t, f.primaryDir, req.WorktreePath)
}

func TestRouteCallProject_NamingThePrimaryRunsThereWithoutTheKey(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	result, _, req := f.call(t, tools.ShellToolName, `{"command":"pwd","project":"Test Project"}`)
	require.False(t, result.GetIsError(), result.GetContent())
	require.NotNil(t, req)
	assert.JSONEq(t, `{"command":"pwd"}`, req.ToolInput)
	assert.Equal(t, f.project, req.ProjectID)
	assert.Equal(t, f.primaryDir, req.WorktreePath)
}

// An attached project's call runs in its main checkout on the chat's machine,
// with its repos, and reaches the executor without `project`; the recorded
// call keeps it, so the UI can say where it ran.
func TestRouteCallProject_AnAttachedProjectRunsInItsMainCheckoutOnTheChatsMachine(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	ctx := context.Background()
	require.NoError(t, f.h.Repo().CreateRepo(ctx, &core.Repo{ID: uuid.NewString(), ProjectID: f.api.ID, Name: "svc", RelativePath: "svc"}))
	// The chat works in a branch worktree of its primary; the attached
	// project never does.
	f.h.CreateTestWorktree(ctx, f.project, f.chat, t.TempDir())

	result, call, req := f.call(t, tools.ToolView, `{"file_path":"README.md","project":"api","repo":"svc"}`)
	require.False(t, result.GetIsError(), result.GetContent())
	require.NotNil(t, req)
	assert.JSONEq(t, `{"file_path":"README.md","repo":"svc"}`, req.ToolInput, "project never reaches a daemon")
	assert.Equal(t, f.api.ID, req.ProjectID)
	assert.Equal(t, "api", req.ProjectName)
	assert.Equal(t, f.apiDir, req.ProjectPath)
	assert.Equal(t, f.apiDir, req.WorktreePath, "its main checkout, not the primary's worktree or run path")
	assert.Empty(t, req.WorktreeID)
	require.Len(t, req.Repos, 1)
	assert.Equal(t, "svc", req.Repos[0].Name)
	require.NotNil(t, req.DaemonSelector)
	assert.Equal(t, "cloud-box", req.DaemonSelector.ID, "on the machine the chat runs on")

	recorded, err := f.h.Repo().GetToolCall(ctx, call.ID)
	require.NoError(t, err)
	var in map[string]any
	require.NoError(t, json.Unmarshal(recorded.Input, &in))
	assert.Equal(t, "api", in[tools.ProjectParam], "the record keeps the model's input")
}

func TestRouteCallProject_AnUnknownProjectIsRefusedWithTheAcceptedValues(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	result, _, req := f.call(t, tools.ShellToolName, `{"command":"pwd","project":"web"}`)
	assert.Nil(t, req, "nothing ran")
	assert.True(t, result.GetIsError())
	assert.Contains(t, result.GetContent(), `"web" is not one of this chat's projects`)
	assert.Contains(t, result.GetContent(), `"Test Project" (the primary)`)
	assert.Contains(t, result.GetContent(), `"api"`)
}

// The one-machine rule is re-checked per call: an attached project the chat's
// machine has no checkout of refuses the call with the way out, and only
// that project's calls stop.
func TestRouteCallProject_AnAttachedProjectNotOnTheMachineIsRefused(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	f.attach(t, "docs", "cloud-box", "")

	result, _, req := f.call(t, tools.ShellToolName, `{"command":"pwd","project":"docs"}`)
	assert.Nil(t, req, "nothing ran")
	assert.True(t, result.GetIsError())
	assert.Contains(t, result.GetContent(), `"docs" isn't checked out on`)
	assert.Contains(t, result.GetContent(), "other projects still run")

	result, _, req = f.call(t, tools.ShellToolName, `{"command":"pwd","project":"api"}`)
	require.False(t, result.GetIsError(), result.GetContent())
	require.NotNil(t, req)
	assert.Equal(t, f.apiDir, req.WorktreePath)
}

// The batch's workspace check is the primary's: when it blocks the batch, a
// call to an attached project still runs, and one to the primary — named or
// not — is refused with the reason.
func TestRouteCallProject_TheBatchWorkspaceBlockIsThePrimarysAlone(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	const blocked = "This workspace is archived with preserved work."
	run := func(input string) (message.ToolResult, *daemonLikeExecutor) {
		executor := &daemonLikeExecutor{}
		act := NewExecuteToolsActivity(tools.WithChats(f.h.Repo()), workspaceRowsOf(f.h.Repo()), toolCallsOf(f.h.Repo()), executor).WithChatProjects(f.h.Repo())
		call := message.ToolCall{ID: "toolu_" + uuid.NewString(), Name: tools.ShellToolName, Input: input}
		var result message.ToolResult
		fn := func(ctx context.Context) error {
			result = act.runToolCall(ctx, RuntimeContext{ChatID: f.chat, Thread: f.chat}, call, nil, "", false, nil, blocked, "act", "run", 1, f.primaryDir)
			return nil
		}
		env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
		env.RegisterActivity(fn)
		_, err := env.ExecuteActivity(fn)
		require.NoError(t, err)
		return result, executor
	}

	result, executor := run(`{"command":"pwd","project":"api"}`)
	require.False(t, result.IsError, result.Content)
	require.Len(t, executor.requests, 1)
	assert.Equal(t, f.apiDir, executor.requests[0].WorktreePath)

	for _, input := range []string{`{"command":"pwd"}`, `{"command":"pwd","project":"Test Project"}`} {
		result, executor = run(input)
		assert.True(t, result.IsError, input)
		assert.Equal(t, blocked, result.Content, input)
		assert.Empty(t, executor.requests, input)
	}
}

// Projects with the same name are named by id; a name two share is accepted
// for neither.
func TestChatProjects_CollidingNamesAreNamedByID(t *testing.T) {
	t.Parallel()
	f := newMultiProjectChat(t)
	twin := f.attach(t, "Test Project", "cloud-box", t.TempDir())

	ctx := context.Background()
	chat, err := store.GetChat(ctx, f.h.Repo().ORM(), f.chat)
	require.NoError(t, err)
	primary, err := f.h.Repo().GetProject(ctx, f.project)
	require.NoError(t, err)
	projects := chatProjects(ctx, f.h.Repo(), f.h.Repo(), chat, primary)
	assert.Equal(t, []string{f.project, "api", twin.ID}, projectValues(projects))

	_, ok := findChatProject(projects, "Test Project")
	assert.False(t, ok, "ambiguous")
	got, ok := findChatProject(projects, twin.ID)
	require.True(t, ok)
	assert.Equal(t, twin.ID, got.project.ID)
}

// A chat with no attachments, or an activity not wired to read them, is its
// primary alone.
func TestChatProjects_WithoutAttachmentsIsThePrimaryAlone(t *testing.T) {
	primary := &db.Project{ID: "p", Name: "app"}
	chat := &db.Chat{ID: "c", ProjectID: "p"}
	got := chatProjects(context.Background(), nil, nil, chat, primary)
	require.Len(t, got, 1)
	assert.True(t, got[0].primary)
	assert.Equal(t, "app", got[0].value)
}
