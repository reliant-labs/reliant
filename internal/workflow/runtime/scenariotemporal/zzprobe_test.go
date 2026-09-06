// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"fmt"
	"testing"
)

// TEMPORARY PROBE — delete before finishing.
func TestZZProbe_OneRingDispatch(t *testing.T) {
	wf := loadBuiltin(t, "one-ring")
	scenarios := loadScenarios(t, "../../builtin/testdata/one-ring_scenarios.yaml")
	sc := scenarios[0]
	fmt.Printf("PROBE scenario=%s\n", sc.Name)
	for _, e := range sc.Events {
		fmt.Printf("PROBE scenario-key node=%q\n", e.Node)
	}
	res := NewRunner(wf).Run(sc)
	fmt.Printf("PROBE status=%s reached=%v\n", res.Status, res.Execution.NodesReached)
	for _, m := range res.Mismatches {
		fmt.Printf("PROBE mismatch: %s\n", m)
	}
}
