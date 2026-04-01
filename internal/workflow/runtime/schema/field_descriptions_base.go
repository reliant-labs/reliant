// Copyright (c) 2025 Reliant Labs

package schema

// FieldDescriptions maps "TypeName.jsonFieldName" to its docstring.
// Populated by generated code in field_descriptions.go via init().
// Used by extractInputFields to provide descriptions for UI.
var FieldDescriptions = make(map[string]string)

// GetFieldDescription returns the docstring for a type's field.
// Returns empty string if not found or if generated descriptions aren't available.
func GetFieldDescription(typeName, fieldName string) string {
	return FieldDescriptions[typeName+"."+fieldName]
}
