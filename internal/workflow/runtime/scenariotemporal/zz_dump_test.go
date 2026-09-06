// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"fmt"
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
)

func TestZZDump_Parity(t *testing.T) {
	f, err := os.Create("/tmp/scratchpad/parity.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "start\n")
	cases := []struct{ workflow, scenarios string }{
		{"structured-agent", "../../builtin/testdata/structured-agent_scenarios.yaml"},
		{"agent", "../../builtin/testdata/agent_scenarios.yaml"},
		{"parallel-compete", "../../builtin/testdata/parallel-compete_scenarios.yaml"},
		{"get-it-right", "../../builtin/testdata/get-it-right_scenarios.yaml"},
		{"one-ring", "../../builtin/testdata/one-ring_scenarios.yaml"},
	}
	agree, diverge := 0, 0
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
			diverge++
			fmt.Fprintf(f, "--- %s / %s\n", tc.workflow, sc.Name)
			fmt.Fprintf(f, "    sim: status=%s reached=%v\n", simRes.Status, simRes.Execution.NodesReached)
			fmt.Fprintf(f, "    tmp: status=%s reached=%v\n", tmpRes.Status, tmpRes.Execution.NodesReached)
			if tmpRes.Execution.Error != nil {
				fmt.Fprintf(f, "    tmp error: %s\n", tmpRes.Execution.Error.Message)
			}
			for _, m := range tmpRes.Mismatches {
				fmt.Fprintf(f, "    tmp mismatch: %s\n", m)
			}
		}
	}
	fmt.Fprintf(f, "PARITY: %d agree, %d diverge\n", agree, diverge)
}
