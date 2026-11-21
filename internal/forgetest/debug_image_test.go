package forgetest

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type debugImageWorkload struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Image   string `json:"image"`
	Runtime struct {
		Type string `json:"type"`
	} `json:"runtime"`
	Build struct {
		Type       string `json:"type"`
		Dockerfile string `json:"dockerfile"`
	} `json:"build"`
}

func TestDevDebugImageIsBuildOnlyAndOptIn(t *testing.T) {
	forge := Pinned(t) // The real KCL renderer is intentionally a slow test.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	eval := func(file, selector string, options ...string) []byte {
		t.Helper()
		args := append([]string{"kcl", "eval", file, "-S", selector}, options...)
		cmd := forge.Command(t.Context(), args...)
		cmd.Dir = root
		out, err := cmd.Output()
		require.NoError(t, err, "render %s %v: %s", file, options, out)
		return out
	}
	type bundle struct {
		Workloads []debugImageWorkload `json:"workloads"`
		Manifests []json.RawMessage    `json:"manifests"`
	}
	renderDev := func(options ...string) (got bundle) {
		t.Helper()
		require.NoError(t, json.Unmarshal(eval("deploy/kcl/dev/main.k", "output", options...), &got))
		return got
	}
	baseline := []string{"reliant-migrate", "reliant-api-server", "reliant-temporal-worker", "daemon-gateway"}
	names := func(workloads []debugImageWorkload) []string {
		out := make([]string, 0, len(workloads))
		for _, workload := range workloads {
			out = append(out, workload.Name)
		}
		return out
	}

	defaultDev := renderDev()
	assert.Equal(t, baseline, names(defaultDev.Workloads))
	assert.Empty(t, defaultDev.Manifests, "normal dev must stay container-free")
	for _, workload := range defaultDev.Workloads {
		assert.Equal(t, "host", workload.Runtime.Type, workload.Name)
		assert.Equal(t, "go", workload.Build.Type, workload.Name)
	}
	assert.Equal(t, defaultDev, renderDev("-D", "debug_image=false"))

	optIn := renderDev("-D", "debug_image=true")
	require.Len(t, optIn.Workloads, len(baseline)+1)
	assert.Equal(t, defaultDev.Workloads, optIn.Workloads[:len(baseline)])
	assert.Equal(t, defaultDev.Manifests, optIn.Manifests, "the debug image must not render runnable objects")
	debug := optIn.Workloads[len(baseline)]
	assert.Equal(t, "reliant-debug-image", debug.Name)
	assert.Equal(t, "tool", debug.Kind)
	assert.Equal(t, "build-only", debug.Runtime.Type)
	assert.Equal(t, "docker", debug.Build.Type)
	assert.Equal(t, "Dockerfile.debug", debug.Build.Dockerfile)
	assert.Equal(t, "reliant-debug-image", debug.Image, "Forge derives a local image name, not a registry target")

	var all []debugImageWorkload
	require.NoError(t, json.Unmarshal(eval("deploy/kcl/workloads.k", "ALL"), &all))
	assert.Equal(t, baseline, names(all), "consumer environments bind ALL without the debug image")
}
