// Copyright (c) 2025 Reliant Labs
// Package schema provides activity input/output type information for template evaluation.
// This package exists to break import cycles between runtime and handlers.
//
// forge:exclude-contract
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
package schema

import (
	"reflect"
	"strings"

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
func GetOutputDefaults(activityName string) map[string]interface{} {
	info, ok := activityTypes[activityName]
	if !ok || info.OutputType == nil {
		return nil
	}

	return getFieldDefaults(info.OutputType)
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
