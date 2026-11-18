package wfyaml

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// unmarshalWorkflowCheck reads `check:`, the GitHub check run the engine owns
// for each run a trigger fires (reliantv1.WorkflowCheck):
//
//	check: reliant/ci                  # the name alone
//
//	check:
//	  name: reliant/ci
//	  repository: '{{ trigger.payload.attributes.repository }}'   # the default
//	  head_sha: '{{ trigger.payload.data.check_suite.head_sha }}'  # default: the event's head
//	  output: '{{ nodes.report.check }}'                           # {title, summary, text?, annotations?}
//	  timeout: 2h                                                   # default 6h
//
// Only the shape is checked here; what each field may say is validation's
// (validation.validateCheck), so a proto or builder caller meets the same
// rules.
func unmarshalWorkflowCheck(node *yaml.Node) (*reliantv1.WorkflowCheck, error) {
	if node.Kind == yaml.ScalarNode {
		name := strings.TrimSpace(node.Value)
		if name == "" {
			return nil, fmt.Errorf("check needs a name: check: <name>, or a mapping with name")
		}
		return &reliantv1.WorkflowCheck{Name: name}, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("check must be the check's name or a mapping of name, repository, head_sha, output and timeout")
	}
	check := &reliantv1.WorkflowCheck{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if seen[key] {
			return nil, fmt.Errorf("check.%s is set twice", key)
		}
		seen[key] = true
		if val.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("check.%s must be a string", key)
		}
		switch key {
		case "name":
			check.Name = strings.TrimSpace(val.Value)
		case "repository":
			check.Repository = val.Value
		case "head_sha":
			check.HeadSha = val.Value
		case "output":
			check.Output = val.Value
		case "timeout":
			check.Timeout = strings.TrimSpace(val.Value)
		default:
			return nil, unknownField(key, "unknown check field: %q (name, repository, head_sha, output, timeout)", key)
		}
	}
	if check.Name == "" {
		return nil, fmt.Errorf("check needs a name: the check's name on GitHub, e.g. reliant/ci")
	}
	return check, nil
}

// marshalWorkflowCheck is the inverse of unmarshalWorkflowCheck: the name
// alone when nothing else is set.
func marshalWorkflowCheck(check *reliantv1.WorkflowCheck) *yaml.Node {
	if check.GetRepository() == "" && check.GetHeadSha() == "" && check.GetOutput() == "" && check.GetTimeout() == "" {
		return scalarNode(check.GetName(), "!!str")
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	add := func(key, value string) {
		if value != "" {
			node.Content = append(node.Content, scalarNode(key, ""), scalarNode(value, "!!str"))
		}
	}
	add("name", check.GetName())
	add("repository", check.GetRepository())
	add("head_sha", check.GetHeadSha())
	add("output", check.GetOutput())
	add("timeout", check.GetTimeout())
	return node
}
