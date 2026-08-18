package wfyaml

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Unmarshal V2Input
// ---------------------------------------------------------------------------

func unmarshalInput(node *yaml.Node) (*reliantv1.Input, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("V2Input: expected mapping, got kind %v", node.Kind)
	}

	input := &reliantv1.Input{}

	// Extract the type discriminator first
	input.Type = getYAMLFieldString(node, "type")
	if input.Type == "" {
		return nil, fmt.Errorf("V2Input: missing 'type' field")
	}

	// Dispatch to correct config type
	switch input.Type {
	case "string":
		cfg, err := unmarshalStringInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_StringInput{StringInput: cfg}
	case "number":
		cfg, err := unmarshalNumberInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_NumberInput{NumberInput: cfg}
	case "integer":
		cfg, err := unmarshalIntegerInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_IntegerInput{IntegerInput: cfg}
	case "boolean":
		cfg, err := unmarshalBooleanInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_BooleanInput{BooleanInput: cfg}
	case "enum":
		cfg, err := unmarshalEnumInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_EnumInput{EnumInput: cfg}
	case "model":
		cfg, err := unmarshalModelInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_ModelInput{ModelInput: cfg}
	case "message":
		cfg, err := unmarshalMessageInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_MessageInput{MessageInput: cfg}
	case "attachments":
		cfg, err := unmarshalAttachmentsInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_AttachmentsInput{AttachmentsInput: cfg}
	case "tools":
		cfg, err := unmarshalToolsInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_ToolsInput{ToolsInput: cfg}
	case "array":
		cfg, err := unmarshalArrayInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_ArrayInput{ArrayInput: cfg}
	case "object":
		cfg, err := unmarshalObjectInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_ObjectInput{ObjectInput: cfg}
	case "any":
		cfg, err := unmarshalAnyInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_AnyInput{AnyInput: cfg}
	case "group":
		cfg, err := unmarshalGroupInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_GroupInput{GroupInput: cfg}
	case "preset":
		cfg, err := unmarshalPresetInputConfig(node)
		if err != nil {
			return nil, err
		}
		input.Config = &reliantv1.Input_PresetInput{PresetInput: cfg}
	default:
		return nil, fmt.Errorf("V2Input: unknown type %q", input.Type)
	}

	return input, nil
}

// unmarshalInputBase extracts description and ui from a mapping node.
func unmarshalInputBase(node *yaml.Node) *reliantv1.InputBase {
	base := &reliantv1.InputBase{}
	base.Description = getYAMLFieldString(node, "description")
	base.Ui = getYAMLFieldString(node, "ui")
	if base.Description == "" && base.Ui == "" {
		return nil
	}
	return base
}

// unmarshalOptionalString extracts an optional string field from a mapping node.
func unmarshalOptionalString(node *yaml.Node, key string) *string {
	n := getYAMLField(node, key)
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	s := n.Value
	return &s
}

// unmarshalOptionalBool extracts an optional bool field from a mapping node.
func unmarshalOptionalBool(node *yaml.Node, key string) *bool {
	n := getYAMLField(node, key)
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	var b bool
	if err := n.Decode(&b); err != nil {
		return nil
	}
	return &b
}

// unmarshalOptionalInt32 extracts an optional int32 field from a mapping node.
func unmarshalOptionalInt32(node *yaml.Node, key string) *int32 {
	n := getYAMLField(node, key)
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	var i int
	if err := n.Decode(&i); err != nil {
		return nil
	}
	i32 := int32(i)
	return &i32
}

// unmarshalOptionalInt64 extracts an optional int64 field from a mapping node.
func unmarshalOptionalInt64(node *yaml.Node, key string) *int64 {
	n := getYAMLField(node, key)
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	var i int64
	if err := n.Decode(&i); err != nil {
		return nil
	}
	return &i
}

// unmarshalOptionalFloat64 extracts an optional float64 field from a mapping node.
func unmarshalOptionalFloat64(node *yaml.Node, key string) *float64 {
	n := getYAMLField(node, key)
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	var f float64
	if err := n.Decode(&f); err != nil {
		return nil
	}
	return &f
}

// unmarshalStructValue converts a yaml.Node into a *structpb.Value.
func unmarshalStructValue(node *yaml.Node) (*structpb.Value, error) {
	if node == nil || node.Tag == "!!null" {
		return nil, nil
	}
	return yamlNodeToStructValue(node)
}

// ---------------------------------------------------------------------------
// Individual input config unmarshalers
// ---------------------------------------------------------------------------

func unmarshalStringInputConfig(node *yaml.Node) (*reliantv1.StringInputConfig, error) {
	cfg := &reliantv1.StringInputConfig{
		Base:      unmarshalInputBase(node),
		Default:   unmarshalOptionalString(node, "default"),
		Pattern:   getYAMLFieldString(node, "pattern"),
		MinLength: unmarshalOptionalInt32(node, "min_length"),
		MaxLength: unmarshalOptionalInt32(node, "max_length"),
	}
	return cfg, nil
}

func unmarshalNumberInputConfig(node *yaml.Node) (*reliantv1.NumberInputConfig, error) {
	cfg := &reliantv1.NumberInputConfig{
		Base:    unmarshalInputBase(node),
		Default: unmarshalOptionalFloat64(node, "default"),
		Min:     unmarshalOptionalFloat64(node, "min"),
		Max:     unmarshalOptionalFloat64(node, "max"),
	}
	return cfg, nil
}

func unmarshalIntegerInputConfig(node *yaml.Node) (*reliantv1.IntegerInputConfig, error) {
	cfg := &reliantv1.IntegerInputConfig{
		Base:    unmarshalInputBase(node),
		Default: unmarshalOptionalInt64(node, "default"),
		Min:     unmarshalOptionalInt64(node, "min"),
		Max:     unmarshalOptionalInt64(node, "max"),
	}
	return cfg, nil
}

func unmarshalBooleanInputConfig(node *yaml.Node) (*reliantv1.BooleanInputConfig, error) {
	cfg := &reliantv1.BooleanInputConfig{
		Base:    unmarshalInputBase(node),
		Default: unmarshalOptionalBool(node, "default"),
	}
	return cfg, nil
}

func unmarshalEnumInputConfig(node *yaml.Node) (*reliantv1.EnumInputConfig, error) {
	cfg := &reliantv1.EnumInputConfig{
		Base: unmarshalInputBase(node),
	}
	// enum field (list of allowed values)
	enumNode := getYAMLField(node, "enum")
	if enumNode != nil {
		if err := enumNode.Decode(&cfg.EnumValues); err != nil {
			return nil, fmt.Errorf("enum.enum: %w", err)
		}
	}
	// multi
	multiNode := getYAMLField(node, "multi")
	if multiNode != nil {
		var m bool
		if err := multiNode.Decode(&m); err == nil {
			cfg.Multi = m
		}
	}
	// default (can be string or array)
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("enum.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

func unmarshalModelInputConfig(node *yaml.Node) (*reliantv1.ModelInputConfig, error) {
	cfg := &reliantv1.ModelInputConfig{
		Base: unmarshalInputBase(node),
	}
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		ms := &reliantv1.ModelSelector{}
		switch defaultNode.Kind {
		case yaml.MappingNode:
			if err := unmarshalModelSelector(defaultNode, ms); err != nil {
				return nil, fmt.Errorf("model.default: %w", err)
			}
		case yaml.ScalarNode:
			ms.Id = defaultNode.Value
		}
		cfg.Default = ms
	}
	// Also handle tags at the top level (shorthand: type: model, tags: [flagship])
	tagsNode := getYAMLField(node, "tags")
	if tagsNode != nil && cfg.Default == nil {
		cfg.Default = &reliantv1.ModelSelector{}
		if err := tagsNode.Decode(&cfg.Default.Tags); err != nil {
			return nil, fmt.Errorf("model.tags: %w", err)
		}
	} else if tagsNode != nil && cfg.Default != nil && len(cfg.Default.Tags) == 0 {
		if err := tagsNode.Decode(&cfg.Default.Tags); err != nil {
			return nil, fmt.Errorf("model.tags: %w", err)
		}
	}
	return cfg, nil
}

func unmarshalMessageInputConfig(node *yaml.Node) (*reliantv1.MessageInputConfig, error) {
	cfg := &reliantv1.MessageInputConfig{
		Base:    unmarshalInputBase(node),
		Default: unmarshalOptionalString(node, "default"),
	}
	return cfg, nil
}

func unmarshalAttachmentsInputConfig(node *yaml.Node) (*reliantv1.AttachmentsInputConfig, error) {
	cfg := &reliantv1.AttachmentsInputConfig{
		Base:     unmarshalInputBase(node),
		MinItems: unmarshalOptionalInt32(node, "min_items"),
		MaxItems: unmarshalOptionalInt32(node, "max_items"),
	}
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("attachments.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

func unmarshalToolsInputConfig(node *yaml.Node) (*reliantv1.ToolsInputConfig, error) {
	cfg := &reliantv1.ToolsInputConfig{
		Base: unmarshalInputBase(node),
	}
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("tools.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

func unmarshalArrayInputConfig(node *yaml.Node) (*reliantv1.ArrayInputConfig, error) {
	cfg := &reliantv1.ArrayInputConfig{
		Base:     unmarshalInputBase(node),
		MinItems: unmarshalOptionalInt32(node, "min_items"),
		MaxItems: unmarshalOptionalInt32(node, "max_items"),
	}
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("array.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

func unmarshalObjectInputConfig(node *yaml.Node) (*reliantv1.ObjectInputConfig, error) {
	cfg := &reliantv1.ObjectInputConfig{
		Base: unmarshalInputBase(node),
	}
	// properties
	propsNode := getYAMLField(node, "properties")
	if propsNode != nil && propsNode.Kind == yaml.MappingNode {
		cfg.Properties = make(map[string]*reliantv1.PropertySchema)
		for i := 0; i < len(propsNode.Content); i += 2 {
			key := propsNode.Content[i].Value
			val := propsNode.Content[i+1]
			ps, err := unmarshalPropertySchema(val)
			if err != nil {
				return nil, fmt.Errorf("object.properties.%s: %w", key, err)
			}
			cfg.Properties[key] = ps
		}
	}
	// required
	reqNode := getYAMLField(node, "required")
	if reqNode != nil {
		if err := reqNode.Decode(&cfg.Required); err != nil {
			return nil, fmt.Errorf("object.required: %w", err)
		}
	}
	// additional_properties
	cfg.AdditionalProperties = unmarshalOptionalBool(node, "additional_properties")
	// default
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("object.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

func unmarshalPropertySchema(node *yaml.Node) (*reliantv1.PropertySchema, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for property schema")
	}
	ps := &reliantv1.PropertySchema{}
	ps.Type = getYAMLFieldString(node, "type")
	ps.Description = getYAMLFieldString(node, "description")
	ps.Minimum = unmarshalOptionalFloat64(node, "minimum")
	ps.Maximum = unmarshalOptionalFloat64(node, "maximum")
	ps.MinLength = unmarshalOptionalInt32(node, "min_length")
	ps.MaxLength = unmarshalOptionalInt32(node, "max_length")
	// enum (as []*structpb.Value)
	enumNode := getYAMLField(node, "enum")
	if enumNode != nil && enumNode.Kind == yaml.SequenceNode {
		for _, item := range enumNode.Content {
			v, err := yamlNodeToStructValue(item)
			if err != nil {
				return nil, fmt.Errorf("property.enum: %w", err)
			}
			ps.EnumValues = append(ps.EnumValues, v)
		}
	}
	// required
	reqNode := getYAMLField(node, "required")
	if reqNode != nil {
		if err := reqNode.Decode(&ps.Required); err != nil {
			return nil, fmt.Errorf("property.required: %w", err)
		}
	}
	// items
	itemsNode := getYAMLField(node, "items")
	if itemsNode != nil {
		items, err := unmarshalPropertySchema(itemsNode)
		if err != nil {
			return nil, fmt.Errorf("property.items: %w", err)
		}
		ps.Items = items
	}
	// properties (nested)
	propsNode := getYAMLField(node, "properties")
	if propsNode != nil && propsNode.Kind == yaml.MappingNode {
		ps.Properties = make(map[string]*reliantv1.PropertySchema)
		for i := 0; i < len(propsNode.Content); i += 2 {
			key := propsNode.Content[i].Value
			val := propsNode.Content[i+1]
			nested, err := unmarshalPropertySchema(val)
			if err != nil {
				return nil, fmt.Errorf("property.properties.%s: %w", key, err)
			}
			ps.Properties[key] = nested
		}
	}
	return ps, nil
}

func unmarshalAnyInputConfig(node *yaml.Node) (*reliantv1.AnyInputConfig, error) {
	cfg := &reliantv1.AnyInputConfig{
		Base: unmarshalInputBase(node),
	}
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("any.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

func unmarshalGroupInputConfig(node *yaml.Node) (*reliantv1.GroupInputConfig, error) {
	cfg := &reliantv1.GroupInputConfig{
		Base: unmarshalInputBase(node),
	}
	// presets
	presetsNode := getYAMLField(node, "presets")
	if presetsNode != nil {
		cfg.Presets = &reliantv1.PresetsConfig{}
		cfg.Presets.Tag = getYAMLFieldString(presetsNode, "tag")
		cfg.Presets.Default = getYAMLFieldString(presetsNode, "default")
	}
	// inputs (nested input map)
	inputsNode := getYAMLField(node, "inputs")
	if inputsNode != nil && inputsNode.Kind == yaml.MappingNode {
		cfg.Inputs = make(map[string]*reliantv1.Input)
		for i := 0; i < len(inputsNode.Content); i += 2 {
			key := inputsNode.Content[i].Value
			val := inputsNode.Content[i+1]
			input, err := unmarshalInput(val)
			if err != nil {
				return nil, fmt.Errorf("group.inputs.%s: %w", key, err)
			}
			cfg.Inputs[key] = input
		}
	}
	return cfg, nil
}

func unmarshalPresetInputConfig(node *yaml.Node) (*reliantv1.PresetInputConfig, error) {
	cfg := &reliantv1.PresetInputConfig{
		Base: unmarshalInputBase(node),
	}
	// tags
	tagsNode := getYAMLField(node, "tags")
	if tagsNode != nil {
		if err := tagsNode.Decode(&cfg.Tags); err != nil {
			return nil, fmt.Errorf("preset.tags: %w", err)
		}
	}
	// multi
	multiNode := getYAMLField(node, "multi")
	if multiNode != nil {
		var m bool
		if err := multiNode.Decode(&m); err == nil {
			cfg.Multi = m
		}
	}
	// default
	defaultNode := getYAMLField(node, "default")
	if defaultNode != nil && defaultNode.Tag != "!!null" {
		v, err := unmarshalStructValue(defaultNode)
		if err != nil {
			return nil, fmt.Errorf("preset.default: %w", err)
		}
		cfg.Default = v
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Marshal V2Input
// ---------------------------------------------------------------------------

func marshalInput(input *reliantv1.Input) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}

	// type field
	m.Content = append(m.Content, scalarNode("type", ""), scalarNode(input.Type, ""))

	switch input.Type {
	case "string":
		if cfg := input.GetStringInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			marshalOptionalString(m, "default", cfg.Default)
			if cfg.Pattern != "" {
				m.Content = append(m.Content, scalarNode("pattern", ""), scalarNode(cfg.Pattern, ""))
			}
			marshalOptionalInt32(m, "min_length", cfg.MinLength)
			marshalOptionalInt32(m, "max_length", cfg.MaxLength)
		}
	case "number":
		if cfg := input.GetNumberInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			marshalOptionalFloat64(m, "default", cfg.Default)
			marshalOptionalFloat64(m, "min", cfg.Min)
			marshalOptionalFloat64(m, "max", cfg.Max)
		}
	case "integer":
		if cfg := input.GetIntegerInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			marshalOptionalInt64(m, "default", cfg.Default)
			marshalOptionalInt64(m, "min", cfg.Min)
			marshalOptionalInt64(m, "max", cfg.Max)
		}
	case "boolean":
		if cfg := input.GetBooleanInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			marshalOptionalBool(m, "default", cfg.Default)
		}
	case "enum":
		if cfg := input.GetEnumInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if len(cfg.EnumValues) > 0 {
				seq := marshalStringList(cfg.EnumValues)
				m.Content = append(m.Content, scalarNode("enum", ""), seq)
			}
			if cfg.Multi {
				m.Content = append(m.Content, scalarNode("multi", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: "true", Tag: "!!bool"})
			}
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
		}
	case "model":
		if cfg := input.GetModelInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if cfg.Default != nil {
				n, err := marshalModelSelector(cfg.Default)
				if err != nil {
					return nil, err
				}
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
		}
	case "message":
		if cfg := input.GetMessageInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			marshalOptionalString(m, "default", cfg.Default)
		}
	case "attachments":
		if cfg := input.GetAttachmentsInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
			marshalOptionalInt32(m, "min_items", cfg.MinItems)
			marshalOptionalInt32(m, "max_items", cfg.MaxItems)
		}
	case "tools":
		if cfg := input.GetToolsInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
		}
	case "array":
		if cfg := input.GetArrayInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
			marshalOptionalInt32(m, "min_items", cfg.MinItems)
			marshalOptionalInt32(m, "max_items", cfg.MaxItems)
		}
	case "object":
		if cfg := input.GetObjectInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if len(cfg.Properties) > 0 {
				propsNode := &yaml.Node{Kind: yaml.MappingNode}
				for k, v := range cfg.Properties {
					pn, err := marshalPropertySchema(v)
					if err != nil {
						return nil, err
					}
					propsNode.Content = append(propsNode.Content, scalarNode(k, ""), pn)
				}
				m.Content = append(m.Content, scalarNode("properties", ""), propsNode)
			}
			if len(cfg.Required) > 0 {
				m.Content = append(m.Content, scalarNode("required", ""), marshalStringList(cfg.Required))
			}
			marshalOptionalBool(m, "additional_properties", cfg.AdditionalProperties)
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
		}
	case "any":
		if cfg := input.GetAnyInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
		}
	case "group":
		if cfg := input.GetGroupInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if cfg.Presets != nil {
				pn := &yaml.Node{Kind: yaml.MappingNode}
				if cfg.Presets.Tag != "" {
					pn.Content = append(pn.Content, scalarNode("tag", ""), scalarNode(cfg.Presets.Tag, ""))
				}
				if cfg.Presets.Default != "" {
					pn.Content = append(pn.Content, scalarNode("default", ""), scalarNode(cfg.Presets.Default, ""))
				}
				m.Content = append(m.Content, scalarNode("presets", ""), pn)
			}
			if len(cfg.Inputs) > 0 {
				inputsNode, err := marshalInputMap(cfg.Inputs)
				if err != nil {
					return nil, err
				}
				m.Content = append(m.Content, scalarNode("inputs", ""), inputsNode)
			}
		}
	case "preset":
		if cfg := input.GetPresetInput(); cfg != nil {
			marshalInputBase(m, cfg.Base)
			if len(cfg.Tags) > 0 {
				m.Content = append(m.Content, scalarNode("tags", ""), marshalStringList(cfg.Tags))
			}
			if cfg.Multi {
				m.Content = append(m.Content, scalarNode("multi", ""), &yaml.Node{Kind: yaml.ScalarNode, Value: "true", Tag: "!!bool"})
			}
			if cfg.Default != nil {
				n := marshalStructpbValue(cfg.Default)
				m.Content = append(m.Content, scalarNode("default", ""), n)
			}
		}
	}

	return m, nil
}

func marshalInputMap(inputs map[string]*reliantv1.Input) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range sortedKeys(inputs) {
		n, err := marshalInput(inputs[k])
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", k, err)
		}
		m.Content = append(m.Content, scalarNode(k, ""), n)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Marshal helpers
// ---------------------------------------------------------------------------

func marshalInputBase(m *yaml.Node, base *reliantv1.InputBase) {
	if base == nil {
		return
	}
	if base.Description != "" {
		m.Content = append(m.Content, scalarNode("description", ""), scalarNode(base.Description, ""))
	}
	if base.Ui != "" {
		m.Content = append(m.Content, scalarNode("ui", ""), scalarNode(base.Ui, ""))
	}
}

func marshalOptionalString(m *yaml.Node, key string, val *string) {
	if val == nil {
		return
	}
	n := scalarNode(*val, "")
	// Ensure empty strings are quoted so they don't get parsed as null
	if *val == "" {
		n.Style = yaml.DoubleQuotedStyle
	}
	m.Content = append(m.Content, scalarNode(key, ""), n)
}

func marshalOptionalBool(m *yaml.Node, key string, val *bool) {
	if val == nil {
		return
	}
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool"}
	if *val {
		n.Value = "true"
	} else {
		n.Value = "false"
	}
	m.Content = append(m.Content, scalarNode(key, ""), n)
}

func marshalOptionalInt32(m *yaml.Node, key string, val *int32) {
	if val == nil {
		return
	}
	m.Content = append(m.Content, scalarNode(key, ""), &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: fmt.Sprintf("%d", *val),
		Tag:   "!!int",
	})
}

func marshalOptionalInt64(m *yaml.Node, key string, val *int64) {
	if val == nil {
		return
	}
	m.Content = append(m.Content, scalarNode(key, ""), &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: fmt.Sprintf("%d", *val),
		Tag:   "!!int",
	})
}

func marshalOptionalFloat64(m *yaml.Node, key string, val *float64) {
	if val == nil {
		return
	}
	m.Content = append(m.Content, scalarNode(key, ""), &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: fmt.Sprintf("%g", *val),
		Tag:   "!!float",
	})
}

func marshalStructpbValue(v *structpb.Value) *yaml.Node {
	var n yaml.Node
	raw := v.AsInterface()
	_ = n.Encode(raw)
	return &n
}

func marshalPropertySchema(ps *reliantv1.PropertySchema) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if ps.Type != "" {
		m.Content = append(m.Content, scalarNode("type", ""), scalarNode(ps.Type, ""))
	}
	if ps.Description != "" {
		m.Content = append(m.Content, scalarNode("description", ""), scalarNode(ps.Description, ""))
	}
	if len(ps.EnumValues) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, v := range ps.EnumValues {
			seq.Content = append(seq.Content, marshalStructpbValue(v))
		}
		m.Content = append(m.Content, scalarNode("enum", ""), seq)
	}
	marshalOptionalFloat64(m, "minimum", ps.Minimum)
	marshalOptionalFloat64(m, "maximum", ps.Maximum)
	marshalOptionalInt32(m, "min_length", ps.MinLength)
	marshalOptionalInt32(m, "max_length", ps.MaxLength)
	if len(ps.Required) > 0 {
		m.Content = append(m.Content, scalarNode("required", ""), marshalStringList(ps.Required))
	}
	if ps.Items != nil {
		itemsNode, err := marshalPropertySchema(ps.Items)
		if err != nil {
			return nil, err
		}
		m.Content = append(m.Content, scalarNode("items", ""), itemsNode)
	}
	if len(ps.Properties) > 0 {
		propsNode := &yaml.Node{Kind: yaml.MappingNode}
		for k, v := range ps.Properties {
			pn, err := marshalPropertySchema(v)
			if err != nil {
				return nil, err
			}
			propsNode.Content = append(propsNode.Content, scalarNode(k, ""), pn)
		}
		m.Content = append(m.Content, scalarNode("properties", ""), propsNode)
	}
	return m, nil
}
