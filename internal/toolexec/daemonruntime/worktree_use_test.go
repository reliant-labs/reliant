package daemonruntime

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The daemon notes the workspaces its work names (worktree_use.go), and the
// shared Reclaimer reports the last one (Result.LastUse).

// useHome points $HOME at a fresh directory and returns its worktree root.
// Not parallel: $HOME is process-wide.
func useHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := worktreereclaim.DefaultRoot()
	require.NoError(t, err)
	return root
}

func TestToolCallNotesTheWorkspacesItNames(t *testing.T) {
	root := useHome(t)
	used := filepath.Join(root, "proj", "fix-a1b2")
	d := newTestDaemonClient("d-use", "u-use")
	s := useTestSession(d, 16)

	// Its chat is bound elsewhere (no project in its context); only its
	// input names the workspace.
	d.executeTool(s, &reliantv1.ToolRequest{RequestId: "use-1", ToolName: "no_such_tool", ContextJson: `{}`,
		ToolInput: `{"command":"cd ` + used + `/reliant && git status"}`})
	require.Equal(t, "use-1", sentToolResponse(t, s).RequestId)

	require.False(t, sharedWorktreeUse().LastUse(used).IsZero(), "a tool call that names a path in a workspace uses it")
	require.True(t, sharedWorktreeUse().LastUse(filepath.Join(root, "proj", "other")).IsZero())
	r, err := sharedWorktreeReclaimer()
	require.NoError(t, err)
	require.Same(t, sharedWorktreeUse(), r.Use, "the reclaimer reports what the tool calls noted")
}

func TestCommandsThatWorkOnPathsNoteThem(t *testing.T) {
	root := useHome(t)
	for name, wantUse := range map[string]bool{
		"fs.read_file": true, "fs.write_file": true, "exec.run": true, "terminal.create": true,
		// Polled by the UI, or the reclaim's own lifecycle: never use.
		"worktree.git_status": false, "project.git_changes": false, "worktree.reconcile": false, "exec.bg_list": false,
	} {
		ws := filepath.Join(root, "proj", "cmd-"+filepath.Base(name))
		release, err := beginRecoveryCommand(name, []byte(`{"path":"`+ws+`/a.txt","working_dir":"`+ws+`"}`))
		require.NoError(t, err, name)
		release()
		require.Equal(t, wantUse, !sharedWorktreeUse().LastUse(ws).IsZero(), name)
	}
}
