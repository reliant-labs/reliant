// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"fmt"
	"sort"
	"strings"
)

// checkMessageExpectations evaluates `expect.messages` against the messages a
// run saved.
//
// saved == nil means the backend does not observe saves at all (the fast
// simulator walks the graph without an activity layer, so no save_message
// template is ever resolved). The assertion is then not evaluated here — it
// is not failed, since the scenario is not wrong, and it is never passed
// silently: messagesUnverifiedWarning reports it through the result's
// false-pass warnings.
func checkMessageExpectations(expect map[string]MessageExpectation, saved []SavedMessage) []string {
	if len(expect) == 0 || saved == nil {
		return nil
	}

	byNode := map[string][]SavedMessage{}
	for _, m := range saved {
		byNode[m.Node] = append(byNode[m.Node], m)
	}

	nodes := make([]string, 0, len(expect))
	for n := range expect {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var mismatches []string
	for _, node := range nodes {
		want := expect[node]
		got := byNode[node]
		if want.Saved != nil {
			if *want.Saved && len(got) == 0 {
				mismatches = append(mismatches, fmt.Sprintf("expected node %q to save a message, but it saved none (saved by: %s)", node, savedNodeList(byNode)))
			}
			if !*want.Saved && len(got) > 0 {
				mismatches = append(mismatches, fmt.Sprintf("expected node %q to save no message, but it saved %d (first: %q)", node, len(got), truncate(got[0].Content, 120)))
			}
		}
		if want.Count != nil && len(got) != *want.Count {
			mismatches = append(mismatches, fmt.Sprintf("expected node %q to save %d message(s), but it saved %d", node, *want.Count, len(got)))
		}
		if want.Role != "" {
			for _, m := range got {
				if m.Role != want.Role {
					mismatches = append(mismatches, fmt.Sprintf("node %q saved a %q message, expected role %q", node, m.Role, want.Role))
					break
				}
			}
		}
		for _, needle := range want.ContentContains {
			found := false
			for _, m := range got {
				if strings.Contains(m.Content, needle) {
					found = true
					break
				}
			}
			if !found {
				mismatches = append(mismatches, fmt.Sprintf("no message saved by node %q contains %q (saved %d)", node, needle, len(got)))
			}
		}
		for _, needle := range want.ContentNotContains {
			for _, m := range got {
				if strings.Contains(m.Content, needle) {
					mismatches = append(mismatches, fmt.Sprintf("a message saved by node %q contains %q, which it must not", node, needle))
					break
				}
			}
		}
	}
	return mismatches
}

func savedNodeList(byNode map[string][]SavedMessage) string {
	if len(byNode) == 0 {
		return "none"
	}
	nodes := make([]string, 0, len(byNode))
	for n := range byNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return strings.Join(nodes, ", ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// messagesUnverifiedWarning is the false-pass warning for a scenario whose
// expect.messages the backend could not evaluate.
func messagesUnverifiedWarning(scenario *Scenario, exec *ExecutionDetails) string {
	if scenario == nil || scenario.Expect == nil || len(scenario.Expect.Messages) == 0 || exec.SavedMessages != nil {
		return ""
	}
	return fmt.Sprintf("expect.messages (%d node(s)) was NOT verified: this backend does not execute save_message; the Temporal backend (scenariotemporal) does", len(scenario.Expect.Messages))
}
