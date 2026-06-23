// Package runtime input processing utilities.
//
// This file contains runtime utilities for processing workflow inputs:
// - Type coercion (coerceToType)
// - Default value application (ApplyDefaults)
//
// For validation, see validate.go.

package runtime

import (
	"fmt"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// coerceToInt64 converts various numeric types to int64 for CEL compatibility.
func coerceToInt64(value interface{}) (interface{}, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case float64:
		// Only coerce if it's a whole number
		if v == float64(int64(v)) {
			return int64(v), true
		}
		return value, false
	case float32:
		if float64(v) == float64(int64(v)) {
			return int64(v), true
		}
		return value, false
	case string:
		// Use strconv.ParseInt for strict integer parsing
		// This correctly rejects strings like "42.5" (unlike fmt.Sscanf)
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i, true
		}
		return value, false
	default:
		return value, false
	}
}

// coerceToFloat64 converts various numeric types to float64.
func coerceToFloat64(value interface{}) (interface{}, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
		return value, false
	default:
		return value, false
	}
}

// coerceToBool converts various types to bool.
func coerceToBool(value interface{}) (interface{}, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
		return value, false
	case int:
		return v != 0, true
	case int64:
		return v != 0, true
	case float64:
		return v != 0, true
	default:
		return value, false
	}
}

// coerceToString converts various types to string.
func coerceToString(value interface{}) (interface{}, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

// ApplyDefaults applies default values from proto input schemas to inputs and coerces
// all values to their declared schema types.
// This should be called after validation to fill in missing optional fields
// and ensure type consistency for CEL evaluation.
//
// For group inputs (type: group), this function creates nested map structure
// (e.g., inputs["agent"]["model"]) for direct CEL access via inputs.agent.model.
//
// Schema-defined inputs are only added when they define explicit defaults; required
// inputs without defaults remain absent so validation can reject missing values.
func ApplyDefaults(inputs map[string]interface{}, schemas map[string]*reliantv1.Input) map[string]interface{} {
	return applyDefaults(inputs, schemas, false)
}

// ApplyDefaultsForRuntime applies schema defaults after required inputs have already
// been validated. It also inserts zero values for any remaining missing inputs so
// CEL expressions can safely access optional schema fields without no-key errors.
func ApplyDefaultsForRuntime(inputs map[string]interface{}, schemas map[string]*reliantv1.Input) map[string]interface{} {
	return applyDefaults(inputs, schemas, true)
}

func applyDefaults(inputs map[string]interface{}, schemas map[string]*reliantv1.Input, includeZeroValues bool) map[string]interface{} {
	if schemas == nil {
		return inputs
	}

	result := make(map[string]interface{})

	// Copy existing inputs
	for k, v := range inputs {
		result[k] = v
	}

	// Apply defaults for missing optional fields and coerce all values to schema types.
	for name, input := range schemas {
		if input == nil {
			continue
		}

		// Handle group inputs by creating nested map structure when defaults or provided
		// values require it. Required nested inputs without defaults are intentionally
		// left absent so validation can reject missing required params.
		if nested := model.GetGroupInputs(input); nested != nil {
			var groupMap map[string]interface{}
			if existing, ok := result[name].(map[string]interface{}); ok {
				groupMap = existing
			}

			for nestedName, nestedInput := range nested {
				if nestedInput == nil {
					continue
				}

				var value interface{}
				exists := false
				if groupMap != nil {
					value, exists = groupMap[nestedName]
				}

				if !exists || value == nil {
					if def := model.GetInputDefault(nestedInput); def != nil {
						value = def
					} else if includeZeroValues {
						value = zeroValueForType(model.GetInputType(nestedInput))
					} else {
						continue
					}
					if groupMap == nil {
						groupMap = make(map[string]interface{})
						result[name] = groupMap
					}
					groupMap[nestedName] = value
				}

				if value != nil {
					if coerced, ok := coerceInputValue(value, nestedInput); ok {
						groupMap[nestedName] = coerced
					}
				}
			}
			continue
		}

		value, exists := result[name]

		if !exists || value == nil {
			if def := model.GetInputDefault(input); def != nil {
				value = def
			} else if includeZeroValues {
				value = zeroValueForType(model.GetInputType(input))
			} else {
				continue
			}
			result[name] = value
		}

		if value != nil {
			if coerced, ok := coerceInputValue(value, input); ok {
				result[name] = coerced
			}
		}
	}

	return result
}

// coerceToType coerces a value based on a type string.
func coerceInputValue(value interface{}, input *reliantv1.Input) (interface{}, bool) {
	if input == nil {
		return value, false
	}

	if enumCfg := input.GetEnumInput(); enumCfg != nil && enumCfg.GetMulti() {
		switch enumValue := value.(type) {
		case []interface{}:
			return enumValue, true
		case []string:
			items := make([]interface{}, len(enumValue))
			for index, item := range enumValue {
				items[index] = item
			}
			return items, true
		default:
			return value, false
		}
	}

	return coerceToType(value, model.GetInputType(input))
}

func coerceToType(value interface{}, typeName string) (interface{}, bool) {
	switch typeName {
	case "integer":
		return coerceToInt64(value)
	case "number":
		return coerceToFloat64(value)
	case "boolean":
		return coerceToBool(value)
	case "string", "enum", "message":
		return coerceToString(value)
	default:
		return value, false
	}
}

func zeroValueForType(schemaType string) interface{} {
	switch schemaType {
	case "string", "enum":
		return ""
	case "model":
		return nil
	case "integer":
		return int64(0)
	case "number":
		return float64(0)
	case "boolean":
		return false
	case "array", "tools":
		return []interface{}{}
	case "object":
		return map[string]interface{}{}
	default:
		return ""
	}
}
