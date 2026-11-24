package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

func runInstallSkillBundle(t *testing.T, req installSkillBundleRequest) installBundleResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	out, err := handleInstallSkillBundle(context.Background(), payload)
	require.NoError(t, err)
	var resp installBundleResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	return resp
}

// The global skills directory is this machine's: the server names the scope
// and the daemon resolves it under its own user config dir.
func TestInstallSkillBundle_ResolvesScopeOnThisMachine(t *testing.T) {
	userDir := t.TempDir()
	config.UseUserConfigDirForTest(t, userDir)
	project := t.TempDir()
	files := []installBundleFile{{Path: "gh/SKILL.md", Content: "# gh"}}

	for scope, wantDir := range map[string]string{
		"global":  filepath.Join(userDir, "skills"),
		"project": filepath.Join(project, config.ReliantDir, "skills"),
		"local":   filepath.Join(project, config.ReliantLocalDir, "skills"),
	} {
		resp := runInstallSkillBundle(t, installSkillBundleRequest{Scope: scope, ProjectPath: project, Files: files})
		require.Empty(t, resp.Error, scope)
		assert.Equal(t, 1, resp.Installed, scope)
		got, err := os.ReadFile(filepath.Join(wantDir, "gh", "SKILL.md"))
		require.NoError(t, err, scope)
		assert.Equal(t, "# gh", string(got), scope)
	}
}

func TestInstallSkillBundle_RefusesUnresolvableScope(t *testing.T) {
	t.Parallel()
	files := []installBundleFile{{Path: "gh/SKILL.md", Content: "# gh"}}
	assert.Contains(t, runInstallSkillBundle(t, installSkillBundleRequest{Scope: "project", Files: files}).Error, "project_path is required")
	assert.Contains(t, runInstallSkillBundle(t, installSkillBundleRequest{Scope: "elsewhere", ProjectPath: t.TempDir(), Files: files}).Error, "unsupported scope")
}

// The deprecated command used to resolve an empty target_dir to the daemon's
// working directory and write there.
func TestInstallBundle_RefusesEmptyTargetDir(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(installBundleRequest{Files: []installBundleFile{{Path: "gh/SKILL.md", Content: "# gh"}}})
	require.NoError(t, err)
	out, err := handleInstallBundle(context.Background(), payload)
	require.NoError(t, err)
	var resp installBundleResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.Equal(t, "target_dir is required", resp.Error)
	assert.Zero(t, resp.Installed)
}
