package wfyaml

import (
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// unmarshalWorkflowConcurrency reads `concurrency:`: max_running (a fixed
// cap), per_member (K runs per member of the trigger's machine pool), or both
// (max_running then caps the scaled value). At least one is required.
func unmarshalWorkflowConcurrency(node *yaml.Node) (*reliantv1.WorkflowConcurrency, error) {
	if node.Kind != yaml.MappingNode || len(node.Content) == 0 {
		return nil, fmt.Errorf("concurrency requires max_running, per_member, or both")
	}
	out := &reliantv1.WorkflowConcurrency{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if key != "max_running" && key != "per_member" {
			return nil, fmt.Errorf("concurrency: unknown key %q (want max_running or per_member)", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("concurrency.%s is set twice", key)
		}
		seen[key] = true
		if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
			return nil, fmt.Errorf("concurrency.%s must be a positive integer", key)
		}
		n, err := strconv.ParseUint(value.Value, 10, 32)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("concurrency.%s must be between 1 and 4294967295", key)
		}
		if key == "max_running" {
			out.MaxRunning = uint32(n)
		} else {
			out.PerMember = uint32(n)
		}
	}
	return out, nil
}

// marshalWorkflowConcurrency is the inverse, emitting only the keys set.
func marshalWorkflowConcurrency(c *reliantv1.WorkflowConcurrency) (*yaml.Node, error) {
	if c.GetMaxRunning() == 0 && c.GetPerMember() == 0 {
		return nil, fmt.Errorf("concurrency requires max_running, per_member, or both")
	}
	policy := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if c.GetMaxRunning() > 0 {
		policy.Content = append(policy.Content, scalarNode("max_running", ""), scalarNode(strconv.FormatUint(uint64(c.GetMaxRunning()), 10), "!!int"))
	}
	if c.GetPerMember() > 0 {
		policy.Content = append(policy.Content, scalarNode("per_member", ""), scalarNode(strconv.FormatUint(uint64(c.GetPerMember()), 10), "!!int"))
	}
	return policy, nil
}
