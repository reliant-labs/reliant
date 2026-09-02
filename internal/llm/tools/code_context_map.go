// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"
)

// Call map for code_context.
//
// This is the feature that targets the expensive shape directly. A depth-1
// answer tells an agent who calls a function; understanding a REQUEST PATH
// means asking that again at each caller, and each hop is a turn whose cost is
// dominated by fresh deliberation about where to look next. Measured on a real
// session, model time correlated with generated reasoning at r=0.97 — so a
// 10-hop walk is ~10 rounds of thinking, not 10 cheap lookups.
//
// Expanding the tree inside ONE call removes the deliberation between hops.
// The expansion is also PARALLEL, which is what makes it affordable: measured
// against gopls, six queries cost ~7s run together versus ~15s run in sequence,
// and the shared-daemon mode (-remote=auto) is SLOWER than direct invocation
// (4.1s vs 2.6s per call), so concurrency is the only lever that works.
//
// The guard rails matter as much as the traversal. An unbounded tree is a
// worse failure than a shallow one: it spends the tokens saved by removing
// turns, and buries the path the reader wanted. So the walk is capped on nodes,
// deduplicated, and cycle-safe, and it always reports what it left out.

const (
	// codeContextDefaultDepth is a real trace, not one hop. The tool exists to
	// remove turns, and "who ultimately triggers this" is the question agents
	// actually have; answering one level at a time is the multi-turn walk this
	// replaces. Three levels covers typical handler->service->repo layering.
	codeContextDefaultDepth = 3

	// codeContextMaxDepth caps traversal. Past five the node budget dominates
	// anyway, so deeper requests widen the tree rather than lengthen it.
	codeContextMaxDepth = 5

	// codeContextMaxNodes bounds total expansions across the whole walk. A
	// heavily-called utility fans out combinatorially, and the useful answer
	// there is "this is called everywhere", which this cap states explicitly.
	codeContextMaxNodes = 60

	// codeContextMaxFanout limits children expanded per node. Breadth past this
	// is noise in a trace; the full list is still available at depth 1.
	codeContextMaxFanout = 8

	// codeContextParallelism bounds concurrent language-server queries. Each
	// gopls invocation is its own process; too many at once starves the box and
	// the wall-clock gain flattens.
	codeContextParallelism = 6
)

// callNode is one function in the call map.
type callNode struct {
	Location codeLocation
	Children []*callNode
	// Elided records children that existed but were not expanded, so the
	// output can distinguish "leaf" from "stopped looking here".
	Elided int
	// Repeat marks a node already expanded elsewhere in the tree; recursion and
	// diamonds are common and re-expanding them wastes the node budget.
	Repeat bool
}

// buildCallMap expands callers (or callees) breadth-first to maxDepth.
//
// Breadth-first, not depth-first: with a node budget, BFS spends it on the
// levels nearest the symbol, which is where the answer usually is. DFS would
// exhaust the budget down one arbitrary branch.
func buildCallMap(
	ctx context.Context,
	engine languageEngine,
	root string,
	origin codeLocation,
	direction string,
	maxDepth int,
	scope scopeMode,
) (*callNode, int) {
	if maxDepth > codeContextMaxDepth {
		maxDepth = codeContextMaxDepth
	}
	if maxDepth < 1 {
		maxDepth = 1
	}

	rootNode := &callNode{Location: origin}
	seen := map[string]bool{locationKey(origin): true}
	budget := codeContextMaxNodes
	truncated := 0

	frontier := []*callNode{rootNode}
	for depth := 0; depth < maxDepth && len(frontier) > 0 && budget > 0; depth++ {
		// One level at a time, all queries concurrent. This is where the
		// wall-clock win comes from.
		results := make([][]codeLocation, len(frontier))
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(codeContextParallelism)

		for i, node := range frontier {
			if node.Repeat {
				continue
			}
			group.Go(func() error {
				// Query the enclosing DECLARATION, not the call site: a
				// language server resolves a symbol at a position, and a call
				// site's position names the callee, not the caller.
				results[i] = engine.ResolveEdges(groupCtx, root, node.Location.traversalPoint(), direction)
				return nil
			})
		}
		// A failing language-server query yields an empty level rather than a
		// failed call: a partial map is still worth returning.
		_ = group.Wait()

		var next []*callNode
		for i, node := range frontier {
			// Filter BEFORE spending budget. An out-of-scope edge would
			// otherwise consume a node slot and a language-server query at the
			// next level, trading depth in the user's own code for a walk
			// through the standard library.
			scoped, _ := filterInScope(root, results[i], scope)
			edges := dedupeLocations(scoped)
			for j, edge := range edges {
				if budget <= 0 || j >= codeContextMaxFanout {
					node.Elided += len(edges) - j
					truncated += len(edges) - j
					break
				}
				child := &callNode{Location: edge}
				key := locationKey(edge)
				if seen[key] {
					child.Repeat = true
				} else {
					seen[key] = true
					next = append(next, child)
				}
				node.Children = append(node.Children, child)
				budget--
			}
		}
		frontier = next
	}

	return rootNode, truncated
}

// locationKey identifies a node for cycle detection. The enclosing function
// name is the stable identity — a function reached from two call sites is the
// same node, and expanding it twice wastes budget on a duplicate subtree.
func locationKey(loc codeLocation) string {
	if loc.Enclosing != "" {
		return loc.Path + "#" + loc.Enclosing
	}
	return fmt.Sprintf("%s:%d", loc.Path, loc.Line)
}

// dedupeLocations collapses repeated call sites. A caller that invokes the
// target in a loop and a branch is still one caller for mapping purposes.
func dedupeLocations(locs []codeLocation) []codeLocation {
	seen := make(map[string]bool, len(locs))
	out := make([]codeLocation, 0, len(locs))
	for _, l := range locs {
		key := locationKey(l)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	// Stable, source-first ordering: tests and generated code are real callers
	// but rarely the one the reader is tracing.
	sort.SliceStable(out, func(i, j int) bool {
		return declRank(out[i].Path) < declRank(out[j].Path)
	})
	return out
}

// writeCallMap renders the tree as an indented outline.
func writeCallMap(out *strings.Builder, root string, node *callNode, direction string, truncated int) {
	title := "CALL MAP — who calls this (inbound)"
	if direction == "callees" {
		title = "CALL MAP — what this calls (outbound)"
	}
	fmt.Fprintf(out, "%s\n", title)

	if len(node.Children) == 0 {
		fmt.Fprintf(out, "  (no edges found)\n\n")
		return
	}
	for i, child := range node.Children {
		writeCallNode(out, root, child, "  ", i == len(node.Children)-1)
	}
	if truncated > 0 {
		fmt.Fprintf(out, "  (%d more edges not expanded — raise `depth` or query a specific symbol)\n", truncated)
	}
	out.WriteString("\n")
}

func writeCallNode(out *strings.Builder, root string, node *callNode, prefix string, last bool) {
	branch := "├─ "
	childPrefix := prefix + "│  "
	if last {
		branch = "└─ "
		childPrefix = prefix + "   "
	}

	label := node.Location.Enclosing
	if label == "" {
		label = "(anonymous)"
	}
	fmt.Fprintf(out, "%s%s%s  %s:%d", prefix, branch, label,
		relativizePath(root, node.Location.Path), node.Location.Line)
	switch {
	case node.Repeat:
		out.WriteString("  [already shown]")
	case node.Elided > 0:
		fmt.Fprintf(out, "  [+%d more]", node.Elided)
	}
	out.WriteString("\n")

	for i, child := range node.Children {
		writeCallNode(out, root, child, childPrefix, i == len(node.Children)-1)
	}
}
