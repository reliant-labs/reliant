// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"sort"
	"sync"
	"testing"
)

// TestDiag_GetItRight prints, for each get-it-right scenario, which sub-workflow
// names transparentRefs opened and which node ids the scenario keys events on.
// TEMPORARY diagnostic — delete before finishing.
func TestDiag_GetItRight(t *testing.T) {
	wf := loadBuiltin(t, "get-it-right")
	scenarios := loadScenarios(t, "../../builtin/testdata/get-it-right_scenarios.yaml")

	for _, sc := range scenarios {
		transparent := transparentRefs(wf, sc.Events)
		names := make([]string, 0, len(transparent))
		for n := range transparent {
			names = append(names, n)
		}
		sort.Strings(names)

		var evNodes []string
		for _, e := range sc.Events {
			evNodes = append(evNodes, e.Node)
		}
		t.Logf("SCENARIO %s\n  transparent=%v\n  eventNodes=%v", sc.Name, names, evNodes)
	}
}

// diagLookups records every id events.next() was called with, globally.
var (
	diagMu      sync.Mutex
	diagLookups []string
)

// TestDiag_DispatchIDs runs one scenario and prints every id the harness looked
// up a mock under. TEMPORARY diagnostic.
func TestDiag_DispatchIDs(t *testing.T) {
	wf := loadBuiltin(t, "get-it-right")
	scenarios := loadScenarios(t, "../../builtin/testdata/get-it-right_scenarios.yaml")
	sc := findScenario(t, scenarios, "happy_path_no_retry")

	diagMu.Lock()
	diagLookups = nil
	diagEnabled = true
	diagHook = func(id string) {
		diagMu.Lock()
		diagLookups = append(diagLookups, id)
		diagMu.Unlock()
	}
	diagMu.Unlock()

	res := NewRunner(wf).Run(sc)

	diagMu.Lock()
	got := append([]string(nil), diagLookups...)
	diagEnabled = false
	diagMu.Unlock()

	t.Logf("DISPATCHED-IDS (in order): %v", got)
	t.Logf("REACHED: %v", res.Execution.NodesReached)
	for _, m := range res.Mismatches {
		t.Logf("MISMATCH: %s", m)
	}
}
