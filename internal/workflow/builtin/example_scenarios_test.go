// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"os"
	"path/filepath"
	"testing"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/stretchr/testify/require"
)

// exampleScenariosDir is examples/scenarios relative to this package directory.
// `go test` runs with cwd set to the package dir, so this resolves from
// internal/workflow/builtin/ back to the repo root.
const exampleScenariosDir = "../../../examples/scenarios"

// TestExampleScenarios runs every scenario under examples/scenarios/<workflow>/
// against the builtin workflow that <workflow> names, through the same runner
// TestBuiltinWorkflowScenarios uses.
//
// examples/scenarios/ is user-facing documentation: it is the corpus someone
// reads to learn what a scenario looks like. Nothing executed it, so it rotted
// into a fork of internal/workflow/builtin/scenarios/ that was two refactors
// behind — fixtures calling a `bash` tool renamed to `shell`, and eleven agent
// scenarios asserting a `max_turns_notification` node that agent.yaml has not
// had since f2ab9317. Every one of those passed review because nothing ran it.
//
// Each directory must name a builtin workflow. A directory that names nothing
// is a dead fixture for a workflow that cannot be run, so it fails loudly
// rather than being skipped.
func TestExampleScenarios(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(exampleScenariosDir)
	require.NoError(t, err, "examples/scenarios must exist at %s", exampleScenariosDir)

	ran := 0
	for _, entry := range entries {
		path := filepath.Join(exampleScenariosDir, entry.Name())
		info, err := os.Stat(path)
		require.NoError(t, err, "stat %s", path)
		if !info.IsDir() {
			continue
		}

		// Each entry must be a SYMLINK to the builtin scenario dir, never a
		// copy. A copy is what rotted: two files with one name, only one of
		// them executed, drifting silently until the docs described a workflow
		// that no longer existed. Running both copies would keep them honest
		// but doubles the suite to no benefit — one source of truth is the
		// actual fix, and examples/workflows and examples/presets were already
		// collapsed to symlinks in dff9326a for exactly this reason.
		require.Equalf(t, os.ModeSymlink, entry.Type()&os.ModeSymlink,
			"examples/scenarios/%s must be a symlink to "+
				"internal/workflow/builtin/scenarios/%s, not a copy — "+
				"a second copy drifts from the one the suite runs",
			entry.Name(), entry.Name())

		workflowName := entry.Name()
		workflowData, err := os.ReadFile(filepath.Join("..", "builtin", workflowName+".yaml"))
		require.NoErrorf(t, err,
			"examples/scenarios/%s/ names no builtin workflow %s.yaml — "+
				"these scenarios can never run; update or remove the directory",
			workflowName, workflowName)

		wf, err := v2.ParseWorkflowProtoBytesWithLoader(workflowData, builtinLoader)
		require.NoError(t, err, "parse workflow %s", workflowName)
		require.NoError(t, validation.StaticAnalysis(wf, builtinLoader).AsError(),
			"workflow validation failed for %s", workflowName)

		scenarios, err := wfscenario.LoadScenariosFromDir(path)
		require.NoError(t, err, "load scenarios from %s", path)
		require.NotEmptyf(t, scenarios, "examples/scenarios/%s/ contains no scenarios", workflowName)
		ran += len(scenarios)

		t.Run(workflowName, func(t *testing.T) {
			t.Parallel()
			r := runner.NewRunner(wf)
			for _, sc := range scenarios {
				t.Run(sc.Name, func(t *testing.T) {
					res := r.Run(sc)
					if res.Status == wfscenario.StatusPassed {
						return
					}
					t.Logf("Description: %s", sc.Description)
					t.Logf("Outcome: %s", res.Execution.Outcome)
					t.Logf("Nodes reached: %v", res.Execution.NodesReached)
					if res.Execution.Error != nil {
						t.Logf("Error: %s", res.Execution.Error.Message)
					}
					for _, m := range res.Mismatches {
						t.Errorf("Mismatch: %s", m)
					}
					t.Errorf("scenario %q: status %s", sc.Name, res.Status)
				})
			}
		})
	}

	require.NotZero(t, ran, "no example scenarios were found under %s", exampleScenariosDir)
}
