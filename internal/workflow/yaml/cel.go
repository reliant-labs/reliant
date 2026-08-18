// Package v2yaml implements custom YAML marshaling/unmarshaling for the proto-generated
// workflow types in reliantv1. It bridges the user-facing YAML syntax (where CEL
// expressions use {{}} delimiters, nodes have type-dispatched args, and structural
// nodes have inline fields) with the proto-generated Go types that use oneof fields.
package wfyaml

import (
	"fmt"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// isInterpolatedCEL returns true if s contains {{…}} template syntax.
func isInterpolatedCEL(s string) bool {
	return strings.Contains(s, "{{")
}

// ---------------------------------------------------------------------------
// CelString
// ---------------------------------------------------------------------------

func unmarshalCelString(node *yaml.Node) (*reliantv1.CelString, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	cs := &reliantv1.CelString{}
	if node.Kind == yaml.ScalarNode && isInterpolatedCEL(node.Value) {
		cs.Value = &reliantv1.CelString_Expr{Expr: node.Value}
		return cs, nil
	}
	// Decode as literal string
	var s string
	if err := node.Decode(&s); err != nil {
		return nil, fmt.Errorf("CelString: cannot decode: %w", err)
	}
	cs.Value = &reliantv1.CelString_Literal{Literal: s}
	return cs, nil
}

func marshalCelString(cs *reliantv1.CelString) (*yaml.Node, error) {
	if cs == nil {
		return nil, nil
	}
	switch v := cs.Value.(type) {
	case *reliantv1.CelString_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelString_Literal:
		return scalarNode(v.Literal, ""), nil
	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// CelBool
// ---------------------------------------------------------------------------

func unmarshalCelBool(node *yaml.Node) (*reliantv1.CelBool, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	cb := &reliantv1.CelBool{}
	if node.Kind == yaml.ScalarNode && isInterpolatedCEL(node.Value) {
		cb.Value = &reliantv1.CelBool_Expr{Expr: node.Value}
		return cb, nil
	}
	var b bool
	if err := node.Decode(&b); err != nil {
		return nil, fmt.Errorf("CelBool: cannot decode: %w", err)
	}
	cb.Value = &reliantv1.CelBool_Literal{Literal: b}
	return cb, nil
}

func marshalCelBool(cb *reliantv1.CelBool) (*yaml.Node, error) {
	if cb == nil {
		return nil, nil
	}
	switch v := cb.Value.(type) {
	case *reliantv1.CelBool_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelBool_Literal:
		n := &yaml.Node{Kind: yaml.ScalarNode}
		if v.Literal {
			n.Value = "true"
			n.Tag = "!!bool"
		} else {
			n.Value = "false"
			n.Tag = "!!bool"
		}
		return n, nil
	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// CelDouble
// ---------------------------------------------------------------------------

func unmarshalCelDouble(node *yaml.Node) (*reliantv1.CelDouble, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	cd := &reliantv1.CelDouble{}
	if node.Kind == yaml.ScalarNode && isInterpolatedCEL(node.Value) {
		cd.Value = &reliantv1.CelDouble_Expr{Expr: node.Value}
		return cd, nil
	}
	var f float64
	if err := node.Decode(&f); err != nil {
		return nil, fmt.Errorf("CelDouble: cannot decode: %w", err)
	}
	cd.Value = &reliantv1.CelDouble_Literal{Literal: f}
	return cd, nil
}

func marshalCelDouble(cd *reliantv1.CelDouble) (*yaml.Node, error) {
	if cd == nil {
		return nil, nil
	}
	switch v := cd.Value.(type) {
	case *reliantv1.CelDouble_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelDouble_Literal:
		s := strconv.FormatFloat(v.Literal, 'f', -1, 64)
		return &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!float"}, nil
	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// CelInt
// ---------------------------------------------------------------------------

func unmarshalCelInt(node *yaml.Node) (*reliantv1.CelInt, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	ci := &reliantv1.CelInt{}
	if node.Kind == yaml.ScalarNode && isInterpolatedCEL(node.Value) {
		ci.Value = &reliantv1.CelInt_Expr{Expr: node.Value}
		return ci, nil
	}
	var i int64
	if err := node.Decode(&i); err != nil {
		return nil, fmt.Errorf("CelInt: cannot decode: %w", err)
	}
	ci.Value = &reliantv1.CelInt_Literal{Literal: i}
	return ci, nil
}

func marshalCelInt(ci *reliantv1.CelInt) (*yaml.Node, error) {
	if ci == nil {
		return nil, nil
	}
	switch v := ci.Value.(type) {
	case *reliantv1.CelInt_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelInt_Literal:
		s := strconv.FormatInt(v.Literal, 10)
		return &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!int"}, nil
	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// CelStringList
// ---------------------------------------------------------------------------

func unmarshalCelStringList(node *yaml.Node) (*reliantv1.CelStringList, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	csl := &reliantv1.CelStringList{}
	if node.Kind == yaml.ScalarNode && isInterpolatedCEL(node.Value) {
		csl.Value = &reliantv1.CelStringList_Expr{Expr: node.Value}
		return csl, nil
	}
	// Must be a sequence
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("CelStringList: expected sequence or CEL expression, got kind %v", node.Kind)
	}
	var strs []string
	if err := node.Decode(&strs); err != nil {
		return nil, fmt.Errorf("CelStringList: cannot decode: %w", err)
	}
	csl.Value = &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: strs}}
	return csl, nil
}

func marshalCelStringList(csl *reliantv1.CelStringList) (*yaml.Node, error) {
	if csl == nil {
		return nil, nil
	}
	switch v := csl.Value.(type) {
	case *reliantv1.CelStringList_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelStringList_Literal:
		if v.Literal == nil {
			return nil, nil
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, s := range v.Literal.Values {
			seq.Content = append(seq.Content, scalarNode(s, ""))
		}
		return seq, nil
	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// CelModelSelector
// ---------------------------------------------------------------------------

func unmarshalCelModelSelector(node *yaml.Node) (*reliantv1.CelModelSelector, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	cms := &reliantv1.CelModelSelector{}
	if node.Kind == yaml.ScalarNode && isInterpolatedCEL(node.Value) {
		cms.Value = &reliantv1.CelModelSelector_Expr{Expr: node.Value}
		return cms, nil
	}
	// Mapping node → decode as V2ModelSelector literal
	if node.Kind == yaml.MappingNode {
		ms := &reliantv1.ModelSelector{}
		if err := unmarshalModelSelector(node, ms); err != nil {
			return nil, fmt.Errorf("CelModelSelector: %w", err)
		}
		cms.Value = &reliantv1.CelModelSelector_Literal{Literal: ms}
		return cms, nil
	}
	// Scalar string (not CEL) → treat as model ID
	if node.Kind == yaml.ScalarNode {
		ms := &reliantv1.ModelSelector{Id: node.Value}
		cms.Value = &reliantv1.CelModelSelector_Literal{Literal: ms}
		return cms, nil
	}
	return nil, fmt.Errorf("CelModelSelector: unexpected node kind %v", node.Kind)
}

func unmarshalModelSelector(node *yaml.Node, ms *reliantv1.ModelSelector) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping for V2ModelSelector, got kind %v", node.Kind)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case "id":
			ms.Id = val.Value
		case "tags":
			if err := val.Decode(&ms.Tags); err != nil {
				return fmt.Errorf("model.tags: %w", err)
			}
		case "providers":
			if err := val.Decode(&ms.Providers); err != nil {
				return fmt.Errorf("model.providers: %w", err)
			}
		}
	}
	return nil
}

func marshalCelModelSelector(cms *reliantv1.CelModelSelector) (*yaml.Node, error) {
	if cms == nil {
		return nil, nil
	}
	switch v := cms.Value.(type) {
	case *reliantv1.CelModelSelector_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelModelSelector_Literal:
		if v.Literal == nil {
			return nil, nil
		}
		return marshalModelSelector(v.Literal)
	default:
		return nil, nil
	}
}

func marshalModelSelector(ms *reliantv1.ModelSelector) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if ms.Id != "" {
		m.Content = append(m.Content, scalarNode("id", ""), scalarNode(ms.Id, ""))
	}
	if len(ms.Tags) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range ms.Tags {
			seq.Content = append(seq.Content, scalarNode(t, ""))
		}
		m.Content = append(m.Content, scalarNode("tags", ""), seq)
	}
	if len(ms.Providers) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, p := range ms.Providers {
			seq.Content = append(seq.Content, scalarNode(p, ""))
		}
		m.Content = append(m.Content, scalarNode("providers", ""), seq)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// CelDaemonSelector
// ---------------------------------------------------------------------------

func unmarshalCelDaemonSelector(node *yaml.Node) (*reliantv1.CelDaemonSelector, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	cds := &reliantv1.CelDaemonSelector{}
	// Scalar string: either CEL expression or shorthand ("local", "cloud", name, or UUID)
	if node.Kind == yaml.ScalarNode {
		if isInterpolatedCEL(node.Value) {
			cds.Value = &reliantv1.CelDaemonSelector_Expr{Expr: node.Value}
			return cds, nil
		}
		// String shorthand: treat as daemon type for known types, otherwise as name
		ds := &reliantv1.DaemonSelectorProto{}
		switch node.Value {
		case "local", "cloud", "any":
			ds.Type = node.Value
		default:
			// Could be a UUID (id) or a name — use name for user-friendliness
			ds.Name = node.Value
		}
		cds.Value = &reliantv1.CelDaemonSelector_Literal{Literal: ds}
		return cds, nil
	}
	// Mapping node: decode as DaemonSelectorProto
	if node.Kind == yaml.MappingNode {
		ds := &reliantv1.DaemonSelectorProto{}
		if err := unmarshalDaemonSelector(node, ds); err != nil {
			return nil, fmt.Errorf("CelDaemonSelector: %w", err)
		}
		cds.Value = &reliantv1.CelDaemonSelector_Literal{Literal: ds}
		return cds, nil
	}
	return nil, fmt.Errorf("CelDaemonSelector: unexpected node kind %v", node.Kind)
}

func unmarshalDaemonSelector(node *yaml.Node, ds *reliantv1.DaemonSelectorProto) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping for DaemonSelector, got kind %v", node.Kind)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case "id":
			ds.Id = val.Value
		case "name":
			ds.Name = val.Value
		case "type":
			ds.Type = val.Value
		case "labels":
			if val.Kind == yaml.MappingNode {
				ds.Labels = make(map[string]string)
				for j := 0; j < len(val.Content); j += 2 {
					ds.Labels[val.Content[j].Value] = val.Content[j+1].Value
				}
			}
		default:
			return fmt.Errorf("unknown daemon selector field: %q", key)
		}
	}
	return nil
}

func marshalCelDaemonSelector(cds *reliantv1.CelDaemonSelector) (*yaml.Node, error) {
	if cds == nil {
		return nil, nil
	}
	switch v := cds.Value.(type) {
	case *reliantv1.CelDaemonSelector_Expr:
		return scalarNode(v.Expr, ""), nil
	case *reliantv1.CelDaemonSelector_Literal:
		if v.Literal == nil {
			return nil, nil
		}
		return marshalDaemonSelector(v.Literal)
	default:
		return nil, nil
	}
}

func marshalDaemonSelector(ds *reliantv1.DaemonSelectorProto) (*yaml.Node, error) {
	// If only type is set, use the compact string form
	if ds.Id == "" && ds.Name == "" && len(ds.Labels) == 0 && ds.Type != "" {
		return scalarNode(ds.Type, ""), nil
	}
	// If only name is set, use compact string form
	if ds.Id == "" && ds.Type == "" && len(ds.Labels) == 0 && ds.Name != "" {
		return scalarNode(ds.Name, ""), nil
	}
	// Full object form
	m := &yaml.Node{Kind: yaml.MappingNode}
	if ds.Id != "" {
		m.Content = append(m.Content, scalarNode("id", ""), scalarNode(ds.Id, ""))
	}
	if ds.Name != "" {
		m.Content = append(m.Content, scalarNode("name", ""), scalarNode(ds.Name, ""))
	}
	if ds.Type != "" {
		m.Content = append(m.Content, scalarNode("type", ""), scalarNode(ds.Type, ""))
	}
	if len(ds.Labels) > 0 {
		labelsNode := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range sortedKeys(ds.Labels) {
			labelsNode.Content = append(labelsNode.Content, scalarNode(k, ""), scalarNode(ds.Labels[k], ""))
		}
		m.Content = append(m.Content, scalarNode("labels", ""), labelsNode)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// DirectCelBool
// ---------------------------------------------------------------------------

func unmarshalDirectCelBool(node *yaml.Node) (*reliantv1.DirectCelBool, error) {
	if node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && node.Value == "") {
		return nil, nil
	}
	if node.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("DirectCelBool: expected scalar, got kind %v", node.Kind)
	}
	return &reliantv1.DirectCelBool{Expr: node.Value}, nil
}

func marshalDirectCelBool(dcb *reliantv1.DirectCelBool) (*yaml.Node, error) {
	if dcb == nil || dcb.Expr == "" {
		return nil, nil
	}
	return scalarNode(dcb.Expr, ""), nil
}

// ---------------------------------------------------------------------------
// YAML node helpers
// ---------------------------------------------------------------------------

// scalarNode creates a yaml.Node for a scalar string value.
// If tag is empty, no explicit tag is set (auto-detect).
func scalarNode(value, tag string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: value}
	if tag != "" {
		n.Tag = tag
	}
	return n
}

// getYAMLField extracts the value node for a given key from a mapping node.
// Returns nil if the key is not found.
func getYAMLField(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// getYAMLFieldString extracts the string value for a given key from a mapping node.
func getYAMLFieldString(node *yaml.Node, key string) string {
	n := getYAMLField(node, key)
	if n == nil {
		return ""
	}
	return n.Value
}
