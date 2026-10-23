// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/configadapter"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// memoryConfigProvider hands every project the same config — the memory under test.
type memoryConfigProvider struct{ cfg *cfgpkg.Config }

func (p memoryConfigProvider) GetProjectConfig(context.Context, cfgpkg.ProjectRef) (*cfgpkg.Config, error) {
	return p.cfg, nil
}

// The rule as this repository's own user memory states it.
const worktreeRootMemory = "- **Worktrees go in `~/src/rl-agent-worktrees/<name>`**, never `/tmp`. When\n" +
	"  your work is pushed and the worktree is clean, `git worktree remove` it.\n"

// shellCallContext is a shell call in the `reliant` repo of a multi-repo
// project at /Users/u/src/reliant-labs.
func shellCallContext(command string) *toolExecutionContext {
	input, _ := json.Marshal(map[string]string{"command": command, "repo": "reliant"})
	return &toolExecutionContext{
		chat:          &db.Chat{ID: "chat-1"},
		project:       &db.Project{ID: "project-1", Path: "/Users/u/src/reliant-labs"},
		worktree:      &rctx.WorktreeInfo{Path: "/Users/u/src/reliant-labs"},
		repos:         []*core.Repo{{Name: "reliant", RelativePath: "reliant"}, {Name: "forge", RelativePath: "forge"}},
		chatID:        "chat-1",
		thread:        "chat-1",
		toolName:      tools.ShellToolName,
		toolInput:     string(input),
		dispatchInput: string(input),
	}
}

func TestWorktreeAddSuggestion_SuggestsTheWorktreeTool(t *testing.T) {
	t.Parallel()

	a := (&ExecuteToolsActivity{}).WithConfigProvider(memoryConfigProvider{cfg: &cfgpkg.Config{}})
	caps := tools.LegacyCapabilities(false)

	got := a.worktreeAddSuggestion(context.Background(), caps, shellCallContext("git worktree add ../reliant-x -b x"))
	assert.Contains(t, got, "worktree tool")
	assert.Contains(t, got, "memory", "the suggestion defers to the user's own rule")

	for _, quiet := range []string{"git worktree list", "git status", "git worktree add /tmp/old HEAD~3"} {
		assert.Empty(t, a.worktreeAddSuggestion(context.Background(), caps, shellCallContext(quiet)), quiet)
	}

	view := shellCallContext("git worktree add ../reliant-x -b x")
	view.toolName = tools.ToolView
	assert.Empty(t, a.worktreeAddSuggestion(context.Background(), caps, view), "only shell calls")
}

// A worktree root named in the user's, the project's or a repo's memory wins:
// a checkout made there — relative paths resolved from the repo the command
// ran in — gets no suggestion. A checkout anywhere else still does.
func TestWorktreeAddSuggestion_MemoryRootIsRespected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	caps := tools.LegacyCapabilities(false)
	for name, cfg := range map[string]*cfgpkg.Config{
		"user memory":    {GlobalMemoryMD: worktreeRootMemory},
		"project memory": {ProjectMemoryMD: worktreeRootMemory},
		"repo memory":    {RepoMemories: map[string]string{"reliant": worktreeRootMemory}},
	} {
		t.Run(name, func(t *testing.T) {
			a := (&ExecuteToolsActivity{}).WithConfigProvider(memoryConfigProvider{cfg: cfg})
			for _, cmd := range []string{
				"git worktree add ~/src/rl-agent-worktrees/reliant-655 -b fix origin/main",
				"git worktree add /Users/u/src/rl-agent-worktrees/reliant-655 -b fix",
				"git worktree add ../../rl-agent-worktrees/reliant-655 -b fix",
			} {
				assert.Empty(t, a.worktreeAddSuggestion(ctx, caps, shellCallContext(cmd)), cmd)
			}
			assert.NotEmpty(t, a.worktreeAddSuggestion(ctx, caps, shellCallContext("git worktree add ../reliant-x -b x")),
				"a checkout outside the memory's root still gets the suggestion")
		})
	}
}

// The suggestion names the worktree tool, so it is made only when load_tool
// would grant that tool this step.
func TestWorktreeAddSuggestion_OnlyWhenTheWorktreeToolIsReachable(t *testing.T) {
	t.Parallel()

	a := (&ExecuteToolsActivity{}).WithConfigProvider(memoryConfigProvider{cfg: &cfgpkg.Config{}})
	call := shellCallContext("git worktree add ../reliant-x -b x")

	shellOnly := tools.ResolveCapabilities(tools.CapabilityInputs{
		Access: tools.ResolveToolAccess(nil, []string{tools.ShellToolName}, nil), Permission: tools.PermissionMutating,
	})
	require.False(t, shellOnly.CanLoad(tools.ToolWorktree))
	assert.Empty(t, a.worktreeAddSuggestion(context.Background(), shellOnly, call))

	all := tools.ResolveCapabilities(tools.CapabilityInputs{
		Access: tools.ResolveToolAccess(nil, []string{"*"}, nil), Permission: tools.PermissionMutating,
	})
	assert.NotEmpty(t, a.worktreeAddSuggestion(context.Background(), all, call))
}

// shellOutputExecutor stands in for the machine: every shell call runs and
// answers the way the shell tool does, with a ShellOutput envelope.
type shellOutputExecutor struct {
	mu   sync.Mutex
	ran  []string
	echo string
}

func (e *shellOutputExecutor) ExecuteTool(_ context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	var params tools.ShellParams
	_ = json.Unmarshal([]byte(req.ToolInput), &params)
	e.mu.Lock()
	e.ran = append(e.ran, params.Command)
	e.mu.Unlock()
	out, _ := json.Marshal(tools.ShellOutput{Stdout: e.echo})
	return &toolexec.ToolResult{Success: true, Content: string(out), RanOnDaemon: true}, nil
}

func (e *shellOutputExecutor) Close() error { return nil }

// End to end through ExecuteTools: `git worktree add` runs, its result says so,
// and carries the suggestion inside its shell envelope — unless the project's
// memory names the root the checkout was made in.
func TestExecuteTools_GitWorktreeAddRunsWithASuggestion(t *testing.T) {
	t.Parallel()

	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()

	userID := "user-" + uuid.NewString()
	project := h.CreateTestProjectWithPath(ctx, "project-"+uuid.NewString(), userID, "/Users/u/src/reliant-labs/reliant")
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, userID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	repo := h.Repo()
	provider := cfgpkg.NewCachedStoredConfigProvider(configadapter.NewRepoConfigStore(repo), repo, cfgpkg.DefaultParsedConfigCacheBytes)
	executor := &shellOutputExecutor{echo: "Preparing worktree (new branch 'x')\n"}
	activity := NewExecuteToolsActivity(h.Repo(), executor).WithConfigProvider(provider)

	run := func(command string) *message.ToolResult {
		t.Helper()
		input, err := json.Marshal(map[string]string{"command": command})
		require.NoError(t, err)
		callID := "toolu_" + uuid.NewString()
		var output ExecuteToolsOutput
		require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
			ChatID: chat.ID, Thread: chat.ID,
			ToolCalls: []message.ToolCall{{ID: callID, Name: tools.ShellToolName, Input: string(input)}},
		}, &output))
		results := resultsByID(output.GetToolResults())
		require.Contains(t, results, callID)
		r := results[callID]
		return &message.ToolResult{Content: r.GetContent(), IsError: r.GetIsError()}
	}
	decode := func(r *message.ToolResult) tools.ShellOutput {
		t.Helper()
		var out tools.ShellOutput
		require.NoError(t, json.Unmarshal([]byte(r.Content), &out), "the result stays one shell envelope: %s", r.Content)
		return out
	}

	got := run("git worktree add ../reliant-x -b x")
	require.False(t, got.IsError, "nothing is refused: %s", got.Content)
	out := decode(got)
	assert.Equal(t, "Preparing worktree (new branch 'x')\n", out.Stdout, "the command's own output is intact")
	assert.Contains(t, out.Suggestion, "worktree tool")

	memory := worktreeRootMemory
	require.NoError(t, h.Repo().UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: project.ID, DaemonID: "daemon-1", GlobalMemoryMD: &memory,
	}))
	got = run("git worktree add ~/src/rl-agent-worktrees/reliant-x -b x origin/main")
	require.False(t, got.IsError, got.Content)
	assert.Empty(t, decode(got).Suggestion, "the user's memory names this root; it wins")

	got = run("git worktree add ../reliant-y -b y")
	assert.NotEmpty(t, decode(got).Suggestion, "outside the memory's root the suggestion still applies")

	assert.Equal(t, []string{
		"git worktree add ../reliant-x -b x",
		"git worktree add ~/src/rl-agent-worktrees/reliant-x -b x origin/main",
		"git worktree add ../reliant-y -b y",
	}, executor.ran, "every command ran")
}
