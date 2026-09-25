// Copyright (c) 2025 Reliant Labs
// Package schema provides activity input/output type information for template evaluation.
// This package exists to break import cycles between runtime and handlers.
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
//
//forge:exclude-contract: activity input/output type metadata for template evaluation; breaks a runtime/handlers import cycle
package schema

import (
	"reflect"
	"strings"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ActivityInputType holds information about an activity's input type
type ActivityInputType struct {
	Name       string
	InputType  reflect.Type
	OutputType reflect.Type
	// InputDescriptor is the proto message descriptor for the input type.
	// When set, proto-based metadata extraction is used instead of Go reflect.
	InputDescriptor protoreflect.MessageDescriptor
	// OutputDescriptor is the proto message descriptor for the output type.
	OutputDescriptor protoreflect.MessageDescriptor
}

// registry of activity types - populated at init time by handlers package
var activityTypes = make(map[string]*ActivityInputType)

// RegisterActivityType registers an activity's input/output types for schema introspection.
// Called by handlers package during init.
//
// This keeps all CEL type registration in one place and avoids import cycles.
//
// Panics if the activity is already registered - use this to catch duplicate registrations
// which indicate a programming error (e.g., RegisterActivity overwriting init() registration).
func RegisterActivityType(name string, inputType, outputType reflect.Type) {
	if _, exists := activityTypes[name]; exists {
		panic("activity type already registered: " + name + " (duplicate registration is a programming error)")
	}
	activityTypes[name] = &ActivityInputType{
		Name:       name,
		InputType:  inputType,
		OutputType: outputType,
	}
}

// IsActivityTypeRegistered returns true if an activity type is already registered.
// Used to check before calling RegisterActivityType to avoid the panic.
func IsActivityTypeRegistered(name string) bool {
	_, ok := activityTypes[name]
	return ok
}

// GetInputDefaults returns a map with all fields from an activity's input type set to zero values.
// This is used to ensure sourceData has all expected fields before CEL evaluation.
// Uses reflection to get ALL fields including those with omitempty tags.
func GetInputDefaults(activityName string) map[string]interface{} {
	info, ok := activityTypes[activityName]
	if !ok || info.InputType == nil {
		return nil
	}

	return getFieldDefaults(info.InputType)
}

// GetOutputDefaults returns a map with all fields from an activity's output type set to zero values.
//
// These are the zeros for a node that did NOT run (loop-output typed-zero
// substitution): sub-message fields stay nil there, because "no structured
// response" is not the same as an empty one. To complete the result of a node
// that DID run, use FillOutputDefaults.
func GetOutputDefaults(activityName string) map[string]interface{} {
	info, ok := activityTypes[activityName]
	if !ok || info.OutputType == nil {
		return nil
	}

	return getFieldDefaults(info.OutputType)
}

// outputDescriptor resolves an activity's proto output descriptor: the
// registered output type when it is a proto message, else the node-type output
// declared in the workflow proto (the registry validation types node outputs
// with). The fallback matters wherever activity registration has not run.
func outputDescriptor(activityName string) protoreflect.MessageDescriptor {
	if info, ok := activityTypes[activityName]; ok {
		if md := protoDescriptorOf(info.OutputType); md != nil {
			return md
		}
		if info.OutputType != nil {
			return nil // a registered non-proto output: reflection describes it
		}
	}
	if md, ok := wfcel.OutputDescriptorForActivity(activityName); ok {
		return md
	}
	return nil
}

// FillOutputDefaults completes the decoded result of an activity that RAN, in
// place, so it carries every field its output type declares — recursively,
// from the output message's proto descriptor (see output_defaults.go): a
// missing field gets its zero value (an unset sub-message becomes its zero
// shape, not nil), a present sub-message is completed the same way, and so is
// each element of a present repeated message field. Present values are never
// overwritten, and message_only fields are never added. Returns output (a new
// map when output is nil). A registered non-proto output gets a top-level fill
// from Go reflection.
func FillOutputDefaults(activityName string, output map[string]interface{}) map[string]interface{} {
	if output == nil {
		output = make(map[string]interface{})
	}
	if md := outputDescriptor(activityName); md != nil {
		fillProtoMessage(md, output)
		return output
	}
	info, ok := activityTypes[activityName]
	if !ok || info.OutputType == nil {
		return output
	}
	for field, value := range getFieldDefaults(info.OutputType) {
		if _, exists := output[field]; !exists {
			output[field] = value
		}
	}
	return output
}

// getFieldDefaults uses reflection to extract all JSON field names and their zero values.
// This works even for fields with omitempty tags.
// Fields with `reliant:"-"` tag are excluded from schema output (internal-only fields).
func getFieldDefaults(t reflect.Type) map[string]interface{} {
	if t == nil {
		return nil
	}

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil
	}

	result := make(map[string]interface{})
	zeroValue := reflect.New(t).Elem()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Skip fields marked as internal-only with reliant:"-" tag
		if field.Tag.Get("reliant") == "-" {
			continue
		}

		// Get JSON field name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse json tag (handle "name,omitempty" format)
		jsonName := strings.Split(jsonTag, ",")[0]
		if jsonName == "" {
			jsonName = field.Name
		}

		// Get zero value for the field
		fieldValue := zeroValue.Field(i)
		result[jsonName] = getZeroValue(fieldValue)
	}

	return result
}

// GetOutputFields returns all field names from an activity's output type.
// Used for validation to check if nodes.<id>.X references valid fields.
func GetOutputFields(activityName string) []string {
	info, ok := activityTypes[activityName]
	if !ok || info.OutputType == nil {
		return nil
	}

	return getFieldNames(info.OutputType)
}

// GetInputFields returns all field names from an activity's input type.
func GetInputFields(activityName string) []string {
	info, ok := activityTypes[activityName]
	if !ok || info.InputType == nil {
		return nil
	}

	return getFieldNames(info.InputType)
}

// getFieldNames extracts all JSON field names from a struct type.
// Fields with `reliant:"-"` tag are excluded from schema output (internal-only fields).
func getFieldNames(t reflect.Type) []string {
	if t == nil {
		return nil
	}

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil
	}

	var fields []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Skip fields marked as internal-only with reliant:"-" tag
		if field.Tag.Get("reliant") == "-" {
			continue
		}

		// Get JSON field name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse json tag (handle "name,omitempty" format)
		jsonName := strings.Split(jsonTag, ",")[0]
		if jsonName == "" {
			jsonName = field.Name
		}

		fields = append(fields, jsonName)
	}

	return fields
}

// RegisterActivityProtoDescriptors registers proto message descriptors for an activity.
// These are used for metadata extraction (field descriptions, enum values, etc.)
// and take precedence over Go reflect-based extraction.
func RegisterActivityProtoDescriptors(name string, inputDesc, outputDesc protoreflect.MessageDescriptor) {
	info, ok := activityTypes[name]
	if !ok {
		// Activity not yet registered via RegisterActivityType, create stub
		activityTypes[name] = &ActivityInputType{
			Name:             name,
			InputDescriptor:  inputDesc,
			OutputDescriptor: outputDesc,
		}
		return
	}
	info.InputDescriptor = inputDesc
	info.OutputDescriptor = outputDesc
}

// ListActivities returns all registered activity names.
func ListActivities() []string {
	names := make([]string, 0, len(activityTypes))
	for name := range activityTypes {
		names = append(names, name)
	}
	return names
}

// getZeroValue returns an appropriate zero value for CEL evaluation.
// Returns CEL-safe defaults: empty slices/maps (not nil) for reference types,
// zero values for primitives. This ensures CEL operations like size() and 'in' work safely.
func getZeroValue(v reflect.Value) interface{} {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		return nil
	case reflect.Slice:
		// Return empty slice instead of nil for CEL-safe operations like size()
		return []interface{}{}
	case reflect.Map:
		// Return empty map instead of nil for CEL-safe operations like 'key in map'
		return map[string]interface{}{}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 0
	case reflect.Float32, reflect.Float64:
		return 0.0
	case reflect.Bool:
		return false
	case reflect.String:
		return ""
	case reflect.Struct:
		// For nested structs, recurse
		return getFieldDefaults(v.Type())
	default:
		return nil
	}
}
