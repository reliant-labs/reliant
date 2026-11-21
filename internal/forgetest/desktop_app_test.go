package forgetest

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The release env declares the desktop app as a build-only ShellBuild that
// runs release.yml's own packaging step for this machine's platform, unsigned
// and unpublished, and deploys nothing.
func TestReleaseDesktopAppIsBuildOnlyPackStep(t *testing.T) {
	forge := Pinned(t) // Use the same KCL evaluator as env build, in the full lane.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	cmd := forge.Command(t.Context(), "kcl", "eval", "deploy/kcl/release/main.k", "-S", "output")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "render release: %s", out)

	type workload struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Runtime struct {
			Type string `json:"type"`
		} `json:"runtime"`
		Build struct {
			Type string `json:"type"`
			Cmd  string `json:"cmd"`
			Cwd  string `json:"cwd"`
		} `json:"build"`
	}
	var got struct {
		Workloads []workload        `json:"workloads"`
		Manifests []json.RawMessage `json:"manifests"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Empty(t, got.Manifests, "the release environment must not deploy objects")

	i := slices.IndexFunc(got.Workloads, func(w workload) bool { return w.Name == "reliant-desktop" })
	require.GreaterOrEqual(t, i, 0, "the release environment must declare reliant-desktop")
	app := got.Workloads[i]
	assert.Equal(t, "tool", app.Kind)
	assert.Equal(t, "build-only", app.Runtime.Type)
	assert.Equal(t, "shell", app.Build.Type)
	assert.Equal(t, "electron", app.Build.Cwd)
	// host: this machine's platform; never: electron-builder publishes nothing.
	assert.Equal(t, "node scripts/pack-desktop.js host electron-builder.js never", app.Build.Cmd)
}
