// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// =============================================================================
// NODE EXECUTION-ORDER ANALYSIS
// =============================================================================
//
// A nodes.<id> reference in a node's config is only safe when <id> is
// guaranteed to have produced an output by the time the expression is
// evaluated. The runtime hard-fails ("no such key: <id>") when the referenced
// node has not run — which happens on router skip paths, parallel branches,
// and conditional edges.
//
// This analysis computes, per node B, the set of nodes guaranteed to have an
// output before B starts (the "guaranteed-before" set G(B)):
//
//   - contribution(p) = G(p) ∪ {p} for every predecessor p. Skipped
//     conditional nodes still publish a skip output and propagate downstream,
//     so the KEY nodes.<p> is present either way (field-level risk on skipped
//     nodes is covered separately by the conditional-access warning).
//   - join "all":  G(join) = union of all predecessor contributions
//     (an all-join fires only after every source completed or was skipped).
//   - join "any" and every other node with multiple in-edges:
//     G(n) = intersection of predecessor contributions (any single inbound
//     path can trigger the node).
//   - entry nodes: G = {} (multiple entries run in parallel — nothing is
//     guaranteed). An entry node that also has in-edges keeps G = {}.
//   - router candidates count as edges router → candidate.
//
// The honest limits of this analysis: it proves ordering from the edge graph
// only. It does NOT claim value-level reachability (e.g. that a condition can
// actually be true) — which is why references to nodes outside G(B) are
// WARNINGS, except references to nodes that provably run AFTER B (B ∈ G(X)),
// which can never be satisfied and are ERRORS.
//
// RESUME-AT-POSITION COUPLING: the runtime's resume mode
// (internal/workflow/runtime/workflow.go, resolveResumeTarget) enters a new
// run directly at a mid-graph node with NO node outputs reconstructed from the
// prior run — thread history is the only carried state. Any nodes.<id>
// reference evaluated from the resume node onward therefore sees the same
// "predecessor never ran" world this analysis warns about on router-skip and
// parallel paths. The has()-guard discipline this validation enforces is what
// makes workflows resumable mid-graph; weakening it silently breaks resume.

// nodeOrderScope describes the evaluation scope of an expression for ordering
// validation: which node it belongs to and whether it evaluates after that
// node completes (save_message, outbound edge conditions) or before it starts
// (config, inject, node condition).
type nodeOrderScope struct {
	nodeID    string
	afterNode bool
}

// computeGuaranteedBefore computes G(B) for every node in the workflow.
// The workflow graph is validated acyclic before CEL validation runs; a
// bounded fixpoint guards against pathological inputs anyway.
func computeGuaranteedBefore(wf *reliantv1.Workflow) map[string]map[string]bool {
	nodes := wf.GetNodes()
	result := make(map[string]map[string]bool, len(nodes))
	if len(nodes) == 0 {
		return result
	}

	nodeByID := make(map[string]*reliantv1.Node, len(nodes))
	for _, node := range nodes {
		nodeByID[node.GetId()] = node
	}

	// Build predecessor map from edges + router candidate dispatch.
	preds := make(map[string][]string, len(nodes))
	addPred := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		if _, known := nodeByID[from]; !known {
			return
		}
		if _, known := nodeByID[to]; !known {
			return
		}
		preds[to] = append(preds[to], from)
	}
	for _, edge := range wf.GetEdges() {
		for _, c := range edge.GetCases() {
			for _, to := range c.GetTo() {
				addPred(edge.GetFrom(), to)
			}
		}
		for _, to := range edge.GetDefault() {
			addPred(edge.GetFrom(), to)
		}
	}
	for _, node := range nodes {
		if node.GetType() != model.NodeTypeRouter {
			continue
		}
		if args := node.GetRouter(); args != nil {
			for _, candidate := range args.GetNodes() {
				addPred(node.GetId(), candidate.GetId())
			}
		}
	}

	entrySet := make(map[string]bool, len(wf.GetEntry()))
	for _, entryID := range wf.GetEntry() {
		entrySet[entryID] = true
	}

	// Bounded fixpoint in dependency order: a node is computable once all its
	// predecessors are computed. The graph is a DAG (cycles are structural
	// errors caught earlier), so this converges in <= len(nodes) rounds.
	for round := 0; round <= len(nodes); round++ {
		progressed := false
		for _, node := range nodes {
			id := node.GetId()
			if _, done := result[id]; done {
				continue
			}

			// Entry nodes execute at workflow start: nothing guaranteed before
			// them, even if they also have inbound edges.
			if entrySet[id] {
				result[id] = map[string]bool{}
				progressed = true
				continue
			}

			nodePreds := preds[id]
			if len(nodePreds) == 0 {
				// Unreachable node (structural error elsewhere) — nothing guaranteed.
				result[id] = map[string]bool{}
				progressed = true
				continue
			}

			ready := true
			for _, p := range nodePreds {
				if _, done := result[p]; !done {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}

			contributions := make([]map[string]bool, 0, len(nodePreds))
			for _, p := range nodePreds {
				contribution := make(map[string]bool, len(result[p])+1)
				for k := range result[p] {
					contribution[k] = true
				}
				contribution[p] = true
				contributions = append(contributions, contribution)
			}

			if isAllJoin(nodeByID[id]) {
				result[id] = unionSets(contributions)
			} else {
				result[id] = intersectSets(contributions)
			}
			progressed = true
		}
		if !progressed {
			break
		}
	}

	// Any node left uncomputed (cycle — should be unreachable here) gets the
	// conservative empty set so validation degrades to warnings, not panics.
	for _, node := range nodes {
		if _, done := result[node.GetId()]; !done {
			result[node.GetId()] = map[string]bool{}
		}
	}

	return result
}

// isAllJoin reports whether the node is a join that waits for ALL sources.
// Join condition "all" is the default; only "any" changes the semantics.
func isAllJoin(node *reliantv1.Node) bool {
	if node == nil || node.GetType() != model.NodeTypeJoin {
		return false
	}
	return strings.TrimSpace(strings.ToLower(model.ConditionExpr(node))) != "any"
}

func unionSets(sets []map[string]bool) map[string]bool {
	result := make(map[string]bool)
	for _, s := range sets {
		for k := range s {
			result[k] = true
		}
	}
	return result
}

func intersectSets(sets []map[string]bool) map[string]bool {
	if len(sets) == 0 {
		return map[string]bool{}
	}
	result := make(map[string]bool, len(sets[0]))
	for k := range sets[0] {
		inAll := true
		for _, s := range sets[1:] {
			if !s[k] {
				inAll = false
				break
			}
		}
		if inAll {
			result[k] = true
		}
	}
	return result
}

// warnNodeOrderingCompiled checks a single CEL expression for unguarded
// nodes.<id> references to nodes that are not guaranteed to have executed
// when the expression is evaluated.
//
// Guarded accesses — has(nodes.X...), null comparisons, optional chaining —
// are exempt (detectConditionalNodeAccess machinery). Nodes with their own
// condition are exempt here: the conditional-access warning already covers
// them, and their output KEY is present even when skipped.
//
// Severity:
//   - ERROR when the referenced node provably runs AFTER the current node
//     (currentNode ∈ G(referenced)) — the reference can never be satisfied.
//   - WARNING otherwise — the node exists but is not on every path from the
//     workflow entry to this node (router dispatch, parallel branch).
func warnNodeOrderingCompiled(expr string, path []string, scope *nodeOrderScope, typeCtx *WorkflowTypeContext, result *Result) {
	if scope == nil || typeCtx == nil || typeCtx.GuaranteedBefore == nil {
		return
	}
	guaranteed, ok := typeCtx.GuaranteedBefore[scope.nodeID]
	if !ok {
		return
	}

	// Build the set of nodes that are NOT safe to reference from this scope.
	unsafeSet := make(map[string]bool)
	for nodeID := range typeCtx.NodeTypes {
		if nodeID == scope.nodeID {
			// Self-references: config fields evaluate before the node runs and
			// are covered by other validations; save_message uses output.*.
			continue
		}
		if guaranteed[nodeID] {
			continue
		}
		if _, isConditional := typeCtx.ConditionalNodes[nodeID]; isConditional {
			// Covered by the conditional-access warning; the skip output keeps
			// the nodes.<id> key present even when the condition is false.
			continue
		}
		unsafeSet[nodeID] = true
	}
	if len(unsafeSet) == 0 {
		return
	}

	// Parse with a dyn environment so the original nodes.X.field syntax is
	// preserved for AST-based access detection (same approach as
	// warnConditionalNodeAccessCompiled).
	env, err := cel.NewEnv(
		cel.Variable(string(wfcel.CELNodes), cel.DynType),
		cel.Variable(string(wfcel.CELInputs), cel.DynType),
		cel.Variable(string(wfcel.CELWorkflow), cel.DynType),
		cel.Variable(string(wfcel.CELOutput), cel.DynType),
		cel.Variable(string(wfcel.CELOutputs), cel.DynType),
		cel.Variable(string(wfcel.CELIter), cel.DynType),
	)
	if err != nil {
		return
	}
	compiledAst, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return
	}

	for _, access := range detectConditionalNodeAccess(compiledAst, unsafeSet) {
		refID := access.NodeID
		if refGuaranteed, ok := typeCtx.GuaranteedBefore[refID]; ok && refGuaranteed[scope.nodeID] {
			// The referenced node always runs after the current node.
			result.Add(&Error{
				Severity: SeverityError,
				Category: CategoryNodeOrdering,
				Path:     path,
				Message: fmt.Sprintf(
					"references '%s', but node '%s' always executes AFTER node '%s' — nodes.%s can never be populated when this expression is evaluated",
					access.Path, refID, scope.nodeID, refID,
				),
				Suggestion: fmt.Sprintf("reference a node that runs before '%s', or restructure the edges", scope.nodeID),
			})
			continue
		}

		suggestion := fmt.Sprintf(
			"guard the access: {{has(nodes.%s) ? %s : '<fallback>'}}, or restructure edges so '%s' always runs before '%s'",
			refID, access.Path, refID, scope.nodeID,
		)
		if access.Path != "nodes."+refID {
			suggestion = fmt.Sprintf(
				"guard the access: {{has(nodes.%s) && has(%s) ? %s : '<fallback>'}}, or restructure edges so '%s' always runs before '%s'",
				refID, access.Path, access.Path, refID, scope.nodeID,
			)
		}
		result.Add(&Error{
			Severity: SeverityWarning,
			Category: CategoryNodeOrdering,
			Path:     path,
			Message: fmt.Sprintf(
				"references '%s', but node '%s' is not guaranteed to have executed before node '%s' (it is not on every path from the workflow entry — e.g. a router dispatch or parallel branch can skip it); at runtime this fails with \"no such key: %s\" when the node has not run",
				access.Path, refID, scope.nodeID, refID,
			),
			Suggestion: suggestion,
		})
	}
}
