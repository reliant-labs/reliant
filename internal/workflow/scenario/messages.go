// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"fmt"
	"sort"
	"strings"
)

// checkMessageExpectations evaluates `expect.messages` against the messages a
// run saved.
//
// The runner resolves every save_message through the runtime's own path, so
// saved is the complete record of what the run wrote.
func checkMessageExpectations(expect map[string]MessageExpectation, saved []SavedMessage) []string {
	if len(expect) == 0 {
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
