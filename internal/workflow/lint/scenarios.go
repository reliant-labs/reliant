// Copyright (c) 2025 Reliant Labs
package lint

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// checkScenarios: the scenarios should start the workflow every way it can
// start (each trigger) and end it every way it can end (each terminal node).
// Coverage is read from what the scenarios ASSERT, not from what a run
// happened to do: a terminal is covered by a scenario that expects it
// (completed, reached, node_outputs, messages or error_node), so the
// coverage is a property of the files, checked without running anything.
func (c *checker) checkScenarios() {
	wf := c.wf
	if len(c.in.Scenarios) == 0 {
		c.add(RuleScenarioNone, "", "the workflow has no scenarios, so none of its triggers or endings is tested",
			"add scenarios under .reliant/workflows/"+wf.GetName()+"/scenarios/")
		return
	}

	fired := map[string]bool{}
	expected := map[string]bool{}
	for _, sc := range c.in.Scenarios {
		if sc == nil {
			continue
		}
		if sc.Trigger != nil {
			fired[sc.Trigger.Name] = true
		}
		if e := sc.Expect; e != nil {
			for _, list := range [][]string{e.Completed, e.Reached} {
				for _, id := range list {
					expected[id] = true
				}
			}
			for id := range e.NodeOutputs {
				expected[id] = true
			}
			for id := range e.Messages {
				expected[id] = true
			}
			if e.ErrorNode != "" {
				expected[e.ErrorNode] = true
			}
		}
	}

	for _, wt := range wf.GetTriggers() {
		if !fired[wt.GetName()] {
			c.add(RuleScenarioTrigger, "", fmt.Sprintf("no scenario fires trigger %q, so its filter and inputs mapping are untested", wt.GetName()),
				fmt.Sprintf("add a scenario with `trigger: {name: %s, payload: {...}}`", wt.GetName()))
		}
	}

	next := successors(wf)
	for _, n := range wf.GetNodes() {
		id := n.GetId()
		if len(next[id]) > 0 || n.GetType() == model.NodeTypeRouter {
			continue
		}
		if !expected[id] {
			c.add(RuleScenarioTerminal, id, "no scenario expects a run to end here",
				"add a scenario whose expect.completed (or reached) names "+id)
		}
	}
}
