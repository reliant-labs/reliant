package forgetest

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkloadGoBuildsTrimPaths protects the declarations, not forge's flag
// propagation (which forge tests). In particular, migrate/go-run and the CLI's
// cross-platform variants must not depend on the Taskfile's GOFLAGS.
func TestWorkloadGoBuildsTrimPaths(t *testing.T) {
	forge := Pinned(t)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	type workload struct {
		Name  string `json:"name"`
		Build struct {
			Type  string   `json:"type"`
			Flags []string `json:"flags"`
		} `json:"build"`
		Runtime struct {
			Variants []struct {
				Name string `json:"name"`
			} `json:"build_variants"`
		} `json:"runtime"`
	}
	eval := func(selector string, target any) {
		t.Helper()
		cmd := forge.Command(t.Context(), "kcl", "eval", "deploy/kcl/workloads.k", "-S", selector)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "evaluate %s: %s", selector, out)
		require.NoError(t, json.Unmarshal(out, target), "%s", out)
	}
	check := func(w workload) {
		t.Helper()
		assert.Equal(t, "go", w.Build.Type, w.Name)
		assert.Contains(t, w.Build.Flags, "-trimpath", w.Name)
	}

	var shared []workload
	eval("ALL", &shared)
	require.Len(t, shared, 4, "every shared server role and the migrate job")
	for _, w := range shared {
		check(w)
	}

	var cli workload
	eval("reliant_cli", &cli)
	check(cli)
	var platforms []string
	for _, variant := range cli.Runtime.Variants {
		platforms = append(platforms, variant.Name)
	}
	assert.ElementsMatch(t, []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64"}, platforms,
		"all CLI variants inherit the workload's GoBuild flags")
}
