package wfyaml

import (
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// concurrencyWaiting maps the YAML spelling of concurrency.waiting.
var concurrencyWaiting = map[string]reliantv1.WorkflowConcurrencyWaiting{
	"fifo":   reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_FIFO,
	"latest": reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST,
}

func unmarshalWorkflowConcurrency(node *yaml.Node) (*reliantv1.WorkflowConcurrency, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("concurrency must be a mapping with max_running")
	}
	policy := &reliantv1.WorkflowConcurrency{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, value := node.Content[i].Value, node.Content[i+1]
		if seen[name] {
			return nil, fmt.Errorf("concurrency.%s is set twice", name)
		}
		seen[name] = true
		switch name {
		case "max_running":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return nil, fmt.Errorf("concurrency.max_running must be a positive integer")
			}
			n, err := strconv.ParseUint(value.Value, 10, 32)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("concurrency.max_running must be between 1 and 4294967295")
			}
			policy.MaxRunning = uint32(n)
		case "waiting":
			waiting, ok := concurrencyWaiting[value.Value]
			if value.Kind != yaml.ScalarNode || !ok {
				return nil, fmt.Errorf("concurrency.waiting must be fifo or latest")
			}
			policy.Waiting = waiting
		case "key":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return nil, fmt.Errorf("concurrency.key must be a non-empty template string")
			}
			policy.Key = value.Value
		default:
			return nil, fmt.Errorf("unknown concurrency field %q (max_running, waiting, key)", name)
		}
	}
	if !seen["max_running"] {
		return nil, fmt.Errorf("concurrency requires max_running")
	}
	if policy.Key != "" && policy.Waiting != reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST {
		return nil, fmt.Errorf("concurrency.key needs waiting: latest (it groups the waiting firings that replace each other)")
	}
	return policy, nil
}

// marshalWorkflowConcurrency is the inverse of unmarshalWorkflowConcurrency.
// waiting is emitted only when it was set.
func marshalWorkflowConcurrency(policy *reliantv1.WorkflowConcurrency) (*yaml.Node, error) {
	if policy.MaxRunning == 0 {
		return nil, fmt.Errorf("concurrency.max_running must be positive")
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	node.Content = append(node.Content, scalarNode("max_running", ""), scalarNode(strconv.FormatUint(uint64(policy.MaxRunning), 10), "!!int"))
	for spelling, waiting := range concurrencyWaiting {
		if policy.Waiting == waiting {
			node.Content = append(node.Content, scalarNode("waiting", ""), scalarNode(spelling, ""))
		}
	}
	if policy.Key != "" {
		node.Content = append(node.Content, scalarNode("key", ""), scalarNode(policy.Key, "!!str"))
	}
	return node, nil
}
