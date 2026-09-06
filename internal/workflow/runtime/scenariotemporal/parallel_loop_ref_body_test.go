// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A `type: workflow` REF inside a PARALLEL loop body must execute, and the
// structural nodes above it must be observable.
//
// The symptom that motivated this looked exactly like a production bug: running
// builtin/parallel-compete.yaml's parallel_compete_use_winner, the Temporal
// backend reached only [improve_prompt, save_improved_prompt,
// implementations.create_wt, review] while the fast simulator ran three full
// candidate iterations. Read literally, that says the real runtime enters the
// parallel loop, runs the first inner node once, and never runs `impl` — which
// would mean parallel-compete creates three worktrees and implements nothing.
//
// The runtime was innocent, and the evidence that settles it is per-iteration
// rather than aggregate: the loop logged "completed=3 failed=0" and the
// sub-workflow's stand-in node completed three times, once per candidate. That
// distinction matters here more than anywhere else, because the loop declares
// `on_failure: continue` — the policy that let the earlier project.path defect
// report a fully successful run while every iteration died in setup. "Never
// dispatched" and "dispatched and failed silently" are indistinguishable from
// the reached-node list alone.
//
// Three harness defects stacked to produce the illusion, all of them in this
// package, none of them ever shipped:
//
//  1. Every `ref:` was replaced by a black box, so `impl`'s body never ran even
//     when the scenario mocked its internals. The gate now mirrors the
//     simulator's workflowNodeIsTransparent (see transparentRefs).
//  2. No `project_path` input, so preset loading failed terminally on `review`
//     and aborted the run. Production always supplies it.
//  3. Structural nodes were unobservable. A loop or `workflow` node dispatches
//     no activity, and the parallel loop executor — unlike the sequential one —
//     fires no iteration checkpoint, so `implementations` announced itself
//     nowhere.
func TestParallelCompete_RefBodyInsideParallelLoopExecutes(t *testing.T) {
	wf := loadBuiltin(t, "parallel-compete")
	all := loadScenarios(t, "../../builtin/testdata/parallel-compete_scenarios.yaml")
	sc := findScenario(t, all, "parallel_compete_use_winner")

	res := NewRunner(wf).Run(sc)

	require.Equal(t, "completed", res.Execution.Outcome,
		"run must complete; error=%v mismatches=%v", res.Execution.Error, res.Mismatches)

	// The ref body really executed. Under black-box mocking this node is never
	// entered no matter what the scenario writes.
	require.Contains(t, res.Execution.NodesReached, "implementations.impl.agent_loop.call_llm")

	// The structural nodes between the graph root and that leaf are reported.
	// `implementations` is the parallel loop itself, and it is the one no
	// checkpoint ever announces.
	require.Contains(t, res.Execution.NodesReached, "implementations")
	require.Contains(t, res.Execution.NodesReached, "implementations.impl")

	// One execution per candidate. Counting is what separates "all three ran"
	// from "one ran and on_failure: continue hid the other two" — the shape the
	// project.path defect hid behind.
	implRuns := 0
	for _, id := range res.Execution.NodesCompleted {
		if id == "implementations.impl.agent_loop.call_llm" {
			implRuns++
		}
	}
	require.Equal(t, 3, implRuns,
		"each of the 3 candidates must run its agent body (completed=%v)",
		res.Execution.NodesCompleted)
}

// An UNMOCKED ref stays a black box. This is the load-bearing half of the gate:
// builtin://agent's loop continues on a completion signal an empty mock never
// supplies, and `max_turns: 0` means unlimited, so opening every ref by default
// does not merely produce wrong output — it does not terminate (a measured
// 766,072-iteration runaway).
func TestUnmockedRefStaysBlackBoxed(t *testing.T) {
	wf := loadBuiltin(t, "parallel-compete")
	all := loadScenarios(t, "../../builtin/testdata/parallel-compete_scenarios.yaml")

	// This scenario mocks `review` as a NODE (not its internals), and never
	// mentions synthesizer's body at all.
	sc := findScenario(t, all, "loop_failure_null_review")

	transparent := transparentRefs(wf, sc.Events)
	require.False(t, transparent["structured-agent"],
		"an event on the ref NODE mocks that node and must leave its body opaque")

	res := NewRunner(wf).Run(sc)
	require.Equal(t, "completed", res.Execution.Outcome,
		"a black-boxed ref must not spin; error=%v", res.Execution.Error)
	for _, id := range res.Execution.NodesReached {
		require.False(t, strings.Contains(id, blackBoxNodeID),
			"harness scaffolding must never appear as a reached node: %q", id)
	}
}
