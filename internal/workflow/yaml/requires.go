package wfyaml

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// unmarshalWorkflowRequirements reads `requires:`:
//
//	requires:
//	  tools: [git, gh, task]
//	  integrations: [github]
//	  repos: [control-plane, reliant]
//
// Each list is plain names (no templates: a requirement is checked before
// anything is evaluated), unique within its list.
func unmarshalWorkflowRequirements(node *yaml.Node) (*reliantv1.WorkflowRequirements, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("requires must be a mapping of tools, integrations and repos")
	}
	req := &reliantv1.WorkflowRequirements{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		list, err := requirementList(key, val)
		if err != nil {
			return nil, err
		}
		switch key {
		case "tools":
			req.Tools = list
		case "integrations":
			req.Integrations = list
		case "repos":
			req.Repos = list
		default:
			return nil, unknownField(key, "unknown requires field: %q (tools, integrations, repos)", key)
		}
	}
	return req, nil
}

func requirementList(key string, val *yaml.Node) ([]string, error) {
	if val.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("requires.%s must be a list", key)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(val.Content))
	for _, item := range val.Content {
		v := strings.TrimSpace(item.Value)
		switch {
		case item.Kind != yaml.ScalarNode || v == "":
			return nil, fmt.Errorf("requires.%s: every entry must be a non-empty name", key)
		case strings.Contains(v, "{{"):
			return nil, fmt.Errorf("requires.%s: %q is a template; requirements are checked before anything is evaluated, so name them literally", key, v)
		case seen[v]:
			return nil, fmt.Errorf("requires.%s: %q is listed twice", key, v)
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

func marshalWorkflowRequirements(req *reliantv1.WorkflowRequirements) *yaml.Node {
	if req == nil || (len(req.GetTools()) == 0 && len(req.GetIntegrations()) == 0 && len(req.GetRepos()) == 0) {
		return nil
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	add := func(key string, values []string) {
		if len(values) == 0 {
			return
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, v := range values {
			seq.Content = append(seq.Content, scalarNode(v, ""))
		}
		m.Content = append(m.Content, scalarNode(key, ""), seq)
	}
	add("tools", req.GetTools())
	add("integrations", req.GetIntegrations())
	add("repos", req.GetRepos())
	return m
}
