package toolexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// TestServerPlacedViewResolvesANestedRepo is reliant#2755: view is placed
// on the server (it reaches the files through the run's daemon client), so its
// context map never crosses JSON. The repos in it arrived as
// []map[string]interface{}, which the decoder did not read, and
// view(repo=<nested>) failed "not found in project (available: root)" while
// shell and code_context, which go to the daemon, resolved the same repo.
func TestServerPlacedViewResolvesANestedRepo(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "control-plane", "internal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "control-plane", "internal", "wire.go"), []byte("package app\n"), 0o644))

	executor := NewRemoteExecutor(&recordingDaemonRouter{})
	executor.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	executor.SetDaemonClientFactory(func(string, *DaemonSelector) daemon.Client { return daemon.NewLocalClient() })

	req := placementTestRequest(tools.ViewToolName)
	req.ToolInput = `{"file_path":"internal/wire.go","repo":"control-plane"}`
	req.ProjectPath, req.WorktreePath = root, root
	req.Repos = []*core.Repo{{ID: "r1", Name: "control-plane", RelativePath: "control-plane"}}

	res, err := executor.ExecuteTool(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "package app")
}

// TestReposFromContextReadsBothShapes: the map a daemon decodes from JSON
// and the one the server passes in process carry the same repos.
func TestReposFromContextReadsBothShapes(t *testing.T) {
	req := &ToolRequest{Repos: []*core.Repo{{ID: "r1", Name: "control-plane", RelativePath: "control-plane"}, nil}}
	inProcess := req.toolContextMap()

	raw, err := json.Marshal(inProcess)
	require.NoError(t, err)
	var overJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &overJSON))

	want := []*core.Repo{{ID: "r1", Name: "control-plane", RelativePath: "control-plane"}}
	for name, m := range map[string]map[string]interface{}{"in process": inProcess, "over JSON": overJSON} {
		got, ok := reposFromContext(m)
		assert.True(t, ok, name)
		assert.Equal(t, want, got, name)
	}

	_, ok := reposFromContext(map[string]interface{}{})
	assert.False(t, ok, "a single-repo project's map carries no repos")
}
