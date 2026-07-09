package wfyaml

// Syntactic sugar: sequence: and parallel: blocks
//
// These are purely YAML-level transformations that desugar into standard
// nodes, edges, and entry fields. They produce the same proto representation
// as hand-written nodes+edges.
//
// ## sequence:
//
// A top-level `sequence:` field replaces the combination of entry:, nodes:,
// and edges: for linear chains of nodes. It parses a list of node definitions
// and generates:
//   - entry: [first_node_id]
//   - Sequential default edges between each pair of adjacent nodes
//   - All nodes are added to the workflow's nodes list
//
// sequence: is syntactic sugar. This:
//
//	sequence:
//	  - id: research
//	    type: workflow
//	    ref: builtin://agent
//	  - id: implement
//	    type: workflow
//	    ref: builtin://agent
//	  - id: review
//	    type: workflow
//	    ref: builtin://structured-agent
//
// desugars to:
//
//	entry: [research]
//	nodes:
//	  - id: research
//	    ...
//	  - id: implement
//	    ...
//	  - id: review
//	    ...
//	edges:
//	  - from: research
//	    default: implement
//	  - from: implement
//	    default: review
//
// sequence: can coexist with additional nodes: and edges: for mixed patterns
// where the main flow is linear but has branches off the end.
// sequence: cannot coexist with entry: (it implies entry).
//
// ## type: parallel (node type sugar)
//
// A node with `type: parallel` and `branches:` desugars into multiple nodes
// plus fork and join edges. It creates an implicit join node and fan-out
// edges from the trigger to each branch, plus edges from each branch to the
// join.
//
// This node in the nodes list:
//
//	- id: explore
//	  type: parallel
//	  branches:
//	    - id: research
//	      type: workflow
//	      ref: builtin://agent
//	    - id: design
//	      type: workflow
//	      ref: builtin://agent
//
// desugars to these nodes:
//
//	- id: research
//	  type: workflow
//	  ref: builtin://agent
//	- id: design
//	  type: workflow
//	  ref: builtin://agent
//	- id: explore
//	  type: join
//	  condition: all
//
// and these edges (injected from whatever triggers `explore`):
//
//	# The edge targeting "explore" becomes edges targeting each branch
//	# Plus edges from each branch to the join node (which keeps the id "explore")
//	- from: research
//	    default: explore
//	- from: design
//	    default: explore
//
// The parallel node's id becomes the join node's id, so downstream edges
// referencing `explore` still work. The branch fan-out is handled by
// rewriting incoming edges to target each branch instead of the parallel node.

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"gopkg.in/yaml.v3"
)

// desugarSequence processes a top-level `sequence:` YAML node into nodes, edges, and entry.
// The sequence node must be a YAML sequence (list) of node definitions.
// Returns the generated nodes, edges, and entry point.
func desugarSequence(seqNode *yaml.Node) ([]*reliantv1.Node, []*reliantv1.Edge, []string, error) {
	if seqNode.Kind != yaml.SequenceNode {
		return nil, nil, nil, fmt.Errorf("sequence: expected list, got kind %v", seqNode.Kind)
	}
	if len(seqNode.Content) == 0 {
		return nil, nil, nil, fmt.Errorf("sequence: must contain at least one node")
	}

	// Each element in the sequence becomes one "logical" node. However, a
	// type: parallel node expands into multiple real nodes (branches + join).
	// We track the logical node IDs for sequential edge generation, then
	// flatten the real nodes into the output list.
	type seqEntry struct {
		logicalID string // the ID used in sequential edges
		nodes     []*reliantv1.Node
		branchIDs []string // non-nil only for parallel nodes
	}

	var entries []seqEntry
	for _, item := range seqNode.Content {
		// Check for parallel sugar first
		expanded, branchIDs, isParallel, err := desugarParallelNode(item)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("sequence: %w", err)
		}
		if isParallel {
			// The join node is the last expanded node and keeps the parallel ID
			joinID := expanded[len(expanded)-1].GetId()
			entries = append(entries, seqEntry{
				logicalID: joinID,
				nodes:     expanded,
				branchIDs: branchIDs,
			})
		} else {
			node, err := unmarshalNode(item)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("sequence: %w", err)
			}
			entries = append(entries, seqEntry{
				logicalID: node.GetId(),
				nodes:     []*reliantv1.Node{node},
			})
		}
	}

	// Generate entry from first logical node
	// If the first node is parallel, entry should be all branch IDs
	var entry []string
	if entries[0].branchIDs != nil {
		entry = entries[0].branchIDs
	} else {
		entry = []string{entries[0].logicalID}
	}

	// Flatten all nodes
	var allNodes []*reliantv1.Node
	for _, e := range entries {
		allNodes = append(allNodes, e.nodes...)
	}

	// Generate sequential edges between adjacent logical nodes.
	// For parallel nodes, we also generate branch→join edges and
	// rewrite the incoming edge to fan-out to all branches.
	var edges []*reliantv1.Edge
	for i := 0; i < len(entries)-1; i++ {
		curr := entries[i]
		next := entries[i+1]

		// Target of the edge: if next is parallel, fan-out to its branches;
		// otherwise target the next logical node.
		var targets []string
		if next.branchIDs != nil {
			targets = next.branchIDs
		} else {
			targets = []string{next.logicalID}
		}

		edges = append(edges, &reliantv1.Edge{
			From:    curr.logicalID,
			Default: targets,
		})
	}

	// Generate branch→join edges for any parallel nodes
	for _, e := range entries {
		if e.branchIDs != nil {
			for _, branchID := range e.branchIDs {
				edges = append(edges, &reliantv1.Edge{
					From:    branchID,
					Default: []string{e.logicalID},
				})
			}
		}
	}

	return allNodes, edges, entry, nil
}

// desugarParallelNode checks if a node YAML mapping has type: parallel and branches:.
// If so, it returns the expanded nodes (branches + join) and the branch IDs for edge rewriting.
// If the node is not a parallel sugar node, returns nil, nil, false, nil.
func desugarParallelNode(nodeYAML *yaml.Node) (expandedNodes []*reliantv1.Node, branchIDs []string, isParallel bool, err error) {
	if nodeYAML.Kind != yaml.MappingNode {
		return nil, nil, false, nil
	}

	// Check if this is a parallel node by scanning for type: parallel
	var nodeID string
	var branchesNode *yaml.Node
	isParallelType := false

	for i := 0; i < len(nodeYAML.Content); i += 2 {
		key := nodeYAML.Content[i].Value
		val := nodeYAML.Content[i+1]
		switch key {
		case "id":
			nodeID = val.Value
		case "type":
			if val.Value == "parallel" {
				isParallelType = true
			}
		case "branches":
			branchesNode = val
		}
	}

	if !isParallelType {
		return nil, nil, false, nil
	}

	if nodeID == "" {
		return nil, nil, true, fmt.Errorf("parallel node must have an id")
	}
	if branchesNode == nil || branchesNode.Kind != yaml.SequenceNode {
		return nil, nil, true, fmt.Errorf("parallel node %q must have branches: (a list of nodes)", nodeID)
	}
	if len(branchesNode.Content) == 0 {
		return nil, nil, true, fmt.Errorf("parallel node %q: branches must contain at least one node", nodeID)
	}

	// Parse each branch as a regular node
	var branches []*reliantv1.Node
	for _, item := range branchesNode.Content {
		node, err := unmarshalNode(item)
		if err != nil {
			return nil, nil, true, fmt.Errorf("parallel node %q branches: %w", nodeID, err)
		}
		branches = append(branches, node)
		branchIDs = append(branchIDs, node.GetId())
	}

	// Create the join node with the parallel node's ID.
	// This means downstream edges referencing the parallel ID still work.
	// The join condition "all" is set via Node.Condition (the base condition field).
	joinNode := &reliantv1.Node{
		Id:        nodeID,
		Type:      model.NodeTypeJoin,
		Condition: &reliantv1.DirectCelBool{Expr: "all"},
		Args: &reliantv1.Node_Join{
			Join: &reliantv1.JoinArgs{},
		},
	}

	// Return branches + join node
	expandedNodes = append(branches, joinNode)
	return expandedNodes, branchIDs, true, nil
}

// expandParallelNodes processes the full nodes list, expanding any type: parallel
// nodes into their desugared form. Also generates the branch→join edges.
// Returns the expanded nodes list and any generated edges.
func expandParallelNodes(nodesYAML *yaml.Node) ([]*reliantv1.Node, []*reliantv1.Edge, map[string][]string, error) {
	if nodesYAML.Kind != yaml.SequenceNode {
		return nil, nil, nil, fmt.Errorf("nodes: expected list")
	}

	var allNodes []*reliantv1.Node
	var generatedEdges []*reliantv1.Edge
	// parallelFanout maps parallel node ID → branch IDs for edge rewriting
	parallelFanout := make(map[string][]string)

	for _, item := range nodesYAML.Content {
		expanded, branchIDs, isParallel, err := desugarParallelNode(item)
		if err != nil {
			return nil, nil, nil, err
		}

		if isParallel {
			allNodes = append(allNodes, expanded...)
			// The join node has the parallel node's original ID (last in expanded)
			joinID := expanded[len(expanded)-1].GetId()
			parallelFanout[joinID] = branchIDs

			// Generate edges from each branch to the join
			for _, branchID := range branchIDs {
				generatedEdges = append(generatedEdges, &reliantv1.Edge{
					From:    branchID,
					Default: []string{joinID},
				})
			}
		} else {
			// Regular node — parse normally
			node, err := unmarshalNode(item)
			if err != nil {
				return nil, nil, nil, err
			}
			allNodes = append(allNodes, node)
		}
	}

	return allNodes, generatedEdges, parallelFanout, nil
}

// rewriteEdgesForParallel rewrites edges that target parallel node IDs.
// Any edge with `default: [parallel_id]` gets rewritten to target all branches.
// Any edge with a case `to: parallel_id` gets rewritten to target all branches.
func rewriteEdgesForParallel(edges []*reliantv1.Edge, parallelFanout map[string][]string) []*reliantv1.Edge {
	if len(parallelFanout) == 0 {
		return edges
	}

	var result []*reliantv1.Edge
	for _, edge := range edges {
		rewritten := rewriteEdge(edge, parallelFanout)
		result = append(result, rewritten...)
	}
	return result
}

// rewriteEdge rewrites a single edge, potentially expanding it into multiple edges
// if it targets a parallel node.
func rewriteEdge(edge *reliantv1.Edge, parallelFanout map[string][]string) []*reliantv1.Edge {
	// Check if any default targets are parallel nodes
	var newDefaults []string
	for _, target := range edge.Default {
		if branches, ok := parallelFanout[target]; ok {
			newDefaults = append(newDefaults, branches...)
		} else {
			newDefaults = append(newDefaults, target)
		}
	}

	// Check if any case targets are parallel nodes.
	// EdgeCase.To is []string — expand any parallel targets to branches.
	var newCases []*reliantv1.EdgeCase
	for _, ec := range edge.Cases {
		var expandedTo []string
		for _, target := range ec.To {
			if branches, ok := parallelFanout[target]; ok {
				expandedTo = append(expandedTo, branches...)
			} else {
				expandedTo = append(expandedTo, target)
			}
		}
		newCases = append(newCases, &reliantv1.EdgeCase{
			To:        expandedTo,
			Condition: ec.Condition,
			Label:     ec.Label,
		})
	}

	return []*reliantv1.Edge{{
		From:    edge.From,
		Default: newDefaults,
		Cases:   newCases,
	}}
}

// rewriteEntryForParallel rewrites entry points that reference parallel nodes.
func rewriteEntryForParallel(entry []string, parallelFanout map[string][]string) []string {
	if len(parallelFanout) == 0 {
		return entry
	}

	var result []string
	for _, e := range entry {
		if branches, ok := parallelFanout[e]; ok {
			result = append(result, branches...)
		} else {
			result = append(result, e)
		}
	}
	return result
}
