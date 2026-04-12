package runtime

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// buildNodeRoutingSystemPrompt constructs the system prompt for node routing.
func buildNodeRoutingSystemPrompt(candidates []*reliantv1.NodeRouterCandidate, customSystemPrompt string) string {
	var sb strings.Builder

	if customSystemPrompt != "" {
		sb.WriteString(customSystemPrompt)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString(defaultNodeRoutingSystemPrompt)
		sb.WriteString("\n\n")
	}

	sb.WriteString("# Available Nodes\n\n")

	// Sort candidates by ID for deterministic output
	sorted := make([]*reliantv1.NodeRouterCandidate, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].GetId() < sorted[j].GetId()
	})

	for i, c := range sorted {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "- **`%s`**", c.GetId())
		if desc := c.GetDescription(); desc != "" {
			fmt.Fprintf(&sb, ": %s", desc)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// buildNodeRoutingResponseSchema returns the JSON schema for the node routing decision.
func buildNodeRoutingResponseSchema(candidates []*reliantv1.NodeRouterCandidate) map[string]interface{} {
	nodeIDs := make([]interface{}, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		id := c.GetId()
		if !seen[id] {
			seen[id] = true
			nodeIDs = append(nodeIDs, id)
		}
	}
	// Sort for determinism
	sort.Slice(nodeIDs, func(i, j int) bool {
		return nodeIDs[i].(string) < nodeIDs[j].(string)
	})

	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"selected_node": map[string]interface{}{
				"type":        "string",
				"enum":        nodeIDs,
				"description": "The ID of the node to route to",
			},
			"reasoning": map[string]interface{}{
				"type":        "string",
				"description": "Brief explanation of why this node was selected",
			},
		},
		"required": []interface{}{"selected_node", "reasoning"},
	}
}

const defaultNodeRoutingSystemPrompt = `You are a node router. Your job is to analyze the user's request and select the most appropriate node to handle it.

Pick the node that best matches the user's intent.`
