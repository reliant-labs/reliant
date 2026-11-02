// Package runtime input processing utilities.
//
// This file contains runtime utilities for processing workflow inputs:
// - Putting values in their declared type's form (coerceInputValue)
// - Default value application (ApplyDefaults)
//
// For validation, see validate.go.

package runtime

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// ApplyDefaults applies default values from proto input schemas to inputs and puts
// every value that fits its declared type in that type's form (coerceInputValue).
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
						value = inputZeroValue(nestedInput)
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
				value = inputZeroValue(input)
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

// coerceInputValue puts a value in the form a run holds an input of its
// declared type in, by the one rule admission and validation also apply
// (validation.InputValue): a whole number becomes an int64 for an integer, a
// boolean or number becomes its text for a string. It never parses text: a
// string where a boolean or number is declared is left as it is, for
// validation to refuse.
func coerceInputValue(value interface{}, input *reliantv1.Input) (interface{}, bool) {
	return validation.InputValue(value, input)
}

// inputZeroValue is the value a declared input is bound to at run time when the
// caller supplied nothing and the schema has no default. Every declared input
// is bound, so `{{inputs.x}}` never fails with "no such key" — it reads the
// zero of x's type instead. (Required-ness is checked BEFORE this, by
// validation.ValidateInputs; this only runs once that has passed.)
//
// The zero has the CEL type a caller-supplied value would have, so it composes
// with the operators authors already use: `inputs.tools + ['x']`, `size(...)`,
// `inputs.flag == true`, `k in inputs.opts`.
//
//   - string, message                      → ""
//   - integer → 0, number → 0.0, boolean → false
//   - array, tools, attachments            → []
//   - object, any                          → {}
//   - integration types (github.repository) → "" ([] when multi)
//   - enum, preset                         → "" ([] when multi). Deliberately
//     NOT the first allowed value: that would silently select a real option
//     the caller never chose.
//   - model                                → null. An empty selector must stay
//     "unset" so model resolution falls through to its own default; size(null)
//     is 0, so it is still safe to test.
func inputZeroValue(input *reliantv1.Input) interface{} {
	if model.IsIntegrationInput(input) {
		if model.IsMultiIntegrationInput(input) {
			return []interface{}{}
		}
		return ""
	}
	switch model.GetInputType(input) {
	case "string", "message":
		return ""
	case "integer":
		return int64(0)
	case "number":
		return float64(0)
	case "boolean":
		return false
	case "array", "tools", "attachments":
		return []interface{}{}
	case "object", "any":
		return map[string]interface{}{}
	case "enum":
		if input.GetEnumInput().GetMulti() {
			return []interface{}{}
		}
		return ""
	case "preset":
		if input.GetPresetInput().GetMulti() {
			return []interface{}{}
		}
		return ""
	case "model":
		return nil
	default:
		return ""
	}
}
