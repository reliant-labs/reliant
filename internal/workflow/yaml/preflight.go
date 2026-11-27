package wfyaml

import (
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
	"gopkg.in/yaml.v3"
)

// unmarshalNodePreflight reads a node's `preflight:` (reliantv1.NodePreflight):
//
//	preflight: true                 # every default
//	preflight: false                # none (the same as leaving it out)
//	preflight:                      # overrides; each optional
//	  tools: [go, task]             # beside git, always checked
//	  min_free_disk: 10GiB          # default 5GiB
//	  max_load: 4                   # per CPU, 5-minute; default 2
//	  allow_root: true              # default false
//	  max_machines: 2               # default 3
//
// nil means no preflight. The block's shape and values are parse errors, like
// a typo in any other key (machinepreflight.FromProto).
func unmarshalNodePreflight(node *yaml.Node) (*reliantv1.NodePreflight, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		var on bool
		if (node.Tag != "" && node.Tag != "!!bool") || node.Decode(&on) != nil {
			return nil, fmt.Errorf("preflight must be true, false or a mapping of tools, min_free_disk, max_load, allow_root and max_machines")
		}
		if !on {
			return nil, nil
		}
		return &reliantv1.NodePreflight{}, nil
	case yaml.MappingNode:
	default:
		return nil, fmt.Errorf("preflight must be true, false or a mapping of tools, min_free_disk, max_load, allow_root and max_machines")
	}
	p := &reliantv1.NodePreflight{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if seen[key] {
			return nil, fmt.Errorf("preflight.%s is set twice", key)
		}
		seen[key] = true
		switch key {
		case "tools":
			if val.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("preflight.tools must be a list of command names")
			}
			for _, item := range val.Content {
				if item.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("preflight.tools must be a list of command names")
				}
				p.Tools = append(p.Tools, item.Value)
			}
		case "min_free_disk":
			if val.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("preflight.min_free_disk must be a size such as 10GiB")
			}
			p.MinFreeDisk = val.Value
		case "max_load":
			f, err := strconv.ParseFloat(val.Value, 64)
			if val.Kind != yaml.ScalarNode || err != nil {
				return nil, fmt.Errorf("preflight.max_load must be a number")
			}
			p.MaxLoad = f
		case "allow_root":
			var b bool
			if val.Kind != yaml.ScalarNode || (val.Tag != "" && val.Tag != "!!bool") || val.Decode(&b) != nil {
				return nil, fmt.Errorf("preflight.allow_root must be true or false")
			}
			p.AllowRoot = b
		case "max_machines":
			n, err := strconv.ParseInt(val.Value, 10, 32)
			if val.Kind != yaml.ScalarNode || err != nil {
				return nil, fmt.Errorf("preflight.max_machines must be a whole number")
			}
			p.MaxMachines = int32(n)
		default:
			return nil, unknownField(key, "unknown preflight field: %q (tools, min_free_disk, max_load, allow_root, max_machines)", key)
		}
	}
	if _, _, err := machinepreflight.FromProto(p); err != nil {
		return nil, fmt.Errorf("preflight.%w", err)
	}
	return p, nil
}

// marshalNodePreflight writes p as `preflight: true` when it overrides
// nothing, else as its mapping.
func marshalNodePreflight(p *reliantv1.NodePreflight) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(p.GetTools()) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, t := range p.GetTools() {
			seq.Content = append(seq.Content, scalarNode(t, ""))
		}
		m.Content = append(m.Content, scalarNode("tools", ""), seq)
	}
	if v := p.GetMinFreeDisk(); v != "" {
		m.Content = append(m.Content, scalarNode("min_free_disk", ""), scalarNode(v, ""))
	}
	if v := p.GetMaxLoad(); v != 0 {
		m.Content = append(m.Content, scalarNode("max_load", ""), scalarNode(strconv.FormatFloat(v, 'f', -1, 64), ""))
	}
	if p.GetAllowRoot() {
		m.Content = append(m.Content, scalarNode("allow_root", ""), scalarNode("true", "!!bool"))
	}
	if v := p.GetMaxMachines(); v != 0 {
		m.Content = append(m.Content, scalarNode("max_machines", ""), scalarNode(strconv.Itoa(int(v)), ""))
	}
	if len(m.Content) == 0 {
		return scalarNode("true", "!!bool")
	}
	return m
}
