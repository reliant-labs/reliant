package toolexec

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// TestServerTools_SeeTheWorktreesMachine: a server-placed tool runs on the
// worker, in a ToolContext rebuilt from the request's context map. The daemon
// that owns the worktree's checkout has to survive that trip. report_bug names
// the machine a defect hit from it, and every prod report filed while the map
// dropped it said daemon_id="", so none could be tied to a pod.
//
// Not parallel: it swaps the process's default slog handler to read the
// report, which with Sentry off (as in tests) is filed to the log.
func TestServerTools_SeeTheWorktreesMachine(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	executor := NewRemoteExecutor(&machineRouter{})
	executor.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))

	result, err := executor.ExecuteTool(context.Background(), &ToolRequest{
		ToolName: tools.ToolReportBug,
		ToolInput: `{"product":"reliant","severity":"critical","title":"Workspace volume detached",` +
			`"summary":"The workspace disk went away mid-run.","expected":"The disk stays mounted.",` +
			`"actual":"ls: /home/workspace: No such file or directory"}`,
		ToolCallID:       "call-1",
		UserID:           "user-1",
		ChatID:           "chat-worktree-daemon",
		ProjectID:        "project-1",
		ProjectPath:      "/home/workspace/projects/reliant",
		WorktreeID:       "wt-1",
		WorktreePath:     "/home/workspace/.reliant/worktrees/reliant/errors",
		WorktreeDaemonID: "daemon-b",
		DaemonSelector:   &DaemonSelector{ID: "daemon-b"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)

	line := logs.String()
	assert.Contains(t, line, "LLM bug report")
	assert.Contains(t, line, "daemon_id=daemon-b", "the worktree's machine reached the tool")
	assert.Contains(t, line, "worktree_id=wt-1")
}
