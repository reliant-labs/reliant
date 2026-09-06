// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// TestParity_SimulatorVsTemporal runs the SAME scenarios through both backends
// and reports every disagreement.
//
// A disagreement is a real signal, not test noise: the two backends consume an
// identical scenario and assert through an identical evaluator
// (simulator.CheckExpectations), so the only thing that can differ is what
// EXECUTED. Either the fast simulator is routing somewhere the real workflow
// does not, or this backend is mocking something the real workflow treats
// differently.
//
// It reports rather than fails. The point of the lane is enumerating the gap;
// converting the gap into a red build before it has been triaged would just
// mean the lane gets skipped.
func TestParity_SimulatorVsTemporal(t *testing.T) {
	cases := []struct {
		workflow  string
		scenarios string
	}{
		{"structured-agent", "../../builtin/testdata/structured-agent_scenarios.yaml"},
		{"agent", "../../builtin/testdata/agent_scenarios.yaml"},
		{"parallel-compete", "../../builtin/testdata/parallel-compete_scenarios.yaml"},
		{"get-it-right", "../../builtin/testdata/get-it-right_scenarios.yaml"},
		{"one-ring", "../../builtin/testdata/one-ring_scenarios.yaml"},
	}

	type divergence struct {
		workflow, scenario     string
		simStatus, tmpStatus   simulator.ScenarioStatus
		simReached, tmpReached []string
		tmpMismatches          []string
	}
	var divergences []divergence
	agree := 0

	for _, tc := range cases {
		wf := loadBuiltin(t, tc.workflow)
		scenarios := loadScenarios(t, tc.scenarios)

		simEngine := simulator.NewEngine(wf)
		tmpRunner := NewRunner(wf)

		for _, sc := range scenarios {
			simRes := simEngine.RunScenario(sc)
			tmpRes := tmpRunner.Run(sc)

			if simRes.Status == tmpRes.Status &&
				sameSet(simRes.Execution.NodesReached, tmpRes.Execution.NodesReached) {
				agree++
				continue
			}
			divergences = append(divergences, divergence{
				workflow: tc.workflow, scenario: sc.Name,
				simStatus: simRes.Status, tmpStatus: tmpRes.Status,
				simReached:    simRes.Execution.NodesReached,
				tmpReached:    tmpRes.Execution.NodesReached,
				tmpMismatches: tmpRes.Mismatches,
			})
		}
	}

	t.Logf("PARITY: %d scenarios agree, %d diverge", agree, len(divergences))
	for _, d := range divergences {
		t.Logf("--- %s / %s", d.workflow, d.scenario)
		t.Logf("    sim: status=%s reached=%v", d.simStatus, d.simReached)
		t.Logf("    tmp: status=%s reached=%v", d.tmpStatus, d.tmpReached)
		for _, m := range d.tmpMismatches {
			t.Logf("    tmp mismatch: %s", m)
		}
	}
	require.NotZero(t, agree, "no scenario agreed on any backend — the harness itself is broken")
}

// sameSet compares reached-node lists as SETS.
//
// Deduplication is required, not a shortcut: the simulator appends a node once
// per loop iteration while the Temporal backend records first-reach only, and
// CheckExpectations itself reads NodesReached through a set
// (`reachedSet[expected]`). Comparing multiplicity would report a difference
// no expectation can express.
func sameSet(a, b []string) bool {
	norm := func(in []string) string {
		seen := map[string]bool{}
		out := []string{}
		for _, s := range in {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	return norm(a) == norm(b)
}
