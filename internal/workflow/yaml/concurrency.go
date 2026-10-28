package wfyaml

import (
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

func unmarshalWorkflowConcurrency(node *yaml.Node) (*reliantv1.WorkflowConcurrency, error) {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 || node.Content[0].Value != "max_running" {
		return nil, fmt.Errorf("concurrency requires exactly max_running")
	}
	value := node.Content[1]
	if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
		return nil, fmt.Errorf("concurrency.max_running must be a positive integer")
	}
	n, err := strconv.ParseUint(value.Value, 10, 32)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("concurrency.max_running must be between 1 and 4294967295")
	}
	return &reliantv1.WorkflowConcurrency{MaxRunning: uint32(n)}, nil
}
