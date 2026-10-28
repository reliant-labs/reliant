// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"gopkg.in/yaml.v3"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The `triggers:` block: WHEN a workflow should run. Each entry is a
// reliantv1.WorkflowTrigger, written in the proto's own field names:
//
//	triggers:
//	  - name: new-issue                  # unique within the workflow; a slug
//	    description: Triage new issues
//	    integration:                     # exactly one of: schedule, webhook,
//	      integration: github            #   integration, workflow_event
//	      events: [issues.opened]
//	      match: {repository: acme/app}
//	    filter: "trigger.payload.data.issue.user.login != 'dependabot[bot]'"
//	    inputs:
//	      issue_number: "{{ trigger.payload.data.issue.number }}"
//
// The canonical form IS the proto shape; there is no alias layer to drift
// from it. The codec walks the source messages by reflection, so a field
// added to ScheduleSource (say) round-trips without a change here. The sugar
// accepted on the way in is small and normalized on the way out:
//
//   - a repeated string may be a single scalar (`events: issues.opened`);
//   - an enum may be its short name (`overlap: allow`) or its proto name
//     (`TRIGGER_OVERLAP_POLICY_ALLOW`); it is written short;
//   - a source with no options may be a bare key (`webhook:`); it is written
//     `webhook: {}`;
//   - an integration's `match` entry may name a workflow input instead of a
//     value: `match: {repository: {input: repos}}` is
//     IntegrationSource.match_input (attribute -> input). The proto field is
//     not spelled in YAML; it is written back into `match`.
//
// Unlike node args, unknown keys are ERRORS at every level. A typo here is
// never harmless: a dropped `filtr:` is a trigger that fires on everything,
// and a dropped `crn:` is a schedule that never fires.

// triggerSourceOneof is the oneof on WorkflowTrigger that holds the source.
const triggerSourceOneof protoreflect.Name = "source"

// runtimeEvaluatedTopLevelKeys are workflow keys whose templates are NOT
// resolved when the definition loads (runtime.ResolveWorkflowTemplates).
// `triggers` belongs here: its inputs are templates over the launch event,
// evaluated when an event fires them, and a run started from a plain chat has
// no event to resolve them against. `on_failure` belongs here too: its nodes
// read `failure` and the node outputs of a run that is already over, none of
// which exist when the definition loads.
var runtimeEvaluatedTopLevelKeys = map[string]bool{
	"outputs":    true,
	"triggers":   true,
	"on_failure": true,
}

// IsRuntimeEvaluatedTopLevelKey reports whether a top-level workflow key's
// templates are evaluated later than workflow load.
func IsRuntimeEvaluatedTopLevelKey(key string) bool { return runtimeEvaluatedTopLevelKeys[key] }

func unmarshalWorkflowTriggers(node *yaml.Node) ([]*reliantv1.WorkflowTrigger, error) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected a list of triggers")
	}
	out := make([]*reliantv1.WorkflowTrigger, 0, len(node.Content))
	for i, item := range node.Content {
		wt, err := unmarshalWorkflowTrigger(item)
		if err != nil {
			name := ""
			if item.Kind == yaml.MappingNode {
				name = getYAMLFieldString(item, "name")
			}
			if name != "" {
				return nil, fmt.Errorf("[%d](%s): %w", i, name, err)
			}
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out = append(out, wt)
	}
	return out, nil
}

func unmarshalWorkflowTrigger(node *yaml.Node) (*reliantv1.WorkflowTrigger, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a mapping for a trigger")
	}
	wt := &reliantv1.WorkflowTrigger{}
	rv := wt.ProtoReflect()
	fields := rv.Descriptor().Fields()

	var sourceSet protoreflect.FieldDescriptor
	for i := 0; i < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		fd := fields.ByName(protoreflect.Name(key))
		if fd == nil {
			return nil, unknownFieldError(key, rv.Descriptor())
		}
		if oneof := fd.ContainingOneof(); oneof != nil && oneof.Name() == triggerSourceOneof {
			if sourceSet != nil {
				return nil, fmt.Errorf("a trigger has exactly one source; found both %q and %q", sourceSet.Name(), fd.Name())
			}
			sourceSet = fd
		}
		if err := setStrictField(rv, fd, val); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return wt, nil
}

// setStrictField sets one field from YAML, rejecting anything the field
// cannot represent.
func setStrictField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	switch {
	case fd.IsMap():
		return setStrictMapField(rv, fd, node)
	case fd.IsList():
		if fd.Kind() != protoreflect.StringKind {
			return fmt.Errorf("unsupported list of %v", fd.Kind())
		}
		strs, err := unmarshalScalarOrScalarList(node)
		if err != nil {
			return err
		}
		list := rv.Mutable(fd).List()
		for _, s := range strs {
			list.Append(protoreflect.ValueOfString(s))
		}
		return nil
	case fd.Kind() == protoreflect.MessageKind:
		msg := rv.NewField(fd).Message()
		if err := unmarshalStrictMessage(node, msg); err != nil {
			return err
		}
		// Set even when empty: `webhook: {}` selects the webhook source, and
		// an empty workflow_event matches a run of any workflow.
		rv.Set(fd, protoreflect.ValueOfMessage(msg))
		return nil
	case fd.Kind() == protoreflect.EnumKind:
		return setEnumField(rv, fd, node)
	default:
		if node.Kind != yaml.ScalarNode {
			return fmt.Errorf("expected a scalar")
		}
		return setScalarField(rv, fd, node)
	}
}

// unmarshalStrictMessage fills msg from a mapping; null or a bare key is an
// empty message.
func unmarshalStrictMessage(node *yaml.Node, msg protoreflect.Message) error {
	if node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || node.Value == "") {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping")
	}
	fields := msg.Descriptor().Fields()
	isIntegration := msg.Descriptor().FullName() == integrationSourceName
	for i := 0; i < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		fd := fields.ByName(protoreflect.Name(key))
		if fd == nil {
			return unknownFieldError(key, msg.Descriptor())
		}
		if isIntegration {
			switch key {
			case integrationMatchInputField:
				return fmt.Errorf("%s: write the scope inside match: `match: {repository: {input: repos}}`", key)
			case integrationMatchField:
				if err := setIntegrationMatch(msg, val); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				continue
			}
		}
		if err := setStrictField(msg, fd, val); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

const integrationSourceName protoreflect.FullName = "reliant.v1.IntegrationSource"

const (
	integrationMatchField      = "match"
	integrationMatchInputField = "match_input"
)

// setIntegrationMatch reads an integration source's `match:` mapping. An entry
// is an attribute and either the value it must equal or `{input: <name>}`, the
// workflow input whose values it must be one of.
func setIntegrationMatch(msg protoreflect.Message, node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping of attributes to values")
	}
	fields := msg.Descriptor().Fields()
	literals := msg.Mutable(fields.ByName(integrationMatchField)).Map()
	inputs := msg.Mutable(fields.ByName(integrationMatchInputField)).Map()
	for i := 0; i < len(node.Content); i += 2 {
		attr, val := node.Content[i].Value, node.Content[i+1]
		switch val.Kind {
		case yaml.ScalarNode:
			literals.Set(protoreflect.ValueOfString(attr).MapKey(), protoreflect.ValueOfString(val.Value))
		case yaml.MappingNode:
			input := ""
			if len(val.Content) == 2 && val.Content[0].Value == "input" && val.Content[1].Kind == yaml.ScalarNode {
				input = val.Content[1].Value
			}
			if input == "" {
				return fmt.Errorf("%q: expected a value, or {input: <workflow input name>}", attr)
			}
			inputs.Set(protoreflect.ValueOfString(attr).MapKey(), protoreflect.ValueOfString(input))
		default:
			return fmt.Errorf("%q: expected a value, or {input: <workflow input name>}, got a %s", attr, yamlKindName(val.Kind))
		}
	}
	return nil
}

func setStrictMapField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	if fd.MapKey().Kind() != protoreflect.StringKind || fd.MapValue().Kind() != protoreflect.StringKind {
		return fmt.Errorf("unsupported map type")
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping of names to strings")
	}
	m := rv.Mutable(fd).Map()
	for i := 0; i < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if val.Kind != yaml.ScalarNode {
			return fmt.Errorf("%q: expected a string, got a %s", key, yamlKindName(val.Kind))
		}
		m.Set(protoreflect.ValueOfString(key).MapKey(), protoreflect.ValueOfString(val.Value))
	}
	return nil
}

// setEnumField accepts the value's short name (the proto name with the enum's
// common prefix removed, lowercased: "allow") or its full proto name.
func setEnumField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("expected a scalar")
	}
	ed := fd.Enum()
	want := strings.TrimSpace(node.Value)
	var allowed []string
	for i := 0; i < ed.Values().Len(); i++ {
		v := ed.Values().Get(i)
		if v.Number() == 0 {
			continue
		}
		short := enumShortName(ed, v)
		allowed = append(allowed, short)
		if strings.EqualFold(want, short) || want == string(v.Name()) {
			rv.Set(fd, protoreflect.ValueOfEnum(v.Number()))
			return nil
		}
	}
	return fmt.Errorf("%q is not one of %s", want, strings.Join(allowed, ", "))
}

// enumShortName is TRIGGER_OVERLAP_POLICY_ALLOW -> "allow": the value name
// without the enum's SCREAMING_CASE prefix.
func enumShortName(ed protoreflect.EnumDescriptor, v protoreflect.EnumValueDescriptor) string {
	prefix := screamingSnake(string(ed.Name())) + "_"
	return strings.ToLower(strings.TrimPrefix(string(v.Name()), prefix))
}

func screamingSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

func unmarshalScalarOrScalarList(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return nil, nil
		}
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("expected a list of strings")
			}
			out = append(out, item.Value)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a string or a list of strings")
	}
}

func unknownFieldError(key string, md protoreflect.MessageDescriptor) error {
	known := make([]string, 0, md.Fields().Len())
	for i := 0; i < md.Fields().Len(); i++ {
		known = append(known, string(md.Fields().Get(i).Name()))
	}
	return unknownField(key, "unknown field %q (known: %s)", key, strings.Join(known, ", "))
}

func yamlKindName(k yaml.Kind) string {
	switch k {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "list"
	case yaml.AliasNode:
		return "alias"
	default:
		return "scalar"
	}
}

// ---------------------------------------------------------------------------
// Marshal
// ---------------------------------------------------------------------------

// marshalWorkflowTriggers writes the canonical form. Field order is the
// proto's except that the source comes right after the description — the
// order a reader wants: what it is, what fires it, then how it is narrowed
// and what it passes on.
func marshalWorkflowTriggers(triggers []*reliantv1.WorkflowTrigger) (*yaml.Node, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for i, wt := range triggers {
		m, err := marshalWorkflowTrigger(wt)
		if err != nil {
			return nil, fmt.Errorf("triggers[%d]: %w", i, err)
		}
		seq.Content = append(seq.Content, m)
	}
	return seq, nil
}

func marshalWorkflowTrigger(wt *reliantv1.WorkflowTrigger) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	rv := wt.ProtoReflect()
	fields := rv.Descriptor().Fields()

	emit := func(fd protoreflect.FieldDescriptor) error {
		if !rv.Has(fd) {
			return nil
		}
		n, err := marshalStrictField(fd, rv.Get(fd))
		if err != nil {
			return fmt.Errorf("%s: %w", fd.Name(), err)
		}
		if n != nil {
			m.Content = append(m.Content, scalarNode(string(fd.Name()), ""), n)
		}
		return nil
	}

	for _, name := range []protoreflect.Name{"name", "description"} {
		if err := emit(fields.ByName(name)); err != nil {
			return nil, err
		}
	}
	if src := rv.WhichOneof(rv.Descriptor().Oneofs().ByName(triggerSourceOneof)); src != nil {
		if err := emit(src); err != nil {
			return nil, err
		}
	}
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		switch fd.Name() {
		case "name", "description":
			continue
		}
		if oneof := fd.ContainingOneof(); oneof != nil && oneof.Name() == triggerSourceOneof {
			continue
		}
		if err := emit(fd); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func marshalStrictField(fd protoreflect.FieldDescriptor, val protoreflect.Value) (*yaml.Node, error) {
	switch {
	case fd.IsMap():
		entries := map[string]string{}
		val.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			entries[k.String()] = v.String()
			return true
		})
		m := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range sortedKeys(entries) {
			m.Content = append(m.Content, scalarNode(k, ""), stringNode(entries[k]))
		}
		return m, nil
	case fd.IsList():
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for i := 0; i < val.List().Len(); i++ {
			seq.Content = append(seq.Content, stringNode(val.List().Get(i).String()))
		}
		return seq, nil
	case fd.Kind() == protoreflect.MessageKind:
		return marshalStrictMessage(val.Message())
	case fd.Kind() == protoreflect.EnumKind:
		v := fd.Enum().Values().ByNumber(val.Enum())
		if v == nil || v.Number() == 0 {
			return nil, nil
		}
		return scalarNode(enumShortName(fd.Enum(), v), ""), nil
	case fd.Kind() == protoreflect.StringKind:
		return stringNode(val.String()), nil
	default:
		return marshalScalarFieldToYAML(fd, val)
	}
}

func marshalStrictMessage(msg protoreflect.Message) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	fields := msg.Descriptor().Fields()
	isIntegration := msg.Descriptor().FullName() == integrationSourceName
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isIntegration {
			switch fd.Name() {
			case integrationMatchInputField:
				continue
			case integrationMatchField:
				if n := marshalIntegrationMatch(msg); n != nil {
					m.Content = append(m.Content, scalarNode(integrationMatchField, ""), n)
				}
				continue
			}
		}
		if !msg.Has(fd) {
			continue
		}
		n, err := marshalStrictField(fd, msg.Get(fd))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fd.Name(), err)
		}
		if n != nil {
			m.Content = append(m.Content, scalarNode(string(fd.Name()), ""), n)
		}
	}
	if len(m.Content) == 0 {
		m.Style = yaml.FlowStyle
	}
	return m, nil
}

// marshalIntegrationMatch writes match and match_input as the one `match:`
// mapping they are authored in; nil when both are empty.
func marshalIntegrationMatch(msg protoreflect.Message) *yaml.Node {
	fields := msg.Descriptor().Fields()
	entries := map[string]string{}
	inputs := map[string]bool{}
	msg.Get(fields.ByName(integrationMatchField)).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		entries[k.String()] = v.String()
		return true
	})
	msg.Get(fields.ByName(integrationMatchInputField)).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		entries[k.String()] = v.String()
		inputs[k.String()] = true
		return true
	})
	if len(entries) == 0 {
		return nil
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range sortedKeys(entries) {
		if inputs[k] {
			ref := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle,
				Content: []*yaml.Node{scalarNode("input", ""), stringNode(entries[k])}}
			m.Content = append(m.Content, scalarNode(k, ""), ref)
			continue
		}
		m.Content = append(m.Content, scalarNode(k, ""), stringNode(entries[k]))
	}
	return m
}

// stringNode is a scalar that always reads back as a string: a value such
// as "10", "true" or "~" is quoted rather than left to retype as a number,
// bool or null. Long values use a literal block, as everywhere else.
func stringNode(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!str"}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}
