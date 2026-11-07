// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// unmarshalWorkflowPermissions reads `permissions: [approvals:decide]`: plain
// names, unique. Which names exist is validation's call, so a proto caller
// is held to the same list.
func unmarshalWorkflowPermissions(node *yaml.Node) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("permissions must be a list, e.g. [approvals:decide]")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		v := strings.TrimSpace(item.Value)
		switch {
		case item.Kind != yaml.ScalarNode || v == "":
			return nil, fmt.Errorf("permissions: every entry must be a non-empty name")
		case strings.Contains(v, "{{"):
			return nil, fmt.Errorf("permissions: %q is a template; a permission is declared literally", v)
		case seen[v]:
			return nil, fmt.Errorf("permissions: %q is listed twice", v)
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

func marshalWorkflowPermissions(perms []string) *yaml.Node {
	if len(perms) == 0 {
		return nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, p := range perms {
		seq.Content = append(seq.Content, scalarNode(p, ""))
	}
	return seq
}
