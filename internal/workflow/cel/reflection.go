package wfcel

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// FieldInfo describes a single proto field for validation and documentation.
type FieldInfo struct {
	// Name is the proto field name (snake_case).
	Name string
	// Type is a simplified type string: "string", "bool", "double", "int",
	// "string_list", "model_selector", "message", "enum", "bytes", etc.
	Type string
	// Description is a human-readable description from the (reliant) annotation.
	Description string
	// EnumValues are the allowed values, parsed from the pipe-separated (reliant).enum_values.
	EnumValues []string
	// UIHint is the UI rendering hint from the (reliant) annotation.
	UIHint string
	// Hidden is true if the field is internal/runtime-only (from (reliant).hidden).
	Hidden bool
	// Category is the field grouping category from the (reliant) annotation.
	Category string
	// DefaultValue is the default as a string from the (reliant) annotation.
	DefaultValue string
	// MinValue is the minimum for numeric fields from the (reliant) annotation.
	MinValue *float64
	// MaxValue is the maximum for numeric fields from the (reliant) annotation.
	MaxValue *float64
	// Label is a short human-readable field label from the (reliant) annotation.
	Label string
	// Placeholder is optional helper text for text-like controls from the (reliant) annotation.
	Placeholder *string
	// VisibilityContexts are optional UI visibility contexts from the (reliant) annotation.
	VisibilityContexts []string
	// CleanupSemantics is an optional cleanup behavior hint from the (reliant) annotation.
	CleanupSemantics *string
	// IsCEL is true if the field is a CelX wrapper (CelString, CelBool, etc.).
	IsCEL bool
	// IsDirect is true if the field is a DirectCelBool.
	IsDirect bool
	// IsRepeated is true if the field is a repeated (list) field.
	IsRepeated bool
	// IsMap is true if the field is a map field.
	IsMap bool
}

// celWrapperTypeMap maps CelX wrapper full names to their underlying simple type.
var celWrapperTypeMap = map[protoreflect.FullName]string{
	"reliant.v1.CelString":        "string",
	"reliant.v1.CelBool":          "bool",
	"reliant.v1.CelDouble":        "double",
	"reliant.v1.CelInt":           "int",
	"reliant.v1.CelStringList":    "string_list",
	"reliant.v1.CelModelSelector": "model_selector",
	"reliant.v1.DirectCelBool":    "bool",
}

// ExtractFieldInfo reads proto descriptors and (reliant) annotations
// to build field info for a message type.
func ExtractFieldInfo(md protoreflect.MessageDescriptor) []FieldInfo {
	fields := md.Fields()
	result := make([]FieldInfo, 0, fields.Len())

	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)

		// Skip oneof discriminator fields that are synthetic
		if fd.ContainingOneof() != nil && fd.ContainingOneof().IsSynthetic() {
			continue
		}

		info := FieldInfo{
			Name:       string(fd.Name()),
			Type:       protoKindToType(fd),
			IsRepeated: fd.IsList(),
			IsMap:      fd.IsMap(),
		}

		// Check if this is a CelX wrapper message type
		if fd.Kind() == protoreflect.MessageKind {
			msgFullName := fd.Message().FullName()
			if typeName, ok := celWrapperTypeMap[msgFullName]; ok {
				info.IsCEL = true
				info.Type = typeName
				if msgFullName == "reliant.v1.DirectCelBool" {
					info.IsDirect = true
				}
			}
		}

		// Read (reliant) field option extension
		meta := getFieldMeta(fd)
		if meta != nil {
			info.Description = meta.GetDescription()
			info.UIHint = meta.GetUiHint()
			info.Hidden = meta.GetHidden()
			info.Category = meta.GetCategory()
			info.DefaultValue = meta.GetDefaultValue()
			info.Label = meta.GetLabel()
			info.VisibilityContexts = append(info.VisibilityContexts, meta.GetVisibilityContexts()...)
			if meta.MinValue != nil {
				v := meta.GetMinValue()
				info.MinValue = &v
			}
			if meta.MaxValue != nil {
				v := meta.GetMaxValue()
				info.MaxValue = &v
			}
			if meta.Placeholder != nil {
				v := meta.GetPlaceholder()
				info.Placeholder = &v
			}
			if meta.CleanupSemantics != nil {
				v := meta.GetCleanupSemantics()
				info.CleanupSemantics = &v
			}
			if ev := meta.GetEnumValues(); ev != "" {
				info.EnumValues = strings.Split(ev, "|")
			}
		}

		result = append(result, info)
	}

	return result
}

// getFieldMeta extracts the (reliant) FieldMeta extension from a field's options.
func getFieldMeta(fd protoreflect.FieldDescriptor) *reliantv1.FieldMeta {
	opts := fd.Options()
	if opts == nil {
		return nil
	}
	ext := proto.GetExtension(opts, reliantv1.E_Reliant)
	if ext == nil {
		return nil
	}
	meta, ok := ext.(*reliantv1.FieldMeta)
	if !ok {
		return nil
	}
	return meta
}

// protoKindToType maps a proto field descriptor to a simple type string.
func protoKindToType(fd protoreflect.FieldDescriptor) string {
	if fd.IsMap() {
		return "map"
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "int"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "int"
	case protoreflect.FloatKind:
		return "double"
	case protoreflect.DoubleKind:
		return "double"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "bytes"
	case protoreflect.EnumKind:
		return "enum"
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "message"
	default:
		return "unknown"
	}
}

// ExtractFieldInfoMap returns field info as a map keyed by field name.
func ExtractFieldInfoMap(md protoreflect.MessageDescriptor) map[string]FieldInfo {
	infos := ExtractFieldInfo(md)
	result := make(map[string]FieldInfo, len(infos))
	for _, info := range infos {
		result[info.Name] = info
	}
	return result
}
