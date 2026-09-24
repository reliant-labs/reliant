// Copyright (c) 2025 Reliant Labs
package tools

import (
	"io/fs"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The scenario tools used to parse stored YAML into a private mirror of
// wfscenario.Scenario that existed only to carry jsonschema tags, then copy it
// field-by-field onto the real type. Every field the mirror forgot was silently
// dropped. The end-to-end tool-path tests (write_scenario running on the
// runner) live in internal/workflow/scenario/runner/tool_path_test.go, where
// the runner is importable; this file keeps the parser guard.

// TestToolParser_MatchesCISemanticsOnRealScenarios is the structural guard.
//
// CI unmarshals builtin scenario files straight into wfscenario.Scenario
// (internal/workflow/builtin/scenarios_test.go). The tools must read those same
// bytes into the same value — otherwise an agent can view a scenario, rewrite
// it through write_scenario, and silently delete assertions that CI still
// enforces. Comparing against the whole embedded corpus (which already uses
// completed:, skipped: and outputs:) makes any future divergence fail here
// rather than in production.
func TestToolParser_MatchesCISemanticsOnRealScenarios(t *testing.T) {
	files, err := fs.Glob(builtin.BuiltinScenarioDirsFS, "scenarios/*/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected embedded builtin scenarios")

	sawSkipped, sawOutputs, sawTyped := false, false, false

	for _, path := range files {
		data, err := fs.ReadFile(builtin.BuiltinScenarioDirsFS, path)
		require.NoError(t, err)

		// What CI sees.
		var expected wfscenario.Scenario
		if err := yaml.Unmarshal(data, &expected); err != nil || expected.Name == "" {
			continue // multi-document file; the per-document cases below cover the fields
		}

		// What the tools see, through the real stored-scenario parser.
		actual, err := dbScenarioToScenarioInternal(&db.WorkflowScenario{Events: string(data)})
		require.NoErrorf(t, err, "tool parser rejected %s", path)

		require.Equalf(t, &expected, actual,
			"tool parser and CI disagree about %s — the tool path is losing or altering fields", path)

		if e := expected.Expect; e != nil {
			sawSkipped = sawSkipped || len(e.Skipped) > 0
			sawOutputs = sawOutputs || len(e.Outputs) > 0
		}
		for _, ev := range expected.Events {
			if ev.Type != "" {
				sawTyped = true
			}
		}
	}

	// A round-trip test over a corpus that exercises none of the dropped fields
	// would pass even with the bug reinstated. Pin that the corpus is load-bearing.
	require.True(t, sawSkipped, "corpus no longer exercises expect.skipped")
	require.True(t, sawOutputs, "corpus no longer exercises expect.outputs")
	require.True(t, sawTyped, "corpus no longer exercises typed events")
}
