package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/depsurface"
)

// The shared contract.Resolver is built from contractPathsUnder, so a file it
// leaves out gets no type-checked foreign embeds. It listed only contract.go,
// and a deps.go role embedding another package's interface
// (`type Store interface { lifecycle.WorkflowReader; ... }`) generated a mock
// without the embedded methods, which failed generate's build validation.
func TestContractPathsUnder_IncludesMarkedDepsFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	internal := filepath.Join(root, "internal")
	files := map[string]string{
		// A contract package.
		"internal/contracted/contract.go": "package contracted\n",
		// A component whose Deps roles forge mocks from deps.go.
		"internal/narrowed/deps.go": depsurface.DepsFileMarker + "\npackage narrowed\n",
		// Both: contract.go is what the walk generates from.
		"internal/both/contract.go": "package both\n",
		"internal/both/deps.go":     depsurface.DepsFileMarker + "\npackage both\n",
		// A hand-written deps.go without the marker generates nothing.
		"internal/unmarked/deps.go": "package unmarked\n",
		// Excluded and testdata trees are pruned for both kinds.
		"internal/excluded/deps.go":          depsurface.DepsFileMarker + "\npackage excluded\n",
		"internal/narrowed/testdata/deps.go": depsurface.DepsFileMarker + "\npackage testdata\n",
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.ProjectConfig{Contracts: config.ContractsConfig{Exclude: []string{"internal/excluded"}}}

	var got []string
	for _, p := range contractPathsUnder(internal, root, cfg) {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.ToSlash(rel))
	}
	slices.Sort(got)
	want := []string{
		"internal/both/contract.go",
		"internal/contracted/contract.go",
		"internal/narrowed/deps.go",
	}
	if !slices.Equal(got, want) {
		t.Errorf("contractPathsUnder = %v, want %v", got, want)
	}
}
