// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/attention"
	"gopkg.in/yaml.v3"
)

func unmarshalWorkflowAttention(node *yaml.Node) (*reliantv1.WorkflowAttention, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("attention must be a mapping")
	}
	policy := &reliantv1.WorkflowAttention{}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, fmt.Errorf("attention field names must be strings")
		}
		name := key.Value
		if seen[name] {
			return nil, fmt.Errorf("attention.%s is set twice", name)
		}
		seen[name] = true
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
			return nil, fmt.Errorf("attention.%s must be a nonempty string", name)
		}
		switch name {
		case "key":
			policy.Key = value.Value
		case "on_start":
			policy.OnStart = value.Value
		case "recovery_timeout":
			policy.RecoveryTimeout = value.Value
		case "on_success":
			policy.OnSuccess = value.Value
		case "on_failure":
			policy.OnFailure = value.Value
		default:
			return nil, unknownField(name, "unknown attention field %q (key, on_start, recovery_timeout, on_success, on_failure)", name)
		}
	}
	if err := attention.Validate(policy); err != nil {
		return nil, err
	}
	return policy, nil
}

func marshalWorkflowAttention(policy *reliantv1.WorkflowAttention) (*yaml.Node, error) {
	if err := attention.Validate(policy); err != nil {
		return nil, err
	}
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, field := range []struct{ name, value string }{
		{"key", policy.GetKey()},
		{"on_start", policy.GetOnStart()},
		{"recovery_timeout", policy.GetRecoveryTimeout()},
		{"on_success", policy.GetOnSuccess()},
		{"on_failure", policy.GetOnFailure()},
	} {
		if field.value != "" {
			node.Content = append(node.Content, scalarNode(field.name, "!!str"), scalarNode(field.value, "!!str"))
		}
	}
	return node, nil
}
