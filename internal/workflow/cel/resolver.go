package wfcel

import (
	"encoding/json"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

// CELEvaluator evaluates CEL expressions against a runtime context.
type CELEvaluator interface {
	// EvalString evaluates a {{expr}} template string, returning the native result.
	// For pure expressions like "{{inputs.model}}", returns the native type.
	// For mixed strings like "hello {{name}}", returns a string.
	EvalString(expr string) (interface{}, error)

	// EvalBool evaluates a direct CEL expression (no {{ }}) as a boolean.
	EvalBool(expr string) (bool, error)
}

// celWrapperNames is the set of known CelX wrapper message full names.
var celWrapperNames = map[protoreflect.FullName]bool{
	"reliant.v1.CelString":        true,
	"reliant.v1.CelBool":          true,
	"reliant.v1.CelDouble":        true,
	"reliant.v1.CelInt":           true,
	"reliant.v1.CelStringList":    true,
	"reliant.v1.CelModelSelector": true,
	"reliant.v1.DirectCelBool":    true,
}

// isCelWrapper reports whether the given message descriptor is a CelX wrapper type.
func isCelWrapper(md protoreflect.MessageDescriptor) bool {
	return celWrapperNames[md.FullName()]
}

// ResolveCELFields walks a proto message, finds all CelX wrapper fields with
// expr set, evaluates them via the CELEvaluator, and sets the literal value.
// Returns a deep copy with expressions resolved. The original message is not modified.
func ResolveCELFields(msg proto.Message, eval CELEvaluator) (proto.Message, error) {
	cloned := proto.Clone(msg)
	if err := resolveMessage(cloned.ProtoReflect(), eval, ""); err != nil {
		return nil, err
	}
	return cloned, nil
}

// resolveMessage walks a protoreflect.Message and resolves any CelX wrapper fields.
func resolveMessage(m protoreflect.Message, eval CELEvaluator, path string) error {
	var errs []error

	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		fieldPath := appendPath(path, string(fd.Name()))

		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true // skip scalars, enums, etc.
		}

		if fd.IsList() {
			if err := resolveRepeatedField(m, fd, v, eval, fieldPath); err != nil {
				errs = append(errs, err)
			}
			return true
		}

		if fd.IsMap() {
			if err := resolveMapField(m, fd, v, eval, fieldPath); err != nil {
				errs = append(errs, err)
			}
			return true
		}

		// Singular message field
		msgVal := v.Message()
		md := msgVal.Descriptor()

		if isCelWrapper(md) {
			if err := resolveCelField(m, fd, msgVal, eval, fieldPath); err != nil {
				errs = append(errs, err)
			}
			return true
		}

		// Not a CelX wrapper — recurse into sub-message
		if err := resolveMessage(msgVal, eval, fieldPath); err != nil {
			errs = append(errs, err)
		}
		return true
	})

	// Also check oneof fields that are set but might contain message types.
	// Range() already visits set oneof fields, so this is handled above.

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// resolveCelField resolves a single CelX wrapper field.
func resolveCelField(
	parent protoreflect.Message,
	fd protoreflect.FieldDescriptor,
	celMsg protoreflect.Message,
	eval CELEvaluator,
	path string,
) error {
	md := celMsg.Descriptor()
	fullName := md.FullName()

	// DirectCelBool: conditions evaluated separately by the engine — skip.
	if fullName == "reliant.v1.DirectCelBool" {
		return nil
	}

	// Inline save_message fields are evaluated later during post-activity handling
	// when output.* namespace is available. Skip early resolution in node config.
	// Paths may be nested (e.g. "workflow.inline.nodes[0].save_message.content").
	if strings.HasPrefix(path, "save_message.") || strings.Contains(path, ".save_message.") {
		return nil
	}

	// All other CelX wrappers have a oneof "value" with cases "literal" and "expr".
	oneofDesc := md.Oneofs().ByName("value")
	if oneofDesc == nil {
		return nil // unexpected, but be safe
	}

	which := celMsg.WhichOneof(oneofDesc)
	if which == nil {
		return nil // no value set
	}

	if which.Name() == "literal" {
		// Runtime templates can appear in CelString literal values (for example,
		// thread.inject.content like "Review: {{nodes.prev.response_text}}").
		// Keep literals intact unless they contain template delimiters.
		if fullName == "reliant.v1.CelString" && strings.Contains(path, "thread.inject.") {
			literalFD := md.Fields().ByName("literal")
			if literalFD != nil {
				literal := celMsg.Get(literalFD).String()
				if strings.Contains(literal, "{{") {
					result, err := eval.EvalString(literal)
					if err != nil {
						return fmt.Errorf("%s: evaluating literal %q: %w", path, literal, err)
					}
					if err := setCelLiteral(parent, fd, celMsg, fullName, result, path); err != nil {
						return err
					}
				}
			}
		}
		return nil // already resolved
	}

	// It's the "expr" case — get the expression string and evaluate.
	exprFD := md.Fields().ByName("expr")
	if exprFD == nil {
		return fmt.Errorf("%s: CelX message %s has no 'expr' field", path, fullName)
	}
	expr := celMsg.Get(exprFD).String()

	result, err := eval.EvalString(expr)
	if err != nil {
		return fmt.Errorf("%s: evaluating %q: %w", path, expr, err)
	}

	// Convert and set the literal value.
	if err := setCelLiteral(parent, fd, celMsg, fullName, result, path); err != nil {
		return err
	}

	return nil
}

// setCelLiteral converts an evaluation result and sets the literal oneof case.
func setCelLiteral(
	parent protoreflect.Message,
	fd protoreflect.FieldDescriptor,
	celMsg protoreflect.Message,
	fullName protoreflect.FullName,
	result interface{},
	path string,
) error {
	md := celMsg.Descriptor()
	literalFD := md.Fields().ByName("literal")
	if literalFD == nil {
		return fmt.Errorf("%s: CelX message %s has no 'literal' field", path, fullName)
	}

	// Clear the expr field to switch the oneof to literal.
	exprFD := md.Fields().ByName("expr")
	if exprFD != nil {
		celMsg.Clear(exprFD)
	}

	switch fullName {
	case "reliant.v1.CelString":
		s, err := toString(result, path)
		if err != nil {
			return err
		}
		celMsg.Set(literalFD, protoreflect.ValueOfString(s))

	case "reliant.v1.CelBool":
		b, err := toBool(result, path)
		if err != nil {
			return err
		}
		celMsg.Set(literalFD, protoreflect.ValueOfBool(b))

	case "reliant.v1.CelDouble":
		f, err := toFloat64(result, path)
		if err != nil {
			return err
		}
		celMsg.Set(literalFD, protoreflect.ValueOfFloat64(f))

	case "reliant.v1.CelInt":
		i, err := toInt64(result, path)
		if err != nil {
			return err
		}
		celMsg.Set(literalFD, protoreflect.ValueOfInt64(i))

	case "reliant.v1.CelStringList":
		sl, err := toStringSlice(result, path)
		if err != nil {
			return err
		}
		stringList := &reliantv1.StringList{Values: sl}
		celMsg.Set(literalFD, protoreflect.ValueOfMessage(stringList.ProtoReflect()))

	case "reliant.v1.CelModelSelector":
		ms, err := toModelSelector(result, path)
		if err != nil {
			return err
		}
		celMsg.Set(literalFD, protoreflect.ValueOfMessage(ms.ProtoReflect()))

	default:
		return fmt.Errorf("%s: unknown CelX type %s", path, fullName)
	}

	return nil
}

// resolveRepeatedField handles repeated (list) message fields.
func resolveRepeatedField(
	parent protoreflect.Message,
	fd protoreflect.FieldDescriptor,
	v protoreflect.Value,
	eval CELEvaluator,
	path string,
) error {
	list := v.List()
	for i := 0; i < list.Len(); i++ {
		elem := list.Get(i)
		elemPath := fmt.Sprintf("%s[%d]", path, i)

		if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
			elemMsg := elem.Message()
			if isCelWrapper(elemMsg.Descriptor()) {
				// CelX in a repeated field is unusual but handle it.
				// We can't easily call resolveCelField here since parent/fd
				// point to the list, not individual elements. Recurse instead.
				if err := resolveMessage(elemMsg, eval, elemPath); err != nil {
					return err
				}
			} else {
				if err := resolveMessage(elemMsg, eval, elemPath); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// resolveMapField handles map fields with message values.
func resolveMapField(
	parent protoreflect.Message,
	fd protoreflect.FieldDescriptor,
	v protoreflect.Value,
	eval CELEvaluator,
	path string,
) error {
	mapVal := v.Map()
	valueFD := fd.MapValue()

	if valueFD.Kind() != protoreflect.MessageKind && valueFD.Kind() != protoreflect.GroupKind {
		return nil // map values are scalars, nothing to resolve
	}

	if valueFD.Message().FullName() == "google.protobuf.Value" {
		var errs []error
		mapVal.Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
			elemPath := fmt.Sprintf("%s[%s]", path, k.String())
			pbValue, ok := mv.Message().Interface().(*structpb.Value)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: expected *structpb.Value, got %T", elemPath, mv.Message().Interface()))
				return true
			}
			resolvedValue, err := resolveStructPBValueTemplates(pbValue, eval, elemPath)
			if err != nil {
				errs = append(errs, err)
				return true
			}
			mapVal.Set(k, protoreflect.ValueOfMessage(resolvedValue.ProtoReflect()))
			return true
		})
		if len(errs) > 0 {
			return errs[0]
		}
		parent.Set(fd, protoreflect.ValueOfMap(mapVal))
		return nil
	}

	var errs []error
	mapVal.Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
		elemPath := fmt.Sprintf("%s[%s]", path, k.String())
		elemMsg := mv.Message()

		if isCelWrapper(elemMsg.Descriptor()) {
			if err := resolveMessage(elemMsg, eval, elemPath); err != nil {
				errs = append(errs, err)
			}
		} else {
			if err := resolveMessage(elemMsg, eval, elemPath); err != nil {
				errs = append(errs, err)
			}
		}
		return true
	})

	if len(errs) > 0 {
		return errs[0]
	}
	parent.Set(fd, protoreflect.ValueOfMap(mapVal))
	return nil
}

// resolveStructPBValueTemplates resolves {{...}} templates inside structpb.Value trees.
// This allows map<string, google.protobuf.Value> fields (for example workflow/loop args)
// to preserve native runtime types (bool/int/list/object) instead of stringifying
// CEL expressions.
func resolveStructPBValueTemplates(value *structpb.Value, eval CELEvaluator, path string) (*structpb.Value, error) {
	if value == nil {
		return nil, nil
	}

	switch kind := value.GetKind().(type) {
	case *structpb.Value_StringValue:
		if !strings.Contains(kind.StringValue, "{{") {
			return value, nil
		}
		resolvedValue, err := eval.EvalString(kind.StringValue)
		if err != nil {
			return nil, fmt.Errorf("%s: evaluating %q: %w", path, kind.StringValue, err)
		}
		pbValue, err := structpb.NewValue(resolvedValue)
		if err != nil {
			return nil, fmt.Errorf("%s: converting resolved template result (%T) to structpb.Value: %w", path, resolvedValue, err)
		}
		return pbValue, nil

	case *structpb.Value_StructValue:
		resolvedFields := make(map[string]*structpb.Value, len(kind.StructValue.GetFields()))
		for fieldName, fieldValue := range kind.StructValue.GetFields() {
			resolvedFieldValue, err := resolveStructPBValueTemplates(fieldValue, eval, fmt.Sprintf("%s.%s", path, fieldName))
			if err != nil {
				return nil, err
			}
			resolvedFields[fieldName] = resolvedFieldValue
		}
		return structpb.NewStructValue(&structpb.Struct{Fields: resolvedFields}), nil

	case *structpb.Value_ListValue:
		resolvedValues := make([]*structpb.Value, 0, len(kind.ListValue.GetValues()))
		for index, listValue := range kind.ListValue.GetValues() {
			resolvedListValue, err := resolveStructPBValueTemplates(listValue, eval, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			resolvedValues = append(resolvedValues, resolvedListValue)
		}
		return structpb.NewListValue(&structpb.ListValue{Values: resolvedValues}), nil

	default:
		return value, nil
	}
}

// =============================================================================
// TYPE CONVERSION HELPERS
// =============================================================================

func toString(v interface{}, path string) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case fmt.Stringer:
		return val.String(), nil
	default:
		// JSON-serialize non-string results (complex types)
		data, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("%s: cannot convert %T to string: %w", path, v, err)
		}
		return string(data), nil
	}
}

func toBool(v interface{}, path string) (bool, error) {
	switch val := v.(type) {
	case bool:
		return val, nil
	default:
		return false, fmt.Errorf("%s: cannot convert %T to bool (value: %v)", path, v, v)
	}
}

func toFloat64(v interface{}, path string) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case int:
		return float64(val), nil
	case int32:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case json.Number:
		return val.Float64()
	default:
		return 0, fmt.Errorf("%s: cannot convert %T to float64 (value: %v)", path, v, v)
	}
}

func toInt64(v interface{}, path string) (int64, error) {
	switch val := v.(type) {
	case int:
		return int64(val), nil
	case int32:
		return int64(val), nil
	case int64:
		return val, nil
	case float64:
		return int64(val), nil
	case float32:
		return int64(val), nil
	case json.Number:
		return val.Int64()
	default:
		return 0, fmt.Errorf("%s: cannot convert %T to int64 (value: %v)", path, v, v)
	}
}

func toStringSlice(v interface{}, path string) ([]string, error) {
	switch val := v.(type) {
	case []string:
		return val, nil
	case []interface{}:
		result := make([]string, len(val))
		for i, elem := range val {
			s, ok := elem.(string)
			if !ok {
				return nil, fmt.Errorf("%s: string list element %d is %T, not string", path, i, elem)
			}
			result[i] = s
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s: cannot convert %T to []string (value: %v)", path, v, v)
	}
}

func toModelSelector(v interface{}, path string) (*reliantv1.ModelSelector, error) {
	// The result might be a map from CEL evaluation.
	switch val := v.(type) {
	case *reliantv1.ModelSelector:
		return val, nil
	case map[string]interface{}:
		ms := &reliantv1.ModelSelector{}
		if id, ok := val["id"].(string); ok {
			ms.Id = id
		}
		if tags, ok := val["tags"]; ok {
			switch t := tags.(type) {
			case []string:
				ms.Tags = t
			case []interface{}:
				for _, elem := range t {
					if s, ok := elem.(string); ok {
						ms.Tags = append(ms.Tags, s)
					}
				}
			}
		}
		if providers, ok := val["providers"]; ok {
			switch p := providers.(type) {
			case []string:
				ms.Providers = p
			case []interface{}:
				for _, elem := range p {
					if s, ok := elem.(string); ok {
						ms.Providers = append(ms.Providers, s)
					}
				}
			}
		}
		return ms, nil
	case string:
		return nil, fmt.Errorf("%s: model selector must be an object (e.g. {id: \"model-name\"}), got string %q — strings are not accepted; convert to {id: string} at the system boundary", path, val)
	default:
		return nil, fmt.Errorf("%s: unsupported model selector type %T — expected map[string]interface{} or *ModelSelector", path, v)
	}
}

// appendPath builds a dot-separated path for error messages.
func appendPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}
