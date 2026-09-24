// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// TestBuiltinScenarios_RealRuntime runs every builtin scenario directory
// (internal/workflow/builtin/scenarios/<workflow>/*.yaml) through the REAL
// DynamicWorkflow and FAILS on any mismatch.
//
// The fast simulator (builtin.TestBuiltinWorkflowScenarios) walks the graph
// itself and has no activity layer, so a scenario passing there proves only
// "given these node outputs, the graph routes here". This lane is what proves
// a builtin actually RUNS: real node-config and inject evaluation, real
// condition skips, real declared-output evaluation, and — through the
// delegated save_message resolution in the activity mocks — the real
// save_message templates. A scenario that the simulator passes and this lane
// fails is a runtime failure the simulator cannot see.
//
// Scenarios listed in knownRealRuntimeGaps are reported, not failed, each with
// the reason it cannot pass yet. That list must only shrink.
func TestBuiltinScenarios_RealRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("real-runtime scenario lane: runs every builtin scenario through DynamicWorkflow")
	}
	// Both scenario sources builtin.TestBuiltinWorkflowScenarios runs:
	// scenarios/<workflow>/*.yaml and testdata/<workflow>_scenarios.yaml.
	byWorkflow := map[string][]scenarioSource{}
	dirFiles, err := fs.Glob(builtin.BuiltinScenarioDirsFS, "scenarios/*/*.yaml")
	require.NoError(t, err)
	for _, f := range dirFiles {
		wfName := path.Base(path.Dir(f))
		byWorkflow[wfName] = append(byWorkflow[wfName], scenarioSource{fsys: builtin.BuiltinScenarioDirsFS, file: f})
	}
	testdataFiles, err := fs.Glob(builtin.BuiltinScenariosFS, "testdata/*_scenarios.yaml")
	require.NoError(t, err)
	for _, f := range testdataFiles {
		wfName := strings.TrimSuffix(path.Base(f), "_scenarios.yaml")
		if _, err := builtin.BuiltinWorkflowsFS.ReadFile(wfName + ".yaml"); err != nil {
			continue // e.g. presets_validation.yaml: not a workflow's scenarios
		}
		byWorkflow[wfName] = append(byWorkflow[wfName], scenarioSource{fsys: builtin.BuiltinScenariosFS, file: f})
	}
	require.NotEmpty(t, byWorkflow)
	names := make([]string, 0, len(byWorkflow))
	for n := range byWorkflow {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, wfName := range names {
		wfName := wfName
		t.Run(wfName, func(t *testing.T) {
			t.Parallel()
			wf := loadBuiltin(t, wfName)
			runner := NewRunner(wf)
			sources := byWorkflow[wfName]
			sort.Slice(sources, func(i, j int) bool { return sources[i].file < sources[j].file })
			for _, src := range sources {
				file := src.file
				data, err := fs.ReadFile(src.fsys, file)
				require.NoError(t, err)
				scenarios, err := simulator.ParseScenarioYAML(data)
				require.NoError(t, err, file)
				for _, sc := range scenarios {
					sc := sc
					t.Run(sc.Name, func(t *testing.T) {
						res := runner.Run(sc)
						if res.Status == simulator.StatusPassed {
							return
						}
						detail := describeResult(res)
						if reason, known := knownRealRuntimeGaps[wfName+"/"+sc.Name]; known {
							t.Logf("KNOWN GAP (%s): %s\n%s", reason, file, detail)
							return
						}
						t.Errorf("%s failed on the real runtime:\n%s", file, detail)
					})
				}
			}
		})
	}
}

type scenarioSource struct {
	fsys fs.FS
	file string
}

// knownRealRuntimeGaps maps "<workflow>/<scenario>" to why that scenario
// cannot pass on the real runtime yet. Every entry is a tracked defect; the
// list must only shrink.
var knownRealRuntimeGaps = map[string]string{
	// Preset-mode routers (router with `presets`, decision field
	// selected_preset) are not mocked by this backend: nodeRoutingDecision*
	// handles node-mode routing only, so the routing CallLLM gets an empty
	// response and the run fails exactly as production would.
	"default-router/routes_to_agent":            "runner: preset-mode router decisions are not mocked",
	"default-router/routes_to_one_ring":         "runner: preset-mode router decisions are not mocked",
	"default-router/routes_to_implement_review": "runner: preset-mode router decisions are not mocked",

	// `black_box: true` on a LOOP node (mocking a parallel loop's aggregate
	// _results) is not supported: this backend black-boxes `ref:` workflow
	// nodes only, so the slide_pipeline event goes unconsumed and the loop
	// runs its real body against unmocked nodes. Every pitch-deck scenario
	// that reaches slide_pipeline hits it; those that stop before
	// (resume_into_current_node) pass, as does everything up to and including
	// plan_deck in the others.
	"pitch-deck/happy_path":                    "runner: black_box on a loop node is not supported",
	"pitch-deck/happy_path#01":                 "runner: black_box on a loop node is not supported",
	"pitch-deck/loop_save_message_self_ref":    "runner: black_box on a loop node is not supported",
	"pitch-deck/loop_save_message_self_ref#01": "runner: black_box on a loop node is not supported",
	"pitch-deck/slide_pipeline_parallel_write": "runner: black_box on a loop node is not supported",
	"pitch-deck/slide_review_results":          "runner: black_box on a loop node is not supported",
	"pitch-deck/start_from_founder_interview":  "runner: black_box on a loop node is not supported",
	"pitch-deck/start_from_research":           "runner: black_box on a loop node is not supported",
	"pitch-deck/visual_review_retry":           "runner: black_box on a loop node is not supported",
	"pitch-deck/visual_review_retry#01":        "runner: black_box on a loop node is not supported",

	// These scenarios key events by the ref URL ("builtin://agent"), an
	// addressing form only the fast simulator resolves.
	"parallel-loop-sample/keyed_parallel_results_route_to_pass": "runner: events keyed by ref URL are simulator-only",
	"parallel-loop-sample/single_item_routes_to_review":         "runner: events keyed by ref URL are simulator-only",

	// After a denied approval the real agent loop calls the LLM again (its
	// `while` still sees the denied iteration's tool_calls); the simulator
	// does not, so the scenario supplies one call_llm event too few. A
	// simulator/runtime divergence in the agent loop, not a validation issue.
	"agent/manual_mode_denied": "sim/runtime divergence: agent loop re-enters after a denied approval",
}

func describeResult(res *Result) string {
	var b strings.Builder
	b.WriteString("  status=" + string(res.Status) + " outcome=" + res.Execution.Outcome + "\n")
	if res.Execution.Error != nil {
		b.WriteString("  error: " + res.Execution.Error.Message + "\n")
	}
	b.WriteString("  reached: " + strings.Join(res.Execution.NodesReached, ", ") + "\n")
	for _, m := range res.Mismatches {
		b.WriteString("  mismatch: " + m + "\n")
	}
	return b.String()
}
