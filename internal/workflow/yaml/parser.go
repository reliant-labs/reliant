package wfyaml

import (
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// ParseWorkflow parses YAML bytes into a *reliantv1.Workflow proto message.
// It preserves the user-facing YAML syntax with CEL expression detection,
// type-dispatched node args, and string-or-array flexibility.
func ParseWorkflow(data []byte) (*reliantv1.Workflow, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("v2yaml: parse YAML: %w", err)
	}

	// yaml.Unmarshal wraps the top-level node in a document node
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("v2yaml: expected document node")
	}

	return unmarshalWorkflow(doc.Content[0])
}

// MarshalWorkflow serializes a *reliantv1.Workflow to YAML bytes.
// It produces the user-facing YAML syntax with CEL expressions using `{{}}`
// delimiters, type-dispatched node args, and compact string-or-array formatting.
func MarshalWorkflow(wf *reliantv1.Workflow) ([]byte, error) {
	node, err := marshalWorkflow(wf)
	if err != nil {
		return nil, fmt.Errorf("v2yaml: marshal workflow: %w", err)
	}

	doc := &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{node},
	}

	return yaml.Marshal(doc)
}

// ---------------------------------------------------------------------------
// Unmarshal Workflow
// ---------------------------------------------------------------------------

func unmarshalWorkflow(node *yaml.Node) (*reliantv1.Workflow, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("V2Workflow: expected mapping, got kind %v", node.Kind)
	}

	wf := &reliantv1.Workflow{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error

		switch key {
		case "name":
			wf.Name = val.Value
		case "description":
			var desc string
			if err := val.Decode(&desc); err == nil {
				wf.Description = desc
			}
		case "apiVersion", "api_version":
			wf.ApiVersion = val.Value

		case "entry":
			wf.Entry, err = unmarshalStringOrStringSlice(val)

		case "presets":
			wf.Presets, err = unmarshalPresetsConfig(val)

		case "inputs":
			wf.Inputs, err = unmarshalInputMap(val)

		case "outputs":
			wf.Outputs, err = unmarshalOutputMap(val)

		case "nodes":
			wf.Nodes, err = unmarshalNodeList(val)

		case "edges":
			wf.Edges, err = unmarshalEdgeList(val)

		case "ui":
			wf.Ui, err = unmarshalWorkflowUI(val)

		case "daemon":
			wf.Daemon, err = unmarshalCelDaemonSelector(val)

		default:
			return nil, fmt.Errorf("unknown workflow field: %q", key)
		}

		if err != nil {
			return nil, fmt.Errorf("workflow.%s: %w", key, err)
		}
	}
	return wf, nil
}

func unmarshalPresetsConfig(node *yaml.Node) (*reliantv1.PresetsConfig, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for presets config")
	}
	pc := &reliantv1.PresetsConfig{}
	pc.Tag = getYAMLFieldString(node, "tag")
	pc.Default = getYAMLFieldString(node, "default")
	return pc, nil
}

func unmarshalInputMap(node *yaml.Node) (map[string]*reliantv1.Input, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for inputs")
	}
	result := make(map[string]*reliantv1.Input)
	for i := 0; i < len(node.Content); i += 2 {
		name := node.Content[i].Value
		val := node.Content[i+1]
		input, err := unmarshalInput(val)
		if err != nil {
			return nil, fmt.Errorf("input '%s': %w", name, err)
		}
		result[name] = input
	}
	return result, nil
}

func unmarshalOutputMap(node *yaml.Node) (map[string]string, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for outputs")
	}
	result := make(map[string]string)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		var val string
		if err := node.Content[i+1].Decode(&val); err != nil {
			return nil, fmt.Errorf("output '%s': %w", key, err)
		}
		result[key] = val
	}
	return result, nil
}

func unmarshalNodeList(node *yaml.Node) ([]*reliantv1.Node, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence for nodes")
	}
	var nodes []*reliantv1.Node
	for i, item := range node.Content {
		n, err := unmarshalNode(item)
		if err != nil {
			return nil, fmt.Errorf("node[%d]: %w", i, err)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func unmarshalEdgeList(node *yaml.Node) ([]*reliantv1.Edge, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence for edges")
	}
	var edges []*reliantv1.Edge
	for i, item := range node.Content {
		e, err := unmarshalEdge(item)
		if err != nil {
			return nil, fmt.Errorf("edge[%d]: %w", i, err)
		}
		edges = append(edges, e)
	}
	return edges, nil
}

// ---------------------------------------------------------------------------
// UI types unmarshal
// ---------------------------------------------------------------------------

func unmarshalWorkflowUI(node *yaml.Node) (*reliantv1.WorkflowUI, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for UI")
	}
	ui := &reliantv1.WorkflowUI{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error
		switch key {
		case "positions":
			ui.Positions, err = unmarshalPositionMap(val)
		case "switches":
			ui.Switches, err = unmarshalSwitchMap(val)
		case "locked":
			var b bool
			if err := val.Decode(&b); err == nil {
				ui.Locked = b
			}
		}
		if err != nil {
			return nil, fmt.Errorf("ui.%s: %w", key, err)
		}
	}
	return ui, nil
}

func unmarshalPositionMap(node *yaml.Node) (map[string]*reliantv1.Position, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for positions")
	}
	result := make(map[string]*reliantv1.Position)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		pos, err := unmarshalPosition(val)
		if err != nil {
			return nil, fmt.Errorf("position '%s': %w", key, err)
		}
		result[key] = pos
	}
	return result, nil
}

func unmarshalPosition(node *yaml.Node) (*reliantv1.Position, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for position")
	}
	pos := &reliantv1.Position{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case "x":
			var f float64
			if err := val.Decode(&f); err != nil {
				return nil, err
			}
			pos.X = f
		case "y":
			var f float64
			if err := val.Decode(&f); err != nil {
				return nil, err
			}
			pos.Y = f
		}
	}
	return pos, nil
}

func unmarshalSwitchMap(node *yaml.Node) (map[string]*reliantv1.SwitchMetadata, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for switches")
	}
	result := make(map[string]*reliantv1.SwitchMetadata)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		sw, err := unmarshalSwitchMetadata(val)
		if err != nil {
			return nil, fmt.Errorf("switch '%s': %w", key, err)
		}
		result[key] = sw
	}
	return result, nil
}

func unmarshalSwitchMetadata(node *yaml.Node) (*reliantv1.SwitchMetadata, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for switch metadata")
	}
	sm := &reliantv1.SwitchMetadata{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error
		switch key {
		case "source_node":
			sm.SourceNode = val.Value
		case "position":
			sm.Position, err = unmarshalPosition(val)
		case "cases":
			sm.Cases, err = unmarshalSwitchCases(val)
		}
		if err != nil {
			return nil, fmt.Errorf("switch.%s: %w", key, err)
		}
	}
	return sm, nil
}

func unmarshalSwitchCases(node *yaml.Node) ([]*reliantv1.SwitchCase, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence for switch cases")
	}
	var cases []*reliantv1.SwitchCase
	for _, item := range node.Content {
		sc := &reliantv1.SwitchCase{}
		for i := 0; i < len(item.Content); i += 2 {
			key := item.Content[i].Value
			val := item.Content[i+1]
			switch key {
			case "id":
				sc.Id = val.Value
			case "condition":
				dcb, err := unmarshalDirectCelBool(val)
				if err != nil {
					return nil, err
				}
				sc.Condition = dcb
			case "label":
				sc.Label = val.Value
			}
		}
		cases = append(cases, sc)
	}
	return cases, nil
}

// ---------------------------------------------------------------------------
// Marshal Workflow
// ---------------------------------------------------------------------------

func marshalWorkflow(wf *reliantv1.Workflow) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}

	// name
	if wf.Name != "" {
		m.Content = append(m.Content, scalarNode("name", ""), scalarNode(wf.Name, ""))
	}

	// apiVersion
	if wf.ApiVersion != "" {
		m.Content = append(m.Content, scalarNode("apiVersion", ""), scalarNode(wf.ApiVersion, ""))
	}

	// description
	if wf.Description != "" {
		descNode := scalarNode(wf.Description, "")
		if len(wf.Description) > 80 {
			descNode.Style = yaml.LiteralStyle
		}
		m.Content = append(m.Content, scalarNode("description", ""), descNode)
	}

	// presets
	if wf.Presets != nil {
		pn := &yaml.Node{Kind: yaml.MappingNode}
		if wf.Presets.Tag != "" {
			pn.Content = append(pn.Content, scalarNode("tag", ""), scalarNode(wf.Presets.Tag, ""))
		}
		if wf.Presets.Default != "" {
			pn.Content = append(pn.Content, scalarNode("default", ""), scalarNode(wf.Presets.Default, ""))
		}
		m.Content = append(m.Content, scalarNode("presets", ""), pn)
	}

	// inputs
	if len(wf.Inputs) > 0 {
		inputsNode, err := marshalInputMap(wf.Inputs)
		if err != nil {
			return nil, err
		}
		m.Content = append(m.Content, scalarNode("inputs", ""), inputsNode)
	}

	// outputs
	if len(wf.Outputs) > 0 {
		outputsNode := &yaml.Node{Kind: yaml.MappingNode}
		for k, v := range wf.Outputs {
			outputsNode.Content = append(outputsNode.Content, scalarNode(k, ""), scalarNode(v, ""))
		}
		m.Content = append(m.Content, scalarNode("outputs", ""), outputsNode)
	}

	// daemon
	if wf.Daemon != nil {
		dn, err := marshalCelDaemonSelector(wf.Daemon)
		if err != nil {
			return nil, err
		}
		if dn != nil {
			m.Content = append(m.Content, scalarNode("daemon", ""), dn)
		}
	}

	// entry — always emit as array
	if len(wf.Entry) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, s := range wf.Entry {
			seq.Content = append(seq.Content, scalarNode(s, ""))
		}
		m.Content = append(m.Content, scalarNode("entry", ""), seq)
	}

	// nodes
	if len(wf.Nodes) > 0 {
		nodesNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, node := range wf.Nodes {
			nn, err := marshalNode(node)
			if err != nil {
				return nil, err
			}
			nodesNode.Content = append(nodesNode.Content, nn)
		}
		m.Content = append(m.Content, scalarNode("nodes", ""), nodesNode)
	}

	// edges
	if len(wf.Edges) > 0 {
		edgesNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, edge := range wf.Edges {
			en, err := marshalEdge(edge)
			if err != nil {
				return nil, err
			}
			edgesNode.Content = append(edgesNode.Content, en)
		}
		m.Content = append(m.Content, scalarNode("edges", ""), edgesNode)
	}

	// ui
	if wf.Ui != nil {
		uiNode, err := marshalWorkflowUI(wf.Ui)
		if err != nil {
			return nil, err
		}
		m.Content = append(m.Content, scalarNode("ui", ""), uiNode)
	}

	return m, nil
}

// ---------------------------------------------------------------------------
// UI types marshal
// ---------------------------------------------------------------------------

func marshalWorkflowUI(ui *reliantv1.WorkflowUI) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if len(ui.Positions) > 0 {
		posNode := &yaml.Node{Kind: yaml.MappingNode}
		for k, v := range ui.Positions {
			pn := &yaml.Node{Kind: yaml.MappingNode}
			pn.Content = append(pn.Content,
				scalarNode("x", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.FormatFloat(v.X, 'f', -1, 64), Tag: "!!float"},
				scalarNode("y", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.FormatFloat(v.Y, 'f', -1, 64), Tag: "!!float"},
			)
			posNode.Content = append(posNode.Content, scalarNode(k, ""), pn)
		}
		m.Content = append(m.Content, scalarNode("positions", ""), posNode)
	}
	if len(ui.Switches) > 0 {
		swNode := &yaml.Node{Kind: yaml.MappingNode}
		for k, v := range ui.Switches {
			smNode, err := marshalSwitchMetadata(v)
			if err != nil {
				return nil, err
			}
			swNode.Content = append(swNode.Content, scalarNode(k, ""), smNode)
		}
		m.Content = append(m.Content, scalarNode("switches", ""), swNode)
	}
	if ui.Locked {
		m.Content = append(m.Content, scalarNode("locked", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: "true", Tag: "!!bool"})
	}
	return m, nil
}

func marshalSwitchMetadata(sm *reliantv1.SwitchMetadata) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if sm.SourceNode != "" {
		m.Content = append(m.Content, scalarNode("source_node", ""), scalarNode(sm.SourceNode, ""))
	}
	if sm.Position != nil {
		pn := &yaml.Node{Kind: yaml.MappingNode}
		pn.Content = append(pn.Content,
			scalarNode("x", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.FormatFloat(sm.Position.X, 'f', -1, 64), Tag: "!!float"},
			scalarNode("y", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.FormatFloat(sm.Position.Y, 'f', -1, 64), Tag: "!!float"},
		)
		m.Content = append(m.Content, scalarNode("position", ""), pn)
	}
	if len(sm.Cases) > 0 {
		casesNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, sc := range sm.Cases {
			scNode := &yaml.Node{Kind: yaml.MappingNode}
			if sc.Id != "" {
				scNode.Content = append(scNode.Content, scalarNode("id", ""), scalarNode(sc.Id, ""))
			}
			if sc.Condition != nil {
				cn, _ := marshalDirectCelBool(sc.Condition)
				if cn != nil {
					scNode.Content = append(scNode.Content, scalarNode("condition", ""), cn)
				} else {
					// Empty condition (default case)
					scNode.Content = append(scNode.Content, scalarNode("condition", ""), scalarNode("", ""))
				}
			}
			if sc.Label != "" {
				scNode.Content = append(scNode.Content, scalarNode("label", ""), scalarNode(sc.Label, ""))
			}
			casesNode.Content = append(casesNode.Content, scNode)
		}
		m.Content = append(m.Content, scalarNode("cases", ""), casesNode)
	}
	return m, nil
}
