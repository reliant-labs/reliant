// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// =============================================================================
// UNSATISFIABLE ALL-JOINS
// =============================================================================
//
// An all-join fires once EVERY source has completed or been skipped. A
// condition-skipped node still publishes a completion (its skip output), so a
// source only fails to arrive when it is never TRIGGERED — and the one thing
// that stops a node being triggered is an edge choosing a different outcome.
//
// An edge with `cases` is an exclusive choice: the first case whose condition
// holds wins, otherwise `default` (core.WorkflowProcessor.matchEdgeTargets).
// Exactly one outcome's targets are triggered. So if every path to source A
// passes through outcome i of an edge E, and every path to source B passes
// through a different outcome j of the same E, A and B can never both be
// triggered in one run: the join waits forever. That is how migrate.yaml's
// results join hung on every run (no_workflow_candidates XOR
// workflow_builder_loop, both wired into one all-join).
//
// The check is deliberately narrow so it can be an ERROR: it only reports
// sources whose EVERY path from the workflow entry is forced through a
// specific outcome of the same edge. A source reachable some other way, or a
// source that sits downstream of both outcomes, is never reported.

// validateAllJoinReachability reports all-joins with mutually exclusive sources.
func validateAllJoinReachability(wf *reliantv1.Workflow, basePath []string, result *Result) {
	g := newEdgeGraph(wf)
	for i, node := range wf.GetNodes() {
		if !isAllJoin(node) {
			continue
		}
		joinID := node.GetId()
		sources := g.preds[joinID]
		if len(sources) < 2 {
			continue
		}
		for _, choice := range g.choices {
			forced := map[int][]string{}
			for _, src := range sources {
				if outcome, ok := g.forcedOutcome(src, choice); ok {
					forced[outcome] = append(forced[outcome], src)
				}
			}
			if len(forced) < 2 {
				continue
			}
			var groups []string
			for _, outcome := range sortedIntKeys(forced) {
				srcs := forced[outcome]
				sort.Strings(srcs)
				groups = append(groups, fmt.Sprintf("%s (%s)", strings.Join(srcs, ", "), choice.outcomeLabel(outcome)))
			}
			result.Add(&Error{
				Severity: SeverityError,
				Category: CategoryStructure,
				Path:     append(append([]string{}, basePath...), "nodes", fmt.Sprintf("[%d](%s)", i, joinID)),
				Message: fmt.Sprintf(
					"all-join '%s' can never be satisfied: its sources are on mutually exclusive branches of the edge from '%s' — only one of %s can run, so the join waits forever",
					joinID, choice.from, strings.Join(groups, " / ")),
				Suggestion: "give the exclusive branches their own `condition: any` join and feed that into this join, or make this join `condition: any`",
			})
			break // one report per join
		}
	}
}

// edgeChoice is one edge with `cases`: an exclusive choice among outcomes.
// Outcome k < len(cases) is case k; outcome len(cases) is `default`.
type edgeChoice struct {
	from     string
	outcomes [][]string
}

func (c edgeChoice) outcomeLabel(i int) string {
	if i == len(c.outcomes)-1 {
		return "default"
	}
	return fmt.Sprintf("case %d", i)
}

type edgeGraph struct {
	entries []string
	succ    map[string][]string
	preds   map[string][]string
	choices []edgeChoice
}

func newEdgeGraph(wf *reliantv1.Workflow) *edgeGraph {
	g := &edgeGraph{entries: wf.GetEntry(), succ: map[string][]string{}, preds: map[string][]string{}}
	add := func(from, to string) {
		g.succ[from] = append(g.succ[from], to)
		g.preds[to] = append(g.preds[to], from)
	}
	for _, edge := range wf.GetEdges() {
		from := edge.GetFrom()
		if len(edge.GetCases()) > 0 {
			choice := edgeChoice{from: from}
			for _, c := range edge.GetCases() {
				choice.outcomes = append(choice.outcomes, c.GetTo())
			}
			choice.outcomes = append(choice.outcomes, edge.GetDefault())
			g.choices = append(g.choices, choice)
		}
		for _, c := range edge.GetCases() {
			for _, to := range c.GetTo() {
				add(from, to)
			}
		}
		for _, to := range edge.GetDefault() {
			add(from, to)
		}
	}
	for _, node := range wf.GetNodes() {
		if node.GetType() == model.NodeTypeRouter {
			for _, cand := range node.GetRouter().GetNodes() {
				add(node.GetId(), cand.GetId())
			}
		}
	}
	return g
}

// forcedOutcome reports whether every path from the entry to target passes
// through exactly one outcome of choice (and returns it). A target reachable
// without passing choice.from's edge, or through more than one of its
// outcomes, is not forced.
func (g *edgeGraph) forcedOutcome(target string, choice edgeChoice) (int, bool) {
	// Reachable from the entry while never taking this choice?
	if g.reaches(g.entries, target, func(from, to string) bool { return from == choice.from && choiceTarget(choice, to) }) {
		return 0, false
	}
	found := -1
	for i, targets := range choice.outcomes {
		if len(targets) == 0 {
			continue
		}
		// Reachable from this outcome's targets (without re-taking the choice)?
		if g.reaches(targets, target, func(from, to string) bool { return from == choice.from && choiceTarget(choice, to) }) {
			if found >= 0 {
				return 0, false // reachable through two outcomes: not forced
			}
			found = i
		}
	}
	return found, found >= 0
}

func choiceTarget(choice edgeChoice, id string) bool {
	for _, targets := range choice.outcomes {
		for _, t := range targets {
			if t == id {
				return true
			}
		}
	}
	return false
}

// reaches reports whether target is reachable from any start node, skipping
// edges for which blocked returns true. A start node equal to target counts.
func (g *edgeGraph) reaches(starts []string, target string, blocked func(from, to string) bool) bool {
	seen := map[string]bool{}
	stack := append([]string{}, starts...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == target {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		for _, next := range g.succ[n] {
			if !blocked(n, next) {
				stack = append(stack, next)
			}
		}
	}
	return false
}

func sortedIntKeys(m map[int][]string) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
