// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/stretchr/testify/require"
)

// TestBuiltinScenarioFalsePassSurvey reports how much of the CI scenario corpus
// passes while NOT executing what it appears to.
//
// It asserts nothing about the counts — both black-boxing a sub-workflow and
// accepting a router's default candidate are legitimate when deliberate, so a
// threshold here would be a guess that fails on the next honest scenario. The
// value is the log: run it with -v to see which scenarios are affected and
// decide, per scenario, whether the gap is intended.
func TestBuiltinScenarioFalsePassSurvey(t *testing.T) {
	t.Parallel()

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)

	var total, withBlackBox, withRouterDefault int
	var blackBoxLines, routerLines []string

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		workflowName := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".yml"), ".yaml")

		workflowData, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		require.NoError(t, err)
		wf, err := v2.ParseWorkflowProtoBytesWithLoader(workflowData, builtinLoader)
		require.NoError(t, err)

		scenarios, err := loadScenariosForWorkflow(workflowName)
		if err != nil || len(scenarios) == 0 {
			continue
		}

		engine := simulator.NewEngine(wf)
		for _, scenario := range scenarios {
			total++
			result := engine.RunScenario(scenario)

			var sawBlackBox, sawRouter bool
			for _, w := range result.Warnings {
				label := workflowName + "/" + scenario.Name + " [" + string(result.Status) + "]: " + w
				switch {
				case strings.HasPrefix(w, "black-box sub-workflow"):
					sawBlackBox = true
					blackBoxLines = append(blackBoxLines, label)
				case strings.HasPrefix(w, "unmocked node router"):
					sawRouter = true
					routerLines = append(routerLines, label)
				}
			}
			if sawBlackBox {
				withBlackBox++
			}
			if sawRouter {
				withRouterDefault++
			}
		}
	}

	sort.Strings(blackBoxLines)
	sort.Strings(routerLines)

	t.Logf("corpus scenarios run: %d", total)
	t.Logf("scenarios black-boxing at least one sub-workflow: %d (%d warnings)", withBlackBox, len(blackBoxLines))
	for _, line := range blackBoxLines {
		t.Logf("  BLACKBOX %s", line)
	}
	t.Logf("scenarios with at least one defaulted node router: %d (%d warnings)", withRouterDefault, len(routerLines))
	for _, line := range routerLines {
		t.Logf("  ROUTER   %s", line)
	}
}
