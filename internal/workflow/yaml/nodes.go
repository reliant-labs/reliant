package wfyaml

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Descriptor-generated bindings
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Unmarshal V2Node
// ---------------------------------------------------------------------------

func unmarshalNode(node *yaml.Node) (*reliantv1.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("node: expected mapping, got kind %v", node.Kind)
	}

	v2node := &reliantv1.Node{}

	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]

		switch key {
		case yamlKeyID:
			v2node.Id = val.Value
		case yamlKeyType:
			v2node.Type = val.Value
		case yamlKeyCondition:
			dcb, err := unmarshalDirectCelBool(val)
			if err != nil {
				return nil, fmt.Errorf("node %s: %s: %w", v2node.Id, yamlKeyCondition, err)
			}
			v2node.Condition = dcb
		case yamlKeyTimeout:
			t, err := unmarshalCelString(val)
			if err != nil {
				return nil, fmt.Errorf("node %s: %s: %w", v2node.Id, yamlKeyTimeout, err)
			}
			v2node.Timeout = t
		case yamlKeySaveMessage:
			sm, err := unmarshalSaveMessageConfig(val)
			if err != nil {
				return nil, fmt.Errorf("node %s: %s: %w", v2node.Id, yamlKeySaveMessage, err)
			}
			v2node.SaveMessage = sm
		case yamlKeyDaemon:
			d, err := unmarshalCelDaemonSelector(val)
			if err != nil {
				return nil, fmt.Errorf("node %s: %s: %w", v2node.Id, yamlKeyDaemon, err)
			}
			v2node.Daemon = d
		}
	}

	if v2node.Type == "" {
		return v2node, nil
	}

	binding, hasBinding := generatedNodeBindingForType(v2node.Type)
	if !hasBinding {
		return v2node, nil
	}

	if binding.isStructural {
		if err := unmarshalStructuralArgs(node, v2node, binding); err != nil {
			return nil, fmt.Errorf("node %s (type=%s): %w", v2node.Id, v2node.Type, err)
		}
		return v2node, nil
	}

	if err := validateActivityNodeTopLevelKeys(node, binding); err != nil {
		return nil, fmt.Errorf("node %s (type=%s): %w", v2node.Id, v2node.Type, err)
	}
	argsNode, err := collectActivityArgsNode(node, binding)
	if err != nil {
		return nil, fmt.Errorf("node %s (type=%s): %w", v2node.Id, v2node.Type, err)
	}
	if argsNode != nil {
		if err := unmarshalActivityArgs(argsNode, v2node, binding); err != nil {
			return nil, fmt.Errorf("node %s (type=%s): %w", v2node.Id, v2node.Type, err)
		}
	} else {
		if err := setEmptyArgs(v2node, binding); err != nil {
			return nil, fmt.Errorf("node %s (type=%s): %w", v2node.Id, v2node.Type, err)
		}
	}

	return v2node, nil
}

// setEmptyArgs sets an empty args message for the node type using proto reflection.
func setEmptyArgs(v2node *reliantv1.Node, binding generatedNodeBinding) error {
	md := v2node.ProtoReflect().Descriptor()
	oneofDesc := md.Oneofs().ByName(protoreflect.Name(yamlKeyArgs))
	if oneofDesc == nil {
		return fmt.Errorf("V2Node has no 'args' oneof")
	}

	var fd protoreflect.FieldDescriptor
	for i := 0; i < oneofDesc.Fields().Len(); i++ {
		f := oneofDesc.Fields().Get(i)
		if f.Name() == binding.oneofFieldName {
			fd = f
			break
		}
	}
	if fd == nil {
		return fmt.Errorf("V2Node.args has no field %q", binding.oneofFieldName)
	}

	emptyMsg := v2node.ProtoReflect().NewField(fd).Message()
	v2node.ProtoReflect().Set(fd, protoreflect.ValueOfMessage(emptyMsg))
	return nil
}

// unmarshalActivityArgs decodes the args mapping into the correct oneof type.
func unmarshalActivityArgs(argsNode *yaml.Node, v2node *reliantv1.Node, binding generatedNodeBinding) error {
	if err := validateMappingKeys(argsNode, binding.argFieldKeys, "node.args"); err != nil {
		return err
	}
	return unmarshalArgsOneof(argsNode, v2node, binding.oneofFieldName, false)
}

// unmarshalStructuralArgs decodes inline fields for structural node types.
func unmarshalStructuralArgs(node *yaml.Node, v2node *reliantv1.Node, binding generatedNodeBinding) error {
	md := v2node.ProtoReflect().Descriptor()
	oneofDesc := md.Oneofs().ByName(protoreflect.Name(yamlKeyArgs))
	if oneofDesc == nil {
		return fmt.Errorf("V2Node has no 'args' oneof")
	}

	var fd protoreflect.FieldDescriptor
	for i := 0; i < oneofDesc.Fields().Len(); i++ {
		f := oneofDesc.Fields().Get(i)
		if f.Name() == binding.oneofFieldName {
			fd = f
			break
		}
	}
	if fd == nil {
		return fmt.Errorf("V2Node.args has no field %q", binding.oneofFieldName)
	}

	argsMsg := v2node.ProtoReflect().NewField(fd).Message().Interface()
	yamlMap, err := yamlMappingToMap(node, true)
	if err != nil {
		return err
	}

	hasInputArgsMapField := false
	if _, ok := binding.argFieldKeys[yamlKeyArgs]; ok {
		hasInputArgsMapField = true
	}
	inputArgsEntries := map[string]*yaml.Node{}

	if argsNode, hasArgsWrapper := yamlMap[yamlKeyArgs]; hasArgsWrapper {
		if argsNode.Kind != yaml.MappingNode {
			return fmt.Errorf("node.%s: expected mapping, got kind %v", yamlKeyArgs, argsNode.Kind)
		}
		for i := 0; i < len(argsNode.Content); i += 2 {
			childKey := argsNode.Content[i].Value
			childVal := argsNode.Content[i+1]
			if _, isStructuralArgField := binding.argFieldKeys[childKey]; isStructuralArgField && childKey != yamlKeyArgs {
				promoteStructuralKey := true
				if hasInputArgsMapField && (childKey == "thread" || childKey == "project") && childVal.Kind != yaml.MappingNode {
					promoteStructuralKey = false
				}
				if promoteStructuralKey {
					if _, exists := yamlMap[childKey]; exists {
						return fmt.Errorf("node: duplicate structural arg key %q across inline and %s", childKey, yamlKeyArgs)
					}
					yamlMap[childKey] = childVal
					continue
				}
			}
			if !hasInputArgsMapField {
				return fmt.Errorf("node.%s: unknown key %q", yamlKeyArgs, childKey)
			}
			inputArgsEntries[childKey] = childVal
		}
		delete(yamlMap, yamlKeyArgs)
	}

	for key, val := range yamlMap {
		if _, ok := binding.argFieldKeys[key]; ok {
			continue
		}
		if !hasInputArgsMapField {
			return fmt.Errorf("node: unknown structural arg key %q", key)
		}
		inputArgsEntries[key] = val
		delete(yamlMap, key)
	}

	if hasInputArgsMapField && len(inputArgsEntries) > 0 {
		inputArgsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for key, val := range inputArgsEntries {
			inputArgsNode.Content = append(inputArgsNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				val,
			)
		}
		yamlMap[yamlKeyArgs] = inputArgsNode
	}

	if err := unmarshalArgsGeneric(yamlMap, argsMsg); err != nil {
		return err
	}
	v2node.ProtoReflect().Set(fd, protoreflect.ValueOfMessage(argsMsg.ProtoReflect()))
	return nil
}

// unmarshalArgsOneof uses proto reflection to find the oneof field, create the
// args message, populate it from YAML, and set it on v2node.
// When structural is true, base-level node fields are skipped.
func unmarshalArgsOneof(node *yaml.Node, v2node *reliantv1.Node, oneofName protoreflect.Name, structural bool) error {
	md := v2node.ProtoReflect().Descriptor()

	// Find the "args" oneof descriptor.
	oneofDesc := md.Oneofs().ByName(protoreflect.Name(yamlKeyArgs))
	if oneofDesc == nil {
		return fmt.Errorf("V2Node has no 'args' oneof")
	}

	// Find the specific field within the oneof.
	var fd protoreflect.FieldDescriptor
	for i := 0; i < oneofDesc.Fields().Len(); i++ {
		f := oneofDesc.Fields().Get(i)
		if f.Name() == oneofName {
			fd = f
			break
		}
	}
	if fd == nil {
		return fmt.Errorf("V2Node.args has no field %q", oneofName)
	}

	// Create a new instance of the args message.
	argsMsg := v2node.ProtoReflect().NewField(fd).Message().Interface()

	// Build the YAML key->value map, skipping base fields for structural nodes.
	yamlMap, err := yamlMappingToMap(node, structural)
	if err != nil {
		return err
	}

	// Populate the args message fields from the YAML map.
	if err := unmarshalArgsGeneric(yamlMap, argsMsg); err != nil {
		return err
	}

	// Set the oneof field on v2node.
	v2node.ProtoReflect().Set(fd, protoreflect.ValueOfMessage(argsMsg.ProtoReflect()))
	return nil
}

// yamlMappingToMap extracts key->yaml.Node pairs from a mapping node.
// When skipBase is true, node base fields (id, type, condition, etc.) are skipped.
// The "args" key is NOT skipped because some proto messages (SubWorkflowArgs, LoopArgs)
// have an "args" field that carries sub-workflow inputs.
func yamlMappingToMap(node *yaml.Node, skipBase bool) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping, got kind %v", node.Kind)
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if skipBase && generatedHasNodeBaseFieldKey(key) {
			continue
		}
		result[key] = node.Content[i+1]
	}
	return result, nil
}

func validateActivityNodeTopLevelKeys(node *yaml.Node, binding generatedNodeBinding) error {
	allowed := map[string]struct{}{
		yamlKeyID:          {},
		yamlKeyType:        {},
		yamlKeyCondition:   {},
		yamlKeyTimeout:     {},
		yamlKeySaveMessage: {},
		yamlKeyDaemon:      {},
		yamlKeyArgs:        {},
	}
	for key := range binding.argFieldKeys {
		allowed[key] = struct{}{}
	}
	return validateMappingKeys(node, allowed, "node")
}

func collectActivityArgsNode(node *yaml.Node, binding generatedNodeBinding) (*yaml.Node, error) {
	argsNode := getYAMLField(node, yamlKeyArgs)
	inlineArgs := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key == yamlKeyArgs || generatedHasNodeBaseFieldKey(key) {
			continue
		}
		if _, isArgKey := binding.argFieldKeys[key]; !isArgKey {
			continue
		}
		inlineArgs[key] = node.Content[i+1]
	}

	if argsNode == nil {
		if len(inlineArgs) == 0 {
			return nil, nil
		}
		collected := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for key, value := range inlineArgs {
			collected.Content = append(collected.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				value,
			)
		}
		return collected, nil
	}

	if argsNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("node.%s: expected mapping, got kind %v", yamlKeyArgs, argsNode.Kind)
	}
	for i := 0; i < len(argsNode.Content); i += 2 {
		key := argsNode.Content[i].Value
		if _, isArgKey := binding.argFieldKeys[key]; !isArgKey {
			return nil, fmt.Errorf("node.%s: unknown key %q", yamlKeyArgs, key)
		}
		if _, duplicated := inlineArgs[key]; duplicated {
			return nil, fmt.Errorf("node: duplicate arg key %q across inline and %s", key, yamlKeyArgs)
		}
	}
	for key, value := range inlineArgs {
		argsNode.Content = append(argsNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			value,
		)
	}
	return argsNode, nil
}

func validateMappingKeys(node *yaml.Node, allowed map[string]struct{}, path string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected mapping, got kind %v", path, node.Kind)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s: unknown key %q", path, key)
		}
	}
	return nil
}

// celWrapperTypeMap maps CelX wrapper full names to their type for dispatch.
var celWrapperTypeMap = map[protoreflect.FullName]string{
	"reliant.v1.CelString":        "CelString",
	"reliant.v1.CelBool":          "CelBool",
	"reliant.v1.CelDouble":        "CelDouble",
	"reliant.v1.CelInt":           "CelInt",
	"reliant.v1.CelStringList":    "CelStringList",
	"reliant.v1.CelModelSelector": "CelModelSelector",
	"reliant.v1.DirectCelBool":    "DirectCelBool",
}

// unmarshalArgsGeneric populates a proto message from a YAML key->node map
// using proto reflection. Handles CelX wrappers, nested messages, repeated fields, and maps.
func unmarshalArgsGeneric(yamlMap map[string]*yaml.Node, msg proto.Message) error {
	md := msg.ProtoReflect().Descriptor()
	rv := msg.ProtoReflect()

	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		fieldName := string(fd.Name())

		yamlVal, ok := yamlMap[fieldName]
		if !ok {
			continue
		}

		if err := setProtoFieldFromYAML(rv, fd, yamlVal); err != nil {
			return fmt.Errorf("%s: %w", fieldName, err)
		}
	}
	return nil
}

// setProtoFieldFromYAML sets a single proto field from a YAML node.
func setProtoFieldFromYAML(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	// Handle map fields.
	if fd.IsMap() {
		return setMapField(rv, fd, node)
	}

	// Handle repeated (non-map) fields.
	if fd.IsList() {
		return setRepeatedField(rv, fd, node)
	}

	// Handle message fields (CelX wrappers and nested messages).
	if fd.Kind() == protoreflect.MessageKind {
		return setMessageField(rv, fd, node)
	}

	// Handle scalar fields.
	return setScalarField(rv, fd, node)
}

// setMessageField handles singular message-typed proto fields.
func setMessageField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	msgFullName := fd.Message().FullName()

	// Check for CelX wrapper types.
	if wrapperType, ok := celWrapperTypeMap[msgFullName]; ok {
		return setCelWrapperField(rv, fd, node, wrapperType)
	}

	// Non-CelX message types — dispatch by full name.
	switch msgFullName {
	case protoMessageFullNameWorkflow:
		wf, err := unmarshalWorkflow(node)
		if err != nil {
			return err
		}
		rv.Set(fd, protoreflect.ValueOfMessage(wf.ProtoReflect()))
		return nil

	case protoMessageFullNameResponseTool:
		rt, err := unmarshalResponseTool(node)
		if err != nil {
			return err
		}
		rv.Set(fd, protoreflect.ValueOfMessage(rt.ProtoReflect()))
		return nil

	case protoMessageFullNameProjectConfig:
		pc, err := unmarshalProjectConfig(node)
		if err != nil {
			return err
		}
		rv.Set(fd, protoreflect.ValueOfMessage(pc.ProtoReflect()))
		return nil

	case protoMessageFullNameStruct:
		s, err := unmarshalStruct(node)
		if err != nil {
			return err
		}
		rv.Set(fd, protoreflect.ValueOfMessage(s.ProtoReflect()))
		return nil

	default:
		// For other message types, try recursive generic unmarshal.
		newMsg := rv.NewField(fd).Message().Interface()
		subMap, err := yamlMappingToMap(node, false)
		if err != nil {
			return err
		}
		if err := unmarshalArgsGeneric(subMap, newMsg); err != nil {
			return err
		}
		rv.Set(fd, protoreflect.ValueOfMessage(newMsg.ProtoReflect()))
		return nil
	}
}

// setCelWrapperField sets a CelX wrapper field from a YAML node.
func setCelWrapperField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node, wrapperType string) error {
	var val proto.Message
	var err error

	switch wrapperType {
	case "CelString":
		val, err = unmarshalCelString(node)
	case "CelBool":
		val, err = unmarshalCelBool(node)
	case "CelDouble":
		val, err = unmarshalCelDouble(node)
	case "CelInt":
		val, err = unmarshalCelInt(node)
	case "CelStringList":
		val, err = unmarshalCelStringList(node)
	case "CelModelSelector":
		val, err = unmarshalCelModelSelector(node)
	case "DirectCelBool":
		val, err = unmarshalDirectCelBool(node)
	default:
		return fmt.Errorf("unknown CelX wrapper type %q", wrapperType)
	}

	if err != nil {
		return err
	}
	// Use reflect to check for typed nil pointers (e.g. (*CelString)(nil))
	// which pass a simple `val == nil` check due to Go's interface nil semantics.
	if val == nil || reflect.ValueOf(val).IsNil() {
		return nil
	}
	rv.Set(fd, protoreflect.ValueOfMessage(val.ProtoReflect()))
	return nil
}

// setRepeatedField handles repeated (non-map) fields.
func setRepeatedField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	// repeated string — handle string-or-string-list pattern.
	if fd.Kind() == protoreflect.StringKind {
		strs, err := unmarshalStringOrStringList(node)
		if err != nil {
			return err
		}
		list := rv.Mutable(fd).List()
		for _, s := range strs {
			list.Append(protoreflect.ValueOfString(s))
		}
		return nil
	}

	// repeated message — handle sequence of nested messages.
	if fd.Kind() == protoreflect.MessageKind {
		return setRepeatedMessageField(rv, fd, node)
	}

	return fmt.Errorf("unsupported repeated field kind %v", fd.Kind())
}

// setRepeatedMessageField handles repeated message fields.
func setRepeatedMessageField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("expected sequence, got kind %v", node.Kind)
	}

	list := rv.Mutable(fd).List()
	for _, item := range node.Content {
		newMsg := list.NewElement().Message().Interface()
		subMap, err := yamlMappingToMap(item, false)
		if err != nil {
			return err
		}
		if err := unmarshalArgsGeneric(subMap, newMsg); err != nil {
			return err
		}
		list.Append(protoreflect.ValueOfMessage(newMsg.ProtoReflect()))
	}
	return nil
}

// setMapField handles map fields.
func setMapField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping for map field, got kind %v", node.Kind)
	}

	valDesc := fd.MapValue()
	mapVal := rv.Mutable(fd).Map()

	for i := 0; i < len(node.Content); i += 2 {
		keyStr := node.Content[i].Value
		valNode := node.Content[i+1]

		key := protoreflect.ValueOfString(keyStr).MapKey()

		switch valDesc.Kind() {
		case protoreflect.StringKind:
			mapVal.Set(key, protoreflect.ValueOfString(valNode.Value))

		case protoreflect.MessageKind:
			// Check if value is structpb.Value.
			if valDesc.Message().FullName() == "google.protobuf.Value" {
				v, err := yamlNodeToStructValue(valNode)
				if err != nil {
					return fmt.Errorf("map value %q: %w", keyStr, err)
				}
				mapVal.Set(key, protoreflect.ValueOfMessage(v.ProtoReflect()))
			} else if valDesc.Message().FullName() == protoMessageFullNameStruct {
				s, err := unmarshalStruct(valNode)
				if err != nil {
					return fmt.Errorf("map value %q: %w", keyStr, err)
				}
				mapVal.Set(key, protoreflect.ValueOfMessage(s.ProtoReflect()))
			} else {
				return fmt.Errorf("unsupported map value message type %q", valDesc.Message().FullName())
			}

		default:
			return fmt.Errorf("unsupported map value kind %v", valDesc.Kind())
		}
	}
	return nil
}

// setScalarField handles scalar (non-message, non-repeated, non-map) proto fields.
func setScalarField(rv protoreflect.Message, fd protoreflect.FieldDescriptor, node *yaml.Node) error {
	switch fd.Kind() {
	case protoreflect.StringKind:
		rv.Set(fd, protoreflect.ValueOfString(node.Value))
	case protoreflect.BoolKind:
		var b bool
		if err := node.Decode(&b); err != nil {
			return fmt.Errorf("cannot decode bool: %w", err)
		}
		rv.Set(fd, protoreflect.ValueOfBool(b))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		var i int32
		if err := node.Decode(&i); err != nil {
			return fmt.Errorf("cannot decode int32: %w", err)
		}
		rv.Set(fd, protoreflect.ValueOfInt32(i))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		var i int64
		if err := node.Decode(&i); err != nil {
			return fmt.Errorf("cannot decode int64: %w", err)
		}
		rv.Set(fd, protoreflect.ValueOfInt64(i))
	case protoreflect.FloatKind:
		var f float32
		if err := node.Decode(&f); err != nil {
			return fmt.Errorf("cannot decode float: %w", err)
		}
		rv.Set(fd, protoreflect.ValueOfFloat32(f))
	case protoreflect.DoubleKind:
		var f float64
		if err := node.Decode(&f); err != nil {
			return fmt.Errorf("cannot decode double: %w", err)
		}
		rv.Set(fd, protoreflect.ValueOfFloat64(f))
	default:
		return fmt.Errorf("unsupported scalar kind %v", fd.Kind())
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helper unmarshalers for nested types
// ---------------------------------------------------------------------------

func unmarshalSaveMessageConfig(node *yaml.Node) (*reliantv1.SaveMessageConfig, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for save_message config")
	}
	sm := &reliantv1.SaveMessageConfig{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error
		switch key {
		case yamlKeyCondition:
			sm.Condition, err = unmarshalCelString(val)
		case "role":
			sm.Role, err = unmarshalCelString(val)
		case "content":
			sm.Content, err = unmarshalCelString(val)
		case "tool_calls":
			sm.ToolCalls, err = unmarshalCelString(val)
		case "tool_results":
			sm.ToolResults, err = unmarshalCelString(val)
		case "attachments":
			sm.Attachments, err = unmarshalCelString(val)
		case "display_style":
			sm.DisplayStyle, err = unmarshalCelString(val)
		}
		if err != nil {
			return nil, fmt.Errorf("save_message.%s: %w", key, err)
		}
	}
	return sm, nil
}

func unmarshalProjectConfig(node *yaml.Node) (*reliantv1.ProjectConfig, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for project config")
	}
	pc := &reliantv1.ProjectConfig{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error
		switch key {
		case "path":
			pc.Path, err = unmarshalCelString(val)
		}
		if err != nil {
			return nil, fmt.Errorf("project.%s: %w", key, err)
		}
	}
	return pc, nil
}

func unmarshalResponseTool(node *yaml.Node) (*reliantv1.ResponseTool, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping for response_tool")
	}
	rt := &reliantv1.ResponseTool{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		var err error
		switch key {
		case "name":
			rt.Name, err = unmarshalCelString(val)
		case "description":
			rt.Description, err = unmarshalCelString(val)
		case "schema":
			// Schema can be a mapping (literal JSON Schema) or a scalar string (CEL expression).
			if val.Kind == yaml.MappingNode {
				s, err := unmarshalStruct(val)
				if err != nil {
					return nil, fmt.Errorf("response_tool.schema: %w", err)
				}
				rt.Schema = s
			} else if val.Kind == yaml.ScalarNode && isInterpolatedCEL(val.Value) {
				// CEL expression — store as sentinel Struct so post-resolution can evaluate it.
				// ResolveCELFields skips google.protobuf.Struct fields, so we use a known key
				// to carry the expression through to EvaluateNodeConfig post-processing.
				s, _ := structpb.NewStruct(map[string]interface{}{
					"__cel_expr__": val.Value,
				})
				rt.Schema = s
			}
		}
		if err != nil {
			return nil, fmt.Errorf("response_tool.%s: %w", key, err)
		}
	}
	return rt, nil
}

// unmarshalStringOrStringList handles fields that accept a single string or []string.
func unmarshalStringOrStringList(node *yaml.Node) ([]string, error) {
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

// unmarshalStruct decodes a YAML mapping into *structpb.Struct.
func unmarshalStruct(node *yaml.Node) (*structpb.Struct, error) {
	// Decode via JSON round-trip for complex nested structures
	var raw interface{}
	if err := node.Decode(&raw); err != nil {
		return nil, err
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("expected mapping for struct, got %T", raw)
	}
	return structpb.NewStruct(m)
}

// yamlNodeToStructValue converts a yaml.Node to a *structpb.Value.
func yamlNodeToStructValue(node *yaml.Node) (*structpb.Value, error) {
	var raw interface{}
	if err := node.Decode(&raw); err != nil {
		return nil, err
	}
	return interfaceToStructValue(raw)
}

func interfaceToStructValue(v interface{}) (*structpb.Value, error) {
	switch val := v.(type) {
	case nil:
		return structpb.NewNullValue(), nil
	case bool:
		return structpb.NewBoolValue(val), nil
	case int:
		return structpb.NewNumberValue(float64(val)), nil
	case int64:
		return structpb.NewNumberValue(float64(val)), nil
	case float64:
		return structpb.NewNumberValue(val), nil
	case string:
		return structpb.NewStringValue(val), nil
	case []interface{}:
		var items []*structpb.Value
		for _, item := range val {
			sv, err := interfaceToStructValue(item)
			if err != nil {
				return nil, err
			}
			items = append(items, sv)
		}
		return structpb.NewListValue(&structpb.ListValue{Values: items}), nil
	case map[string]interface{}:
		s, err := structpb.NewStruct(val)
		if err != nil {
			return nil, err
		}
		return structpb.NewStructValue(s), nil
	default:
		// Fallback: JSON round-trip
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return structpb.NewStringValue(string(b)), nil
	}
}

// ---------------------------------------------------------------------------
// Marshal V2Node
// ---------------------------------------------------------------------------

func marshalNode(v2node *reliantv1.Node) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}

	// id and type
	m.Content = append(m.Content, scalarNode(yamlKeyID, ""), scalarNode(v2node.Id, ""))
	m.Content = append(m.Content, scalarNode(yamlKeyType, ""), scalarNode(v2node.Type, ""))

	// condition
	if v2node.Condition != nil && v2node.Condition.Expr != "" {
		cn, _ := marshalDirectCelBool(v2node.Condition)
		if cn != nil {
			m.Content = append(m.Content, scalarNode(yamlKeyCondition, ""), cn)
		}
	}

	// thread is now in SubWorkflowArgs, serialized by marshalStructuralArgsGeneric

	// timeout
	if v2node.Timeout != nil {
		tn, _ := marshalCelString(v2node.Timeout)
		if tn != nil {
			m.Content = append(m.Content, scalarNode(yamlKeyTimeout, ""), tn)
		}
	}

	// save_message (node base level)
	if v2node.SaveMessage != nil {
		smn, err := marshalSaveMessageConfig(v2node.SaveMessage)
		if err != nil {
			return nil, err
		}
		if smn != nil {
			m.Content = append(m.Content, scalarNode(yamlKeySaveMessage, ""), smn)
		}
	}

	// daemon (node base level)
	if v2node.Daemon != nil {
		dn, _ := marshalCelDaemonSelector(v2node.Daemon)
		if dn != nil {
			m.Content = append(m.Content, scalarNode(yamlKeyDaemon, ""), dn)
		}
	}

	// Marshal args based on generated descriptor binding.
	binding, hasBinding := generatedNodeBindingForType(v2node.Type)
	if hasBinding {
		if binding.isStructural {
			argsNodes, err := marshalStructuralArgsGeneric(v2node)
			if err != nil {
				return nil, err
			}
			m.Content = append(m.Content, argsNodes...)
		} else {
			argsNode, err := marshalActivityArgsGeneric(v2node)
			if err != nil {
				return nil, err
			}
			if argsNode != nil && len(argsNode.Content) > 0 {
				m.Content = append(m.Content, scalarNode(yamlKeyArgs, ""), argsNode)
			}
		}
	}

	return m, nil
}

// ---------------------------------------------------------------------------
// Generic args marshaling via proto reflection
// ---------------------------------------------------------------------------

// marshalActivityArgsGeneric marshals the args oneof field as a YAML mapping node.
func marshalActivityArgsGeneric(v2node *reliantv1.Node) (*yaml.Node, error) {
	argsMsg := getPopulatedArgsMessage(v2node)
	if argsMsg == nil {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	return marshalProtoMessageToYAMLMapping(argsMsg)
}

// marshalStructuralArgsGeneric marshals the args oneof field as inline key-value pairs.
func marshalStructuralArgsGeneric(v2node *reliantv1.Node) ([]*yaml.Node, error) {
	argsMsg := getPopulatedArgsMessage(v2node)
	if argsMsg == nil {
		return nil, nil
	}
	mapping, err := marshalProtoMessageToYAMLMapping(argsMsg)
	if err != nil {
		return nil, err
	}
	return mapping.Content, nil
}

// getPopulatedArgsMessage returns the populated args message from V2Node's oneof,
// or nil if no args field is set.
func getPopulatedArgsMessage(v2node *reliantv1.Node) proto.Message {
	rv := v2node.ProtoReflect()
	md := rv.Descriptor()
	oneofDesc := md.Oneofs().ByName(protoreflect.Name(yamlKeyArgs))
	if oneofDesc == nil {
		return nil
	}
	fd := rv.WhichOneof(oneofDesc)
	if fd == nil {
		return nil
	}
	return rv.Get(fd).Message().Interface()
}

// marshalProtoMessageToYAMLMapping marshals a proto message to a YAML mapping node
// using proto reflection. Handles CelX wrappers, nested messages, repeated fields,
// map fields, and scalars. Skips fields marked hidden in (reliant) annotations.
func marshalProtoMessageToYAMLMapping(msg proto.Message) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	rv := msg.ProtoReflect()
	md := rv.Descriptor()

	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)

		// Skip fields that are not set / at default value.
		// Note: We do NOT skip hidden fields here. The (reliant) hidden annotation
		// is for UI schema generation, not YAML serialization. Some hidden fields
		// (e.g., messages, expected_response_tools) are user-authored YAML content
		// that must round-trip. Truly runtime-only hidden fields (resolved_*, etc.)
		// won't be set after YAML parsing, so they're naturally skipped by the
		// rv.Has(fd) check below.
		if !rv.Has(fd) {
			continue
		}

		fieldName := string(fd.Name())
		val := rv.Get(fd)

		yamlVal, err := marshalProtoFieldToYAML(fd, val)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fieldName, err)
		}
		if yamlVal == nil {
			continue
		}

		m.Content = append(m.Content, scalarNode(fieldName, ""), yamlVal)
	}

	return m, nil
}

// marshalProtoFieldToYAML converts a proto field value to a YAML node.
func marshalProtoFieldToYAML(fd protoreflect.FieldDescriptor, val protoreflect.Value) (*yaml.Node, error) {
	// Handle map fields.
	if fd.IsMap() {
		return marshalMapFieldToYAML(fd, val)
	}

	// Handle repeated (non-map) fields.
	if fd.IsList() {
		return marshalRepeatedFieldToYAML(fd, val)
	}

	// Handle message fields.
	if fd.Kind() == protoreflect.MessageKind {
		return marshalMessageFieldToYAML(fd, val)
	}

	// Handle scalar fields.
	return marshalScalarFieldToYAML(fd, val)
}

// marshalMessageFieldToYAML handles singular message-typed fields.
func marshalMessageFieldToYAML(fd protoreflect.FieldDescriptor, val protoreflect.Value) (*yaml.Node, error) {
	msgVal := val.Message().Interface()
	msgFullName := fd.Message().FullName()

	// CelX wrappers — extract the YAML representation.
	if _, ok := celWrapperTypeMap[msgFullName]; ok {
		return marshalCelWrapperToYAML(msgFullName, msgVal)
	}

	// Special message types.
	switch msgFullName {
	case protoMessageFullNameWorkflow:
		wf, ok := msgVal.(*reliantv1.Workflow)
		if !ok {
			return nil, fmt.Errorf("expected *Workflow")
		}
		return marshalWorkflow(wf)

	case protoMessageFullNameResponseTool:
		rt, ok := msgVal.(*reliantv1.ResponseTool)
		if !ok {
			return nil, fmt.Errorf("expected *ResponseTool")
		}
		return marshalResponseTool(rt)

	case protoMessageFullNameProjectConfig:
		pc, ok := msgVal.(*reliantv1.ProjectConfig)
		if !ok {
			return nil, fmt.Errorf("expected *ProjectConfig")
		}
		return marshalProjectConfig(pc)

	case protoMessageFullNameStruct:
		s, ok := msgVal.(*structpb.Struct)
		if !ok {
			return nil, fmt.Errorf("expected *structpb.Struct")
		}
		return marshalStructPb(s)

	default:
		// Generic nested message — recurse.
		return marshalProtoMessageToYAMLMapping(msgVal)
	}
}

// marshalCelWrapperToYAML marshals a CelX wrapper proto message to its YAML representation.
func marshalCelWrapperToYAML(fullName protoreflect.FullName, msg proto.Message) (*yaml.Node, error) {
	switch fullName {
	case "reliant.v1.CelString":
		return marshalCelString(msg.(*reliantv1.CelString))
	case "reliant.v1.CelBool":
		return marshalCelBool(msg.(*reliantv1.CelBool))
	case "reliant.v1.CelDouble":
		return marshalCelDouble(msg.(*reliantv1.CelDouble))
	case "reliant.v1.CelInt":
		return marshalCelInt(msg.(*reliantv1.CelInt))
	case "reliant.v1.CelStringList":
		return marshalCelStringList(msg.(*reliantv1.CelStringList))
	case "reliant.v1.CelModelSelector":
		return marshalCelModelSelector(msg.(*reliantv1.CelModelSelector))
	case "reliant.v1.DirectCelBool":
		return marshalDirectCelBool(msg.(*reliantv1.DirectCelBool))
	default:
		return nil, fmt.Errorf("unknown CelX wrapper type %q", fullName)
	}
}

// marshalRepeatedFieldToYAML handles repeated (non-map) fields.
func marshalRepeatedFieldToYAML(fd protoreflect.FieldDescriptor, val protoreflect.Value) (*yaml.Node, error) {
	list := val.List()
	if list.Len() == 0 {
		return nil, nil
	}

	// repeated string — use flow-style sequence.
	if fd.Kind() == protoreflect.StringKind {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for i := 0; i < list.Len(); i++ {
			seq.Content = append(seq.Content, scalarNode(list.Get(i).String(), ""))
		}
		return seq, nil
	}

	// repeated message — sequence of nested messages.
	if fd.Kind() == protoreflect.MessageKind {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for i := 0; i < list.Len(); i++ {
			elemMsg := list.Get(i).Message().Interface()
			elemNode, err := marshalProtoMessageToYAMLMapping(elemMsg)
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, elemNode)
		}
		return seq, nil
	}

	return nil, fmt.Errorf("unsupported repeated field kind %v", fd.Kind())
}

// marshalMapFieldToYAML handles map fields.
func marshalMapFieldToYAML(fd protoreflect.FieldDescriptor, val protoreflect.Value) (*yaml.Node, error) {
	mapVal := val.Map()
	if mapVal.Len() == 0 {
		return nil, nil
	}

	m := &yaml.Node{Kind: yaml.MappingNode}
	valDesc := fd.MapValue()

	mapVal.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		keyStr := k.String()

		switch valDesc.Kind() {
		case protoreflect.StringKind:
			m.Content = append(m.Content, scalarNode(keyStr, ""), scalarNode(v.String(), ""))

		case protoreflect.MessageKind:
			msgFullName := valDesc.Message().FullName()
			switch msgFullName {
			case "google.protobuf.Value":
				pbVal, ok := v.Message().Interface().(*structpb.Value)
				if ok {
					var valNode yaml.Node
					_ = valNode.Encode(pbVal.AsInterface())
					m.Content = append(m.Content, scalarNode(keyStr, ""), &valNode)
				}
			case protoMessageFullNameStruct:
				pbStruct, ok := v.Message().Interface().(*structpb.Struct)
				if ok {
					n, err := marshalStructPb(pbStruct)
					if err == nil {
						m.Content = append(m.Content, scalarNode(keyStr, ""), n)
					}
				}
			}

		default:
			// Unsupported map value type — skip silently.
		}
		return true
	})

	return m, nil
}

// marshalScalarFieldToYAML handles scalar (non-message, non-repeated, non-map) fields.
func marshalScalarFieldToYAML(fd protoreflect.FieldDescriptor, val protoreflect.Value) (*yaml.Node, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		s := val.String()
		if s == "" {
			return nil, nil
		}
		n := scalarNode(s, "")
		if len(s) > 80 {
			n.Style = yaml.LiteralStyle
		}
		return n, nil

	case protoreflect.BoolKind:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool"}
		if val.Bool() {
			n.Value = "true"
		} else {
			n.Value = "false"
		}
		return n, nil

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		s := strconv.FormatInt(val.Int(), 10)
		return &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!int"}, nil

	case protoreflect.FloatKind:
		s := strconv.FormatFloat(val.Float(), 'f', -1, 32)
		return &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!float"}, nil

	case protoreflect.DoubleKind:
		s := strconv.FormatFloat(val.Float(), 'f', -1, 64)
		return &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!float"}, nil

	default:
		return nil, fmt.Errorf("unsupported scalar kind %v for marshal", fd.Kind())
	}
}

// ---------------------------------------------------------------------------
// Marshal helpers for nested types
// ---------------------------------------------------------------------------

func marshalCelStringField(m *yaml.Node, key string, cs *reliantv1.CelString) {
	if cs == nil {
		return
	}
	n, _ := marshalCelString(cs)
	if n != nil {
		m.Content = append(m.Content, scalarNode(key, ""), n)
	}
}

func marshalStringList(strs []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, s := range strs {
		seq.Content = append(seq.Content, scalarNode(s, ""))
	}
	return seq
}

func marshalSaveMessageConfig(sm *reliantv1.SaveMessageConfig) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	marshalCelStringField(m, yamlKeyCondition, sm.Condition)
	marshalCelStringField(m, "role", sm.Role)
	marshalCelStringField(m, "content", sm.Content)
	marshalCelStringField(m, "tool_calls", sm.ToolCalls)
	marshalCelStringField(m, "tool_results", sm.ToolResults)
	marshalCelStringField(m, "attachments", sm.Attachments)
	marshalCelStringField(m, "display_style", sm.DisplayStyle)
	return m, nil
}

func marshalProjectConfig(pc *reliantv1.ProjectConfig) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	marshalCelStringField(m, "path", pc.Path)
	return m, nil
}

func marshalResponseTool(rt *reliantv1.ResponseTool) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	marshalCelStringField(m, "name", rt.Name)
	marshalCelStringField(m, "description", rt.Description)
	if rt.Schema != nil {
		n, err := marshalStructPb(rt.Schema)
		if err != nil {
			return nil, err
		}
		m.Content = append(m.Content, scalarNode("schema", ""), n)
	}
	return m, nil
}

func marshalStructPb(s *structpb.Struct) (*yaml.Node, error) {
	m := s.AsMap()
	var n yaml.Node
	if err := n.Encode(m); err != nil {
		return nil, err
	}
	return &n, nil
}
