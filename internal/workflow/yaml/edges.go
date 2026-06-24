package wfyaml

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Unmarshal V2Edge
// ---------------------------------------------------------------------------

func unmarshalEdge(node *yaml.Node) (*reliantv1.Edge, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("edge: expected mapping, got kind %v", node.Kind)
	}
	if err := validateEdgeKeys(node); err != nil {
		return nil, err
	}

	edge := &reliantv1.Edge{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case yamlKeyFrom:
			edge.From = val.Value
		case yamlKeyCases:
			cases, err := unmarshalEdgeCases(val)
			if err != nil {
				return nil, fmt.Errorf("edge.%s: %w", yamlKeyCases, err)
			}
			edge.Cases = cases
		case yamlKeyDefault:
			defaults, err := unmarshalStringOrStringSlice(val)
			if err != nil {
				return nil, fmt.Errorf("edge.%s: %w", yamlKeyDefault, err)
			}
			edge.Default = defaults
		case yamlKeyTo:
			to, err := unmarshalStringOrStringSlice(val)
			if err != nil {
				return nil, fmt.Errorf("edge.%s: %w", yamlKeyTo, err)
			}
			edge.Default = to
		}
	}
	return edge, nil
}

func unmarshalEdgeCases(node *yaml.Node) ([]*reliantv1.EdgeCase, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence for edge cases")
	}
	var cases []*reliantv1.EdgeCase
	for _, item := range node.Content {
		ec, err := unmarshalEdgeCase(item)
		if err != nil {
			return nil, err
		}
		cases = append(cases, ec)
	}
	return cases, nil
}

func unmarshalEdgeCase(node *yaml.Node) (*reliantv1.EdgeCase, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for edge case")
	}
	if err := validateEdgeCaseKeys(node); err != nil {
		return nil, err
	}
	ec := &reliantv1.EdgeCase{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case yamlKeyTo:
			to, err := unmarshalStringOrStringSlice(val)
			if err != nil {
				return nil, fmt.Errorf("edge_case.to: %w", err)
			}
			ec.To = to
		case yamlKeyCondition:
			ec.Condition = val.Value
		case yamlKeyLabel:
			ec.Label = val.Value
		}
	}
	return ec, nil
}

// unmarshalStringOrStringSlice handles both string and []string YAML values.
func validateEdgeKeys(node *yaml.Node) error {
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key == yamlKeyTo {
			continue
		}
		if !generatedHasEdgeFieldKey(key) {
			return fmt.Errorf("edge: unknown key %q", key)
		}
	}
	return nil
}

func validateEdgeCaseKeys(node *yaml.Node) error {
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !generatedHasEdgeCaseFieldKey(key) {
			return fmt.Errorf("edge_case: unknown key %q", key)
		}
	}
	return nil
}

func unmarshalStringOrStringSlice(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		var strs []string
		if err := node.Decode(&strs); err != nil {
			return nil, err
		}
		return strs, nil
	default:
		return nil, fmt.Errorf("expected string or []string, got kind %v", node.Kind)
	}
}

// ---------------------------------------------------------------------------
// Marshal V2Edge
// ---------------------------------------------------------------------------

func marshalEdge(edge *reliantv1.Edge) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}

	// from
	m.Content = append(m.Content, scalarNode(yamlKeyFrom, ""), scalarNode(edge.From, ""))

	// cases
	if len(edge.Cases) > 0 {
		casesNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, ec := range edge.Cases {
			ecNode, err := marshalEdgeCase(ec)
			if err != nil {
				return nil, err
			}
			casesNode.Content = append(casesNode.Content, ecNode)
		}
		m.Content = append(m.Content, scalarNode(yamlKeyCases, ""), casesNode)
	}

	// default: single string or array
	if len(edge.Default) > 0 {
		n := marshalStringOrArray(edge.Default)
		m.Content = append(m.Content, scalarNode(yamlKeyDefault, ""), n)
	}

	return m, nil
}

func marshalEdgeCase(ec *reliantv1.EdgeCase) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if len(ec.To) > 0 {
		n := marshalStringOrArray(ec.To)
		m.Content = append(m.Content, scalarNode(yamlKeyTo, ""), n)
	}
	if ec.Condition != "" {
		m.Content = append(m.Content, scalarNode(yamlKeyCondition, ""), scalarNode(ec.Condition, ""))
	}
	if ec.Label != "" {
		m.Content = append(m.Content, scalarNode(yamlKeyLabel, ""), scalarNode(ec.Label, ""))
	}
	return m, nil
}

// marshalStringOrArray marshals a []string as a single string if len==1, array otherwise.
func marshalStringOrArray(strs []string) *yaml.Node {
	if len(strs) == 1 {
		return scalarNode(strs[0], "")
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, s := range strs {
		seq.Content = append(seq.Content, scalarNode(s, ""))
	}
	return seq
}
