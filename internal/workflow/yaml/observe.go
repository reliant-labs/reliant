package wfyaml

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"gopkg.in/yaml.v3"
)

// unmarshalNodeObserve reads `observe:`, on a node or on the workflow
// (reliantv1.NodeObserve):
//
//	observe:
//	  attributes:                      # span attributes, key -> template
//	    trigger: "{{ output.trigger }}"
//	  events:                          # span events
//	    - name: decision
//	      when: output.cut == false    # optional, raw CEL
//	      each: "{{ output.prs }}"     # optional: one event per element, as item
//	      attributes: { name: cut, value: "{{ output.cut ? 'cut' : 'no_cut' }}" }
//	  metrics:                         # OTEL instruments
//	    - name: rc_train.ticks
//	      kind: counter                # counter | histogram | gauge
//	      value: "{{ 1 }}"             # optional for a counter
//	      unit: "{call}"               # optional
//	      when: ...                    # optional, raw CEL
//	      attributes: { decision: "{{ output.cut ? 'cut' : 'no_cut' }}" }
//
// The block's shape is a parse error, like a typo in any other key: an
// unknown key, a bad name or kind, a count over its limit
// (model.ObserveProblems). Its expressions are validation's, and only ever a
// warning there: a node's observability must never stop it from running.
func unmarshalNodeObserve(node *yaml.Node) (*reliantv1.NodeObserve, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("observe must be a mapping of attributes, events and metrics")
	}
	o := &reliantv1.NodeObserve{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if seen[key] {
			return nil, fmt.Errorf("observe.%s is set twice", key)
		}
		seen[key] = true
		var err error
		switch key {
		case "attributes":
			o.Attributes, err = unmarshalObserveAttributes(val, "observe.attributes")
		case "events":
			o.Events, err = unmarshalObserveEvents(val)
		case "metrics":
			o.Metrics, err = unmarshalObserveMetrics(val)
		default:
			return nil, unknownField(key, "unknown observe field: %q (attributes, events, metrics)", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if problems := model.ObserveProblems(o); len(problems) > 0 {
		msgs := make([]string, 0, len(problems))
		for _, p := range problems {
			msgs = append(msgs, fmt.Sprintf("observe.%s: %s", strings.Join(p.Path, "."), p.Message))
		}
		return nil, fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return o, nil
}

// unmarshalObserveAttributes reads a key -> template mapping. A value is a
// scalar, kept as written: a template, or a literal.
func unmarshalObserveAttributes(node *yaml.Node, path string) (map[string]string, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping of key to template", path)
	}
	out := make(map[string]string, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%s.%s is set twice", path, key)
		}
		if val.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s.%s must be a string (a template such as \"{{ output.x }}\", or a literal)", path, key)
		}
		out[key] = val.Value
	}
	return out, nil
}

func unmarshalObserveEvents(node *yaml.Node) ([]*reliantv1.ObserveEvent, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("observe.events must be a list")
	}
	out := make([]*reliantv1.ObserveEvent, 0, len(node.Content))
	for i, item := range node.Content {
		path := fmt.Sprintf("observe.events[%d]", i)
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s must be a mapping of name, when, each and attributes", path)
		}
		e := &reliantv1.ObserveEvent{}
		seen := map[string]bool{}
		for j := 0; j+1 < len(item.Content); j += 2 {
			key, val := item.Content[j].Value, item.Content[j+1]
			if seen[key] {
				return nil, fmt.Errorf("%s.%s is set twice", path, key)
			}
			seen[key] = true
			var err error
			switch key {
			case "name":
				e.Name, err = observeScalar(val, path, key)
			case "when":
				e.When, err = observeScalar(val, path, key)
			case "each":
				e.Each, err = observeScalar(val, path, key)
			case "attributes":
				e.Attributes, err = unmarshalObserveAttributes(val, path+".attributes")
			default:
				return nil, unknownField(key, "%s: unknown key %q (name, when, each, attributes)", path, key)
			}
			if err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func unmarshalObserveMetrics(node *yaml.Node) ([]*reliantv1.ObserveMetric, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("observe.metrics must be a list")
	}
	out := make([]*reliantv1.ObserveMetric, 0, len(node.Content))
	for i, item := range node.Content {
		path := fmt.Sprintf("observe.metrics[%d]", i)
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s must be a mapping of name, kind, value, unit, when and attributes", path)
		}
		m := &reliantv1.ObserveMetric{}
		seen := map[string]bool{}
		for j := 0; j+1 < len(item.Content); j += 2 {
			key, val := item.Content[j].Value, item.Content[j+1]
			if seen[key] {
				return nil, fmt.Errorf("%s.%s is set twice", path, key)
			}
			seen[key] = true
			var err error
			switch key {
			case "name":
				m.Name, err = observeScalar(val, path, key)
			case "kind":
				m.Kind, err = observeScalar(val, path, key)
			case "value":
				m.Value, err = observeScalar(val, path, key)
			case "unit":
				m.Unit, err = observeScalar(val, path, key)
			case "when":
				m.When, err = observeScalar(val, path, key)
			case "attributes":
				m.Attributes, err = unmarshalObserveAttributes(val, path+".attributes")
			default:
				return nil, unknownField(key, "%s: unknown key %q (name, kind, value, unit, when, attributes)", path, key)
			}
			if err != nil {
				return nil, err
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func observeScalar(val *yaml.Node, path, key string) (string, error) {
	if val.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s.%s must be a string", path, key)
	}
	return val.Value, nil
}

// marshalNodeObserve is the inverse of unmarshalNodeObserve. An empty block
// is written as `{}`, so it survives a round trip.
func marshalNodeObserve(o *reliantv1.NodeObserve) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(o.GetAttributes()) > 0 {
		node.Content = append(node.Content, scalarNode("attributes", ""), marshalObserveAttributes(o.GetAttributes()))
	}
	if len(o.GetEvents()) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range o.GetEvents() {
			m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = appendObserveScalar(m.Content, "name", e.GetName())
			m.Content = appendObserveScalar(m.Content, "when", e.GetWhen())
			m.Content = appendObserveScalar(m.Content, "each", e.GetEach())
			if len(e.GetAttributes()) > 0 {
				m.Content = append(m.Content, scalarNode("attributes", ""), marshalObserveAttributes(e.GetAttributes()))
			}
			seq.Content = append(seq.Content, m)
		}
		node.Content = append(node.Content, scalarNode("events", ""), seq)
	}
	if len(o.GetMetrics()) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, mt := range o.GetMetrics() {
			m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = appendObserveScalar(m.Content, "name", mt.GetName())
			m.Content = appendObserveScalar(m.Content, "kind", mt.GetKind())
			m.Content = appendObserveScalar(m.Content, "value", mt.GetValue())
			m.Content = appendObserveScalar(m.Content, "unit", mt.GetUnit())
			m.Content = appendObserveScalar(m.Content, "when", mt.GetWhen())
			if len(mt.GetAttributes()) > 0 {
				m.Content = append(m.Content, scalarNode("attributes", ""), marshalObserveAttributes(mt.GetAttributes()))
			}
			seq.Content = append(seq.Content, m)
		}
		node.Content = append(node.Content, scalarNode("metrics", ""), seq)
	}
	return node
}

func marshalObserveAttributes(attrs map[string]string) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range sortedKeys(attrs) {
		m.Content = append(m.Content, scalarNode(k, ""), scalarNode(attrs[k], "!!str"))
	}
	return m
}

func appendObserveScalar(content []*yaml.Node, key, value string) []*yaml.Node {
	if value == "" {
		return content
	}
	return append(content, scalarNode(key, ""), scalarNode(value, "!!str"))
}
