// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
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
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

type spawnWorktreeFixture struct {
	*activityPlacementFixture
	chatID   string
	activity *CreateWorkflowWithThreadActivity
}

func newSpawnWorktreeFixture(t *testing.T) *spawnWorktreeFixture {
	t.Helper()
	f := newActivityPlacementFixture(t)
	return &spawnWorktreeFixture{
		activityPlacementFixture: f,
		chatID:                   f.chat(t, ""),
		activity:                 NewCreateWorkflowWithThreadActivity(threads.NewService(f.repo), f.repo),
	}
}

func (f *spawnWorktreeFixture) spawn(t *testing.T, worktree, threadID string) CreateWorkflowWithThreadOutput {
	t.Helper()
	if threadID == "" {
		threadID = uuid.NewString()
	}
	origin := db.ThreadOriginSpawn
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(f.activity.Execute)
	val, err := env.ExecuteActivity(f.activity.Execute, CreateWorkflowWithThreadInput{
		WorkflowID:   uuid.NewString(),
		WorkflowName: "builtin://agent",
		ChatID:       f.chatID,
		ThreadID:     threadID,
		Origin:       &origin,
		Worktree:     worktree,
	})
	require.NoError(t, err)
	var out CreateWorkflowWithThreadOutput
	require.NoError(t, val.Get(&out))
	if out.Refusal == "" {
		out.ThreadID = threadID
	}
	return out
}

func TestSpawnWorktree_BindsThreadByNameAndByID(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	wtID := f.worktree(t, "feature-x", "daemon-b")

	for _, ref := range []string{"feature-x", wtID} {
		out := f.spawn(t, ref, "")
		require.Empty(t, out.Refusal, ref)
		th, err := f.repo.GetThread(context.Background(), out.ThreadID)
		require.NoError(t, err)
		require.NotNil(t, th.WorktreeID, ref)
		assert.Equal(t, wtID, *th.WorktreeID)
	}
}

func TestSpawnWorktree_OmittedLeavesThreadUnbound(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	f.worktree(t, "feature-x", "daemon-b")
	out := f.spawn(t, "", "")
	require.Empty(t, out.Refusal)
	th, err := f.repo.GetThread(context.Background(), out.ThreadID)
	require.NoError(t, err)
	assert.Nil(t, th.WorktreeID, "no worktree param keeps the inherit-the-chat default")
}

func TestSpawnWorktree_Refusals(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	ctx := context.Background()
	f.worktree(t, "good", "daemon-b")

	creating := f.worktree(t, "creating", "daemon-b")
	failed := f.worktree(t, "failed", "daemon-b")
	archived := f.worktree(t, "archived", "daemon-b")
	for id, st := range map[string]reliantv1.WorktreeStatus{
		creating: reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING,
		failed:   reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED,
	} {
		w, err := f.repo.GetWorktree(ctx, id)
		require.NoError(t, err)
		w.Status = int32(st)
		require.NoError(t, f.repo.UpdateWorktree(ctx, w))
	}
	require.NoError(t, f.repo.ArchiveWorktree(ctx, archived))

	// A worktree of another project.
	otherProject := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, f.repo.CreateProject(ctx, &db.Project{ID: otherProject, Name: "O", Path: "/o", UserID: f.userID, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	foreign := &core.Worktree{ID: uuid.NewString(), Name: "foreign", Path: "/o/wt", Branch: "foreign", ProjectID: otherProject, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, f.repo.CreateWorktree(ctx, foreign))

	cases := map[string]string{
		"nope":     "does not exist",
		"creating": "still being created",
		"failed":   "failed to be created",
		"archived": "is archived",
		foreign.ID: "different project",
		"foreign":  "does not exist",
	}
	for ref, want := range cases {
		out := f.spawn(t, ref, "")
		assert.Contains(t, out.Refusal, want, ref)
		assert.Contains(t, out.Refusal, `"good"`, "refusal must list the usable worktrees: %s", ref)
		list := out.Refusal[strings.Index(out.Refusal, "Active worktrees"):]
		assert.NotContains(t, list, `"creating"`, "only ACTIVE worktrees are listed")
		assert.NotContains(t, list, `"archived"`, "only ACTIVE worktrees are listed")
	}
}

func TestSpawnWorktree_ResumeMustMatchBinding(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	f.worktree(t, "a", "daemon-b")
	f.worktree(t, "b", "daemon-b")

	first := f.spawn(t, "a", "")
	require.Empty(t, first.Refusal)

	// Same workspace: fine, and still bound.
	again := f.spawn(t, "a", first.ThreadID)
	require.Empty(t, again.Refusal)

	// Different workspace: refused, binding untouched.
	moved := f.spawn(t, "b", first.ThreadID)
	assert.Contains(t, moved.Refusal, "already bound")
	th, err := f.repo.GetThread(context.Background(), first.ThreadID)
	require.NoError(t, err)
	assert.Equal(t, "a", mustWorktree(t, f, *th.WorktreeID).Name)

	// An unbound conversation (chat's own workspace) is not silently rebound.
	plain := f.spawn(t, "", "")
	require.Empty(t, plain.Refusal)
	rebound := f.spawn(t, "a", plain.ThreadID)
	assert.Contains(t, rebound.Refusal, "already bound")
	assert.Contains(t, rebound.Refusal, "this chat's own workspace")
}

func mustWorktree(t *testing.T, f *spawnWorktreeFixture, id string) *core.Worktree {
	t.Helper()
	w, err := f.repo.GetWorktree(context.Background(), id)
	require.NoError(t, err)
	return w
}

func TestEffectiveWorktreeID_ThreadBeatsChat(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	ctx := context.Background()
	chatWT := f.worktree(t, "chat-wt", "daemon-a")
	threadWT := f.worktree(t, "thread-wt", "daemon-b")
	chatID := f.chat(t, chatWT)
	chat, err := f.repo.GetChat(ctx, chatID)
	require.NoError(t, err)

	// Unbound thread and unknown thread both fall back to the chat's.
	f.chatID = chatID
	plain := f.spawn(t, "", "")
	got, bound, err := effectiveWorktreeID(ctx, f.repo, chat, plain.ThreadID)
	require.NoError(t, err)
	assert.False(t, bound)
	assert.Equal(t, chatWT, *got)
	got, bound, err = effectiveWorktreeID(ctx, f.repo, chat, "no-such-thread")
	require.NoError(t, err)
	assert.False(t, bound)
	assert.Equal(t, chatWT, *got)

	// A bound thread wins, so tools route to ITS worktree's daemon.
	spawned := f.spawn(t, "thread-wt", "")
	require.Empty(t, spawned.Refusal)
	got, bound, err = effectiveWorktreeID(ctx, f.repo, chat, spawned.ThreadID)
	require.NoError(t, err)
	assert.True(t, bound)
	assert.Equal(t, threadWT, *got)
}

func TestLoadWorktreeInfo_ThreadBoundRoutesToWorktreeOwner(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	ctx := context.Background()
	wtID := f.worktree(t, "feature-x", "daemon-b")
	chat, err := f.repo.GetChat(ctx, f.chatID)
	require.NoError(t, err)
	project, err := f.repo.GetProject(ctx, f.projectID)
	require.NoError(t, err)
	a := &ExecuteToolsActivity{repo: f.repo}

	spawned := f.spawn(t, "feature-x", "")
	info, _, err := a.loadWorktreeInfo(ctx, chat, project, spawned.ThreadID)
	require.NoError(t, err)
	assert.Equal(t, wtID, info.ID)
	assert.Equal(t, "daemon-b", info.DaemonID)
	assert.Contains(t, info.Path, "feature-x")

	// The parent thread (no binding) stays on the project checkout.
	info, _, err = a.loadWorktreeInfo(ctx, chat, project, "main-thread")
	require.NoError(t, err)
	assert.Empty(t, info.ID)
	assert.Equal(t, project.Path, info.Path)

	// A bound thread whose workspace was archived fails loudly instead of
	// running in the project checkout.
	require.NoError(t, f.repo.ArchiveWorktree(ctx, wtID))
	_, _, err = a.loadWorktreeInfo(ctx, chat, project, spawned.ThreadID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archived")
}

// capturingExecutor records the ToolRequest it is handed.
type capturingExecutor struct {
	mockToolExecutor
	got *toolexec.ToolRequest
}

func (c *capturingExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	c.got = req
	return &toolexec.ToolResult{Success: true, Content: "ok"}, nil
}

// The production shape: the launcher always injects project_path (the chat's
// checkout), a spawned child inherits it as the tools' projectPathOverride,
// and the parent may carry an explicit daemon selector. A child spawned with a
// worktree must still run in THAT checkout on THAT owner.
func TestSpawnWorktree_ChildToolsRunInWorktreeDespiteInheritedOverrides(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	wtID := f.worktree(t, "feature-x", "daemon-b")
	spawned := f.spawn(t, "feature-x", "")
	require.Empty(t, spawned.Refusal)
	wt := mustWorktree(t, f, wtID)

	run := func(thread string, override string, sel *types.DaemonSelector) *toolexec.ToolRequest {
		exec := &capturingExecutor{mockToolExecutor: *newMockToolExecutor()}
		act := NewExecuteToolsActivity(f.repo, exec)
		fn := func(ctx context.Context) (message.ToolResult, error) {
			return act.executeSingleTool(ctx, nil, f.chatID, thread, "shell", `{"command":"pwd"}`,
				"tc-"+uuid.NewString(), "act", "run", 1, override, sel), nil
		}
		env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
		env.RegisterActivity(fn)
		_, err := env.ExecuteActivity(fn)
		require.NoError(t, err)
		require.NotNil(t, exec.got)
		return exec.got
	}

	mainCheckout := "/home/u/projects/p"
	parentSel := &types.DaemonSelector{ID: "daemon-parent"}

	child := run(spawned.ThreadID, mainCheckout, parentSel)
	assert.Equal(t, wt.Path, child.WorktreePath, "thread-bound worktree beats the inherited project_path")
	assert.Equal(t, wtID, child.WorktreeID)
	require.NotNil(t, child.DaemonSelector)
	assert.Equal(t, "daemon-b", child.DaemonSelector.ID, "worktree owner beats the inherited selector")

	// Unchanged for the parent thread: the override and selector still win.
	parent := run("parent-thread", mainCheckout, parentSel)
	assert.Equal(t, mainCheckout, parent.WorktreePath)
	require.NotNil(t, parent.DaemonSelector)
	assert.Equal(t, "daemon-parent", parent.DaemonSelector.ID)
}

// The CreateWorkflowWithThread activity hands the worktree's checkout path
// back so the spawn can make it the child's working directory; a resumed
// thread that was bound reports it too, and an unbound one reports none.
func TestSpawnWorktree_ActivityReturnsWorktreePath(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	wtID := f.worktree(t, "feature-x", "daemon-b")
	wt := mustWorktree(t, f, wtID)

	first := f.spawn(t, "feature-x", "")
	assert.Equal(t, wt.Path, first.WorktreePath)
	resumed := f.spawn(t, "feature-x", first.ThreadID)
	assert.Equal(t, wt.Path, resumed.WorktreePath)
	assert.Empty(t, f.spawn(t, "", "").WorktreePath)
}

func TestThreadBoundExplicit(t *testing.T) {
	sel := &types.DaemonSelector{ID: "x"}
	assert.Nil(t, threadBoundExplicit(true, "owner", sel), "system-prompt/MCP routing follows the thread's worktree owner")
	assert.Equal(t, sel, threadBoundExplicit(false, "owner", sel))
	assert.Equal(t, sel, threadBoundExplicit(true, "", sel))
}

// The prompt's "Your working directory" line takes the worktree path
// streamLLMResponse resolves for the thread, never the inherited project path.
func TestSystemPromptWorkingDirectory_IsTheThreadWorktree(t *testing.T) {
	activity := &CallLLMActivity{}
	prompts := activity.getSystemPrompts(nil, "/main/checkout", "/wt/feature-x", nil, nil, nil, true)
	joined := strings.Join(prompts, "\n")
	assert.Contains(t, joined, "/wt/feature-x")
	assert.NotContains(t, joined, "/main/checkout")
}

// The "create a worktree with the worktree tool" line is emitted only when the
// step can reach that tool, so it never points at a tool load_tool refuses.
func TestSystemPrompt_WorktreeLineFollowsReachability(t *testing.T) {
	activity := &CallLLMActivity{}
	const line = "create a worktree with the worktree tool"
	with := strings.Join(activity.getSystemPrompts(nil, "/p", "", nil, nil, nil, true), "\n")
	without := strings.Join(activity.getSystemPrompts(nil, "/p", "", nil, nil, nil, false), "\n")
	assert.Contains(t, with, line)
	assert.NotContains(t, without, line)
}

// Archiving releases a name, so an archived row and a live one can share it.
// A spawn by that name binds the live row, and with only archived rows left
// it is refused as archived.
func TestSpawnWorktree_ResolvesTheLiveRowOfAReusedName(t *testing.T) {
	f := newSpawnWorktreeFixture(t)
	ctx := context.Background()

	oldID := f.worktree(t, "feat", "daemon-b")
	require.NoError(t, f.repo.ArchiveWorktree(ctx, oldID))
	liveID := f.worktree(t, "feat", "daemon-b")

	out := f.spawn(t, "feat", "")
	require.Empty(t, out.Refusal)
	th, err := f.repo.GetThread(ctx, out.ThreadID)
	require.NoError(t, err)
	require.NotNil(t, th.WorktreeID)
	assert.Equal(t, liveID, *th.WorktreeID)

	require.NoError(t, f.repo.ArchiveWorktree(ctx, liveID))
	out = f.spawn(t, "feat", "")
	assert.Contains(t, out.Refusal, "is archived")
}
