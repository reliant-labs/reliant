// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonLikeExecutor stands in for the machine side of tool execution. Like
// every real exec path on the daemon, it refuses to run in a directory that
// does not exist (osutil.ValidateWorkingDir — the exact error users saw), and
// it answers worktree.ensure through ensure, which a test sets to what the
// daemon would do.
type daemonLikeExecutor struct {
	mu        sync.Mutex
	requests  []*toolexec.ToolRequest
	ensures   []toolexec.WorkspaceEnsureRequest
	selectors []*toolexec.DaemonSelector
	ensure    func(req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error)
}

func (d *daemonLikeExecutor) ExecuteTool(_ context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()
	if err := osutil.ValidateWorkingDir(req.WorktreePath); err != nil {
		return &toolexec.ToolResult{Success: false, IsError: true, Content: err.Error(), RanOnDaemon: true}, nil
	}
	return &toolexec.ToolResult{Success: true, Content: "ran in " + req.WorktreePath, RanOnDaemon: true}, nil
}

func (d *daemonLikeExecutor) Close() error { return nil }

func (d *daemonLikeExecutor) EnsureWorkspace(_ context.Context, _ string, selector *toolexec.DaemonSelector, req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
	d.mu.Lock()
	d.ensures = append(d.ensures, req)
	d.selectors = append(d.selectors, selector)
	ensure := d.ensure
	d.mu.Unlock()
	if ensure == nil {
		return &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspacePresent, Path: req.Path}, nil
	}
	return ensure(req)
}

func (d *daemonLikeExecutor) ensureCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ensures)
}

type workspaceRecoveryFixture struct {
	h           *IdempotencyTestHelper
	chatID      string
	projectPath string
	workspace   string // the chat's worktree directory
	worktreeID  string
}

// setupWorkspaceRecoveryFixture is a chat bound to a worktree whose directory
// does not exist on disk, owned by daemon-owner.
func setupWorkspaceRecoveryFixture(t *testing.T) *workspaceRecoveryFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	ctx := context.Background()
	userID, projectID, chatID := uuid.New().String(), uuid.New().String(), uuid.New().String()
	projectPath := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "worktrees", "proj", "feature-1a2b3c4d")

	h.CreateTestProjectWithPath(ctx, projectID, userID, projectPath)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	wt := h.CreateTestWorktree(ctx, projectID, chatID, workspace)
	require.NoError(t, h.Repo().AdoptWorktreeDaemon(ctx, wt.ID, "daemon-owner"))

	return &workspaceRecoveryFixture{h: h, chatID: chatID, projectPath: projectPath, workspace: workspace, worktreeID: wt.ID}
}

// run executes one batch the way the runtime does for a chat: ProjectPath is
// the chat's checkout as the launcher snapshotted it into the run's inputs.
func (f *workspaceRecoveryFixture) run(t *testing.T, executor *daemonLikeExecutor, calls ...message.ToolCall) *ExecuteToolsOutput {
	t.Helper()
	var output ExecuteToolsOutput
	err := f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), executor).Execute, ExecuteToolsInput{
		ChatID:      f.chatID,
		Thread:      f.chatID,
		ProjectPath: f.workspace,
		ToolCalls:   calls,
	}, &output)
	require.NoError(t, err)
	return &output
}

func bashCall(command string) message.ToolCall {
	return message.ToolCall{ID: "toolu_" + uuid.New().String(), Name: "bash", Input: `{"command":"` + command + `"}`}
}

// The reported failure: a chat's worktree directory is gone and every tool
// call fails before running, call after call. With recovery, the daemon is
// asked to put the workspace back before the batch, the call runs, and the
// model is told — once — that it is in a recreated checkout.
func TestExecuteTools_MissingWorktreeIsRecoveredBeforeTheCallRuns(t *testing.T) {
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()

	executor := &daemonLikeExecutor{}
	executor.ensure = func(req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
		if osutil.ValidateWorkingDir(req.Path) == nil {
			return &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspacePresent, Path: req.Path}, nil
		}
		require.NoError(t, os.MkdirAll(req.Path, 0o755))
		return &toolexec.WorkspaceEnsureResponse{
			Status: toolexec.WorkspaceRepaired,
			Path:   req.Path,
			Notice: &toolexec.WorkspaceNotice{
				Kind: toolexec.NoticeRepaired, WorkspacePath: req.Path, RunningIn: req.Path,
				Branch: req.Branch, Checkouts: []string{""}, At: time.Now(),
			},
		}, nil
	}

	first := f.run(t, executor, bashCall("git status"))
	require.Len(t, first.ToolResults, 1)
	assert.False(t, first.ToolResults[0].GetIsError(), "the call must run once the workspace is back, got: %s", first.ToolResults[0].GetContent())
	firstContent := first.ToolResults[0].GetContent()
	assert.Contains(t, firstContent, "recreated", "the model must be told its workspace was recreated")
	assert.Contains(t, firstContent, "test-branch")
	assert.Contains(t, firstContent, "Uncommitted")
	assert.Contains(t, firstContent, "ran in "+f.workspace, "the tool's own output follows the note")

	second := f.run(t, executor, bashCall("ls"))
	assert.False(t, second.ToolResults[0].GetIsError(), second.ToolResults[0].GetContent())
	assert.NotContains(t, second.ToolResults[0].GetContent(), "recreated", "the note is said once, not on every call")

	require.GreaterOrEqual(t, executor.ensureCount(), 1)
	req := executor.ensures[0]
	assert.Equal(t, f.workspace, req.Path)
	assert.True(t, req.Repair, "the chat's own worktree is recreatable")
	assert.Equal(t, f.worktreeID, req.WorktreeID)
	assert.Equal(t, "test-branch", req.Branch)
	assert.Equal(t, f.projectPath, req.FallbackPath)
	assert.Equal(t, f.chatID, req.Audience)
	assert.Equal(t, []toolexec.WorkspaceEnsureRepo{{RepoPath: f.projectPath, Rel: ""}}, req.Repos,
		"a project with no nested repos is one repo at its root")
	require.NotNil(t, executor.selectors[0])
	assert.Equal(t, "daemon-owner", executor.selectors[0].ID, "ensure goes to the daemon that owns the checkout")
}

// Nothing to recreate from: the batch still runs — in the directory the daemon
// fell back to — and the model is told where it is, what was lost, and how to
// get its branch back.
func TestExecuteTools_UnrecoverableWorktreeRunsInTheFallbackWithANote(t *testing.T) {
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()

	told := false
	executor := &daemonLikeExecutor{}
	executor.ensure = func(req toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
		resp := &toolexec.WorkspaceEnsureResponse{Status: toolexec.WorkspaceFallback, Path: req.FallbackPath, Detail: "branch 'test-branch' no longer exists"}
		if !told {
			told = true
			resp.Notice = &toolexec.WorkspaceNotice{
				Kind: toolexec.NoticeFallback, WorkspacePath: req.Path, RunningIn: req.FallbackPath,
				Branch: req.Branch, Reason: resp.Detail, At: time.Now(),
			}
		}
		return resp, nil
	}

	first := f.run(t, executor, bashCall("pwd"), message.ToolCall{ID: "toolu_" + uuid.New().String(), Name: "view", Input: `{"file_path":"README.md"}`})
	require.Len(t, first.ToolResults, 2)
	assert.False(t, first.ToolResults[0].GetIsError(), first.ToolResults[0].GetContent())
	note := first.ToolResults[0].GetContent()
	assert.Contains(t, note, f.workspace, "names the workspace that is gone")
	assert.Contains(t, note, f.projectPath, "names where the call ran instead")
	assert.Contains(t, note, "main checkout")
	assert.Contains(t, note, "no longer exists", "says why it could not be recreated")
	assert.NotContains(t, first.ToolResults[1].GetContent(), "main checkout", "one note per batch, on its first call")

	for _, req := range executor.requests {
		assert.Equal(t, f.projectPath, req.WorktreePath, "every call of the batch runs in the fallback directory")
	}
	assert.Equal(t, 1, executor.ensureCount(), "one ensure per batch, not one per call")

	second := f.run(t, executor, bashCall("pwd"))
	assert.False(t, second.ToolResults[0].GetIsError())
	assert.NotContains(t, second.ToolResults[0].GetContent(), "main checkout")
}

// A batch that never touches the machine does not ask it anything: ensure
// would cost a round trip, and could reach a machine nothing else needs.
func TestExecuteTools_ServerOnlyBatchDoesNotEnsure(t *testing.T) {
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()

	executor := &daemonLikeExecutor{}
	f.run(t, executor, message.ToolCall{ID: "toolu_" + uuid.New().String(), Name: tools.ToolWebSearch, Input: `{"query":"x"}`})
	assert.Equal(t, 0, executor.ensureCount())
}

// A daemon that cannot answer (asleep, or too old to know the command) must
// not cost the batch anything: it runs exactly as it did before recovery
// existed.
func TestExecuteTools_EnsureFailureLeavesTheBatchAsItWas(t *testing.T) {
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	require.NoError(t, os.MkdirAll(f.workspace, 0o755))

	executor := &daemonLikeExecutor{}
	executor.ensure = func(toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
		return nil, errors.New(`daemon command "worktree.ensure" failed: unknown command`)
	}
	out := f.run(t, executor, bashCall("ls"))
	assert.False(t, out.ToolResults[0].GetIsError(), out.ToolResults[0].GetContent())
	assert.Equal(t, f.workspace, executor.requests[0].WorktreePath)
	assert.False(t, strings.Contains(out.ToolResults[0].GetContent(), "Workspace"), "no note without an answer")
}

// Not even a panic in the transport may cost the batch: the check is a
// pre-flight it can run without.
func TestExecuteTools_PanickingEnsureLeavesTheBatchAsItWas(t *testing.T) {
	f := setupWorkspaceRecoveryFixture(t)
	defer f.h.Cleanup()
	require.NoError(t, os.MkdirAll(f.workspace, 0o755))

	executor := &daemonLikeExecutor{}
	executor.ensure = func(toolexec.WorkspaceEnsureRequest) (*toolexec.WorkspaceEnsureResponse, error) {
		panic("router without generic commands")
	}
	out := f.run(t, executor, bashCall("ls"))
	assert.False(t, out.ToolResults[0].GetIsError(), out.ToolResults[0].GetContent())
	assert.Equal(t, f.workspace, executor.requests[0].WorktreePath)
}
