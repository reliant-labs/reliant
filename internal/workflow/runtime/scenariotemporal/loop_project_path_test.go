// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// A `workflow` node's `project.path` must take effect wherever that node sits.
// docs/workflows/patterns.mdx describes project.path as the working directory
// for a sub-workflow, with no carve-out for loop bodies, and the field is
// declared on the node — not on the enclosing loop — so a reader has no reason
// to expect it honored at the top level and dropped one level in.
//
// It was dropped. workflow.go honors model.NodeProjectPath at all three of its
// sub-workflow construction sites (:1181, :1519, :1722), overriding the
// inherited path with the node's own before building the executor. Neither loop
// executor did: both passed the LOOP's inherited e.projectPath straight through
// and never consulted the node. A `workflow` node inside a loop body therefore
// ran against the parent's directory, and when the parent had none — the normal
// case for a loop that creates its own worktrees — it failed outright in preset
// loading with "project path not set, cannot load presets".
//
// builtin/parallel-compete.yaml is the workflow that breaks. Its `impl` node
// sets `project.path: "{{nodes.create_wt.path}}"` so each candidate runs in the
// worktree its own iteration just created; with the override ignored, all three
// candidates died before any agent ran. The loop declares `on_failure: continue`,
// so it still reported completion — which is why this never surfaced as a failed
// run, and why the assertion below reads what the body REACHED rather than the
// run's outcome.

// TestParallelCompete_ImplNodeHonorsItsOwnProjectPath runs the REAL
// builtin/parallel-compete.yaml — the workflow the defect was found in — rather
// than a reduction of it, so the test cannot drift away from the shape that
// broke.
//
// Measured on this scenario: before the fix all 3 iterations abort with
// "project path not set, cannot load presets" and nothing beneath `impl` is ever
// reached; after it, the sub-workflow starts and reports its own nodes.
func TestParallelCompete_ImplNodeHonorsItsOwnProjectPath(t *testing.T) {
	wf := loadBuiltin(t, "parallel-compete")
	scenarios := loadScenarios(t, "../../builtin/testdata/parallel-compete_scenarios.yaml")

	var sc *simulator.Scenario
	for _, s := range scenarios {
		if s.Name == "parallel_compete_use_winner" {
			sc = s
			break
		}
	}
	require.NotNil(t, sc, "scenario parallel_compete_use_winner must exist")

	res := NewRunner(wf).Run(sc)
	t.Logf("completed=%v", res.Execution.NodesCompleted)

	// Each candidate's `impl` runs builtin://agent, which this backend keeps
	// opaque, so the sub-workflow is observed through the one node its black-box
	// stand-in runs. That node completing is proof the iteration got PAST preset
	// loading — the step that consumes the project path and the step that used
	// to abort.
	//
	// Counting matters: the loop declares on_failure: continue, so a run in
	// which every candidate died still reports completion. Only the per-iteration
	// evidence distinguishes the two, and there must be one per candidate.
	implRuns := 0
	for _, completedNode := range res.Execution.NodesCompleted {
		if strings.HasPrefix(completedNode, "implementations.impl.") {
			implRuns++
		}
	}
	require.Equal(t, 3, implRuns,
		"all 3 candidates must enter their sub-workflow. parallel-compete's `impl` "+
			"node sets project.path to the worktree its own iteration created; "+
			"ignoring that override aborts every candidate in preset loading, and "+
			"on_failure: continue then reports the run as completed anyway "+
			"(completed=%v)", res.Execution.NodesCompleted)
}
