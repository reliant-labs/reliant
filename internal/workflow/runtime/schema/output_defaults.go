// Copyright (c) 2025 Reliant Labs
package schema

import (
	"reflect"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Why output defaults are derived from the proto descriptor.
//
// An activity result reaches the workflow as protojson with EmitUnpopulated
// off, so every unset field is simply ABSENT: a zero int, an empty string, an
// empty list, and — the case Go reflection could not describe — an unset
// sub-message. Reflection saw a message field as a nil pointer and defaulted it
// to nil, so `nodes.x.message.text` failed with "no such key" whenever the
// activity left `message` unset, and fields inside a present message
// (`message.id`, `tool_calls[0].input`) were never filled at all. The
// descriptor knows the full shape, so the defaults here are the JSON the
// workflow would have seen had every field been emitted.
//
// Two deliberate exceptions:
//   - message_only fields are skipped. The activity wrapper clears them before
//     the result enters history; filling them back in would reintroduce a
//     field the workflow is not supposed to see.
//   - google.protobuf well-known types (Struct, Value, ...) have no fixed
//     field set. Struct defaults to an empty map, the others to nil.

var protoMessageType = reflect.TypeOf((*proto.Message)(nil)).Elem()

// protoDescriptorOf returns the message descriptor for a Go type that is a
// proto message (pointer or value form), or nil.
func protoDescriptorOf(t reflect.Type) protoreflect.MessageDescriptor {
	if t == nil {
		return nil
	}
	if t.Kind() != reflect.Ptr {
		t = reflect.PointerTo(t)
	}
	if !t.Implements(protoMessageType) {
		return nil
	}
	msg, ok := reflect.New(t.Elem()).Interface().(proto.Message)
	if !ok {
		return nil
	}
	return msg.ProtoReflect().Descriptor()
}

// protoOutputDefaults returns the zero-valued JSON shape of a message: one
// entry per non-message-only field, keyed by proto name.
func protoOutputDefaults(md protoreflect.MessageDescriptor) map[string]interface{} {
	out := make(map[string]interface{})
	fillProtoMessage(md, out)
	return out
}

// fillProtoMessage adds every missing field of md to m and recurses into
// present sub-messages and repeated-message elements.
func fillProtoMessage(md protoreflect.MessageDescriptor, m map[string]interface{}) {
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if wfcel.IsMessageOnly(fd) {
			continue
		}
		name := string(fd.Name())
		value, exists := m[name]
		if !exists || (value == nil && !isNullableField(fd)) {
			m[name] = protoFieldZero(fd)
			continue
		}
		if fd.IsMap() || !isFixedShapeMessage(fd) {
			continue
		}
		if fd.IsList() {
			if items, ok := value.([]interface{}); ok {
				for _, item := range items {
					if itemMap, ok := item.(map[string]interface{}); ok {
						fillProtoMessage(fd.Message(), itemMap)
					}
				}
			}
			continue
		}
		if sub, ok := value.(map[string]interface{}); ok {
			fillProtoMessage(fd.Message(), sub)
		}
	}
}

// protoFieldZero is the JSON-shaped zero value for one field.
func protoFieldZero(fd protoreflect.FieldDescriptor) interface{} {
	switch {
	case fd.IsList():
		return []interface{}{}
	case fd.IsMap():
		return map[string]interface{}{}
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return false
	case protoreflect.StringKind, protoreflect.BytesKind:
		return ""
	case protoreflect.EnumKind:
		// protojson emits the enum's name; the zero value is its first value.
		if values := fd.Enum().Values(); values.Len() > 0 {
			return string(values.Get(0).Name())
		}
		return ""
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return 0.0
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Struct":
			return map[string]interface{}{}
		case "google.protobuf.ListValue":
			return []interface{}{}
		}
		if isFixedShapeMessage(fd) {
			return protoOutputDefaults(fd.Message())
		}
		return nil
	default:
		// All integer kinds. protojson renders 64-bit integers as strings, but
		// a CEL consumer compares them numerically, so the zero stays numeric.
		return 0
	}
}

// isFixedShapeMessage reports whether fd is a message with a known field set
// that can be filled — i.e. not a google.protobuf well-known type.
func isFixedShapeMessage(fd protoreflect.FieldDescriptor) bool {
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return false
	}
	return fd.Message().FullName().Parent() != "google.protobuf"
}

// isNullableField reports whether null is a meaningful value for fd, so a
// present null must be kept rather than replaced by the zero value. Only
// google.protobuf.Value (and the dynamic well-known types) can carry null.
func isNullableField(fd protoreflect.FieldDescriptor) bool {
	if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
		return false
	}
	switch fd.Message().FullName() {
	case "google.protobuf.Value", "google.protobuf.Any":
		return true
	}
	return false
}
