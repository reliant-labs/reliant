// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// modelInput creates a model input with an optional default selector.
func modelInput(defaultSelector *reliantv1.ModelSelector) *reliantv1.Input {
	cfg := &reliantv1.ModelInputConfig{}
	if defaultSelector != nil {
		cfg.Default = defaultSelector
	}
	return &reliantv1.Input{
		Type:   "model",
		Config: &reliantv1.Input_ModelInput{ModelInput: cfg},
	}
}

// stringInput creates a string input with an optional default value.
func stringInput(defaultVal *string) *reliantv1.Input {
	cfg := &reliantv1.StringInputConfig{}
	if defaultVal != nil {
		cfg.Default = defaultVal
	}
	return &reliantv1.Input{
		Type:   "string",
		Config: &reliantv1.Input_StringInput{StringInput: cfg},
	}
}

// stringInputWithMinLength creates a string input with a min_length constraint and optional default.
func stringInputWithMinLength(minLen int32, defaultVal *string) *reliantv1.Input {
	cfg := &reliantv1.StringInputConfig{
		MinLength: &minLen,
	}
	if defaultVal != nil {
		cfg.Default = defaultVal
	}
	return &reliantv1.Input{
		Type:   "string",
		Config: &reliantv1.Input_StringInput{StringInput: cfg},
	}
}

// numberInput creates a number input with optional min, max, and default.
func numberInput(min, max, defaultVal *float64) *reliantv1.Input {
	cfg := &reliantv1.NumberInputConfig{}
	if min != nil {
		cfg.Min = min
	}
	if max != nil {
		cfg.Max = max
	}
	if defaultVal != nil {
		cfg.Default = defaultVal
	}
	return &reliantv1.Input{
		Type:   "number",
		Config: &reliantv1.Input_NumberInput{NumberInput: cfg},
	}
}

// integerInput creates an integer input with an optional default.
func integerInput(defaultVal *int64) *reliantv1.Input {
	cfg := &reliantv1.IntegerInputConfig{}
	if defaultVal != nil {
		cfg.Default = defaultVal
	}
	return &reliantv1.Input{
		Type:   "integer",
		Config: &reliantv1.Input_IntegerInput{IntegerInput: cfg},
	}
}

func integerInputWithBounds(min, max *int64, defaultVal *int64) *reliantv1.Input {
	cfg := &reliantv1.IntegerInputConfig{}
	if min != nil {
		cfg.Min = min
	}
	if max != nil {
		cfg.Max = max
	}
	if defaultVal != nil {
		cfg.Default = defaultVal
	}
	return &reliantv1.Input{
		Type:   "integer",
		Config: &reliantv1.Input_IntegerInput{IntegerInput: cfg},
	}
}

func enumInput(enumValues []string, multi bool) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "enum",
		Config: &reliantv1.Input_EnumInput{EnumInput: &reliantv1.EnumInputConfig{
			EnumValues: enumValues,
			Multi:      multi,
		}},
	}
}

func arrayInput(minItems, maxItems *int32) *reliantv1.Input {
	cfg := &reliantv1.ArrayInputConfig{}
	if minItems != nil {
		cfg.MinItems = minItems
	}
	if maxItems != nil {
		cfg.MaxItems = maxItems
	}
	return &reliantv1.Input{
		Type:   "array",
		Config: &reliantv1.Input_ArrayInput{ArrayInput: cfg},
	}
}

func objectInput(required []string, additionalProps *bool, properties map[string]*reliantv1.PropertySchema) *reliantv1.Input {
	cfg := &reliantv1.ObjectInputConfig{Required: required, Properties: properties}
	if additionalProps != nil {
		cfg.AdditionalProperties = additionalProps
	}
	return &reliantv1.Input{
		Type:   "object",
		Config: &reliantv1.Input_ObjectInput{ObjectInput: cfg},
	}
}

func presetInput(multi bool) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "preset",
		Config: &reliantv1.Input_PresetInput{PresetInput: &reliantv1.PresetInputConfig{
			Multi: multi,
		}},
	}
}

// booleanInput creates a boolean input (always required since no default).
func booleanInput() *reliantv1.Input {
	return &reliantv1.Input{
		Type:   "boolean",
		Config: &reliantv1.Input_BooleanInput{BooleanInput: &reliantv1.BooleanInputConfig{}},
	}
}

// groupInput creates a group input with nested inputs.
func groupInput(inputs map[string]*reliantv1.Input) *reliantv1.Input {
	return &reliantv1.Input{
		Type: "group",
		Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
			Inputs: inputs,
		}},
	}
}

func TestValidateInputs_ModelEmptyDefault(t *testing.T) {
	tests := []struct {
		name          string
		inputs        map[string]*reliantv1.Input
		provided      map[string]any
		wantErrors    bool
		errorContains string
	}{
		{
			name: "model with no default and no value provided - errors as required",
			inputs: map[string]*reliantv1.Input{
				"model": modelInput(nil),
			},
			provided:      map[string]any{},
			wantErrors:    true,
			errorContains: "required input 'model' is not provided",
		},
		{
			name: "model with no default but value provided - passes",
			inputs: map[string]*reliantv1.Input{
				"model": modelInput(nil),
			},
			provided:   map[string]any{"model": map[string]interface{}{"id": "claude-4-sonnet"}},
			wantErrors: false,
		},
		{
			name: "string with empty default and no value - passes (empty is valid for strings)",
			inputs: map[string]*reliantv1.Input{
				"name": stringInput(ptr.Of("")),
			},
			provided:   map[string]any{},
			wantErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{
				Name:   "test",
				Inputs: tt.inputs,
			}

			result := ValidateInputs(wf, tt.provided)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" && result.HasErrors() {
				errStr := result.Error()
				if !strings.Contains(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

// TestValidateInputs_NestedGroupValidation tests that inputs nested within groups
// are properly validated, including scenarios where model is inside a group.
func TestValidateInputs_NestedGroupValidation(t *testing.T) {
	tests := []struct {
		name          string
		inputs        map[string]*reliantv1.Input
		provided      map[string]any
		wantErrors    bool
		errorContains string
	}{
		{
			name: "nested model with no default and no value provided - errors as required",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided:      map[string]any{},
			wantErrors:    true,
			errorContains: "required input 'agent.model' is not provided",
		},
		{
			name: "nested model with no default but value provided in group - passes",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided: map[string]any{
				"agent": map[string]any{
					"model": map[string]interface{}{"id": "claude-4-sonnet"},
				},
			},
			wantErrors: false,
		},
		{
			name: "nested model without default - errors (requires value)",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided:      map[string]any{},
			wantErrors:    true,
			errorContains: "required input 'agent.model' is not provided",
		},
		{
			name: "unknown nested input in group - errors",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided: map[string]any{
				"agent": map[string]any{
					"model":   map[string]interface{}{"id": "claude-4-sonnet"},
					"unknown": "bad",
				},
			},
			wantErrors:    true,
			errorContains: "unknown input(s): agent.unknown",
		},
		{
			name: "multiple nested inputs - validates all",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
					"temperature": numberInput(
						nil, nil, nil,
					),
				}),
			},
			provided: map[string]any{
				"agent": map[string]any{
					"model":       map[string]interface{}{"id": "claude-4-sonnet"},
					"temperature": 0.7,
				},
			},
			wantErrors: false,
		},
		{
			name: "map[string]interface{} provided instead of map[string]any - works",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided: map[string]any{
				"agent": map[string]interface{}{
					"model": map[string]interface{}{"id": "claude-4-sonnet"},
				},
			},
			wantErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{
				Name:   "test",
				Inputs: tt.inputs,
			}

			result := ValidateInputs(wf, tt.provided)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" && result.HasErrors() {
				errStr := result.Error()
				if !strings.Contains(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

// float64Ptr is a helper to create a pointer to a float64 value
func float64Ptr(f float64) *float64 {
	return &f
}

// TestValidateInputs_RejectsFlatKeys tests that flat dot-notation keys are rejected.
// Inputs must be properly nested, not flat like "agent.model".
func TestValidateInputs_RejectsFlatKeys(t *testing.T) {
	tests := []struct {
		name          string
		inputs        map[string]*reliantv1.Input
		provided      map[string]any
		wantErrors    bool
		errorContains string
	}{
		{
			name: "flat key agent.model is rejected",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided: map[string]any{
				"agent.model": map[string]interface{}{"id": "claude-4-sonnet"}, // Flat key - should be rejected
			},
			wantErrors:    true,
			errorContains: "flat input keys are not allowed",
		},
		{
			name: "multiple flat keys are rejected",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model":       modelInput(nil),
					"temperature": numberInput(nil, nil, nil),
				}),
			},
			provided: map[string]any{
				"agent.model":       map[string]interface{}{"id": "claude-4-sonnet"},
				"agent.temperature": 0.7,
			},
			wantErrors:    true,
			errorContains: "flat input keys are not allowed",
		},
		{
			name: "nested structure is accepted",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided: map[string]any{
				"agent": map[string]any{
					"model": map[string]interface{}{"id": "claude-4-sonnet"},
				},
			},
			wantErrors: false,
		},
		{
			name: "top-level inputs without dots are accepted",
			inputs: map[string]*reliantv1.Input{
				"model":       modelInput(nil),
				"temperature": numberInput(nil, nil, nil),
			},
			provided: map[string]any{
				"model":       map[string]interface{}{"id": "claude-4-sonnet"},
				"temperature": 0.7,
			},
			wantErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{
				Name:   "test",
				Inputs: tt.inputs,
			}

			result := ValidateInputs(wf, tt.provided)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" && result.HasErrors() {
				errStr := result.Error()
				if !strings.Contains(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

// TestValidateInputs_TypeValidation tests type checking for various input types.
func TestValidateInputs_TypeValidation(t *testing.T) {
	validStr := "test"
	validInt := int64(10)
	validFloat := 0.5

	tests := []struct {
		name          string
		inputs        map[string]*reliantv1.Input
		provided      map[string]any
		wantErrors    bool
		errorContains string
	}{
		{
			name: "string type - valid",
			inputs: map[string]*reliantv1.Input{
				"name": stringInput(nil),
			},
			provided:   map[string]any{"name": "hello"},
			wantErrors: false,
		},
		{
			name: "string type - wrong type",
			inputs: map[string]*reliantv1.Input{
				"name": stringInput(nil),
			},
			provided:      map[string]any{"name": 123},
			wantErrors:    true,
			errorContains: "expects string",
		},
		{
			name: "integer type - valid",
			inputs: map[string]*reliantv1.Input{
				"count": integerInput(&validInt),
			},
			provided:   map[string]any{"count": 5},
			wantErrors: false,
		},
		{
			name: "integer type - wrong type",
			inputs: map[string]*reliantv1.Input{
				"count": integerInput(&validInt),
			},
			provided:      map[string]any{"count": "five"},
			wantErrors:    true,
			errorContains: "expects integer",
		},
		{
			name: "number type - valid",
			inputs: map[string]*reliantv1.Input{
				"temp": numberInput(nil, nil, &validFloat),
			},
			provided:   map[string]any{"temp": 0.7},
			wantErrors: false,
		},
		{
			name: "boolean type - valid",
			inputs: map[string]*reliantv1.Input{
				"enabled": booleanInput(),
			},
			provided:   map[string]any{"enabled": true},
			wantErrors: false,
		},
		{
			name: "boolean type - wrong type",
			inputs: map[string]*reliantv1.Input{
				"enabled": booleanInput(),
			},
			provided:      map[string]any{"enabled": "true"},
			wantErrors:    true,
			errorContains: "expects boolean",
		},
		{
			name: "model type - no default with value provided passes",
			inputs: map[string]*reliantv1.Input{
				"model": modelInput(nil),
			},
			// In proto validation, model type expects a string value
			provided:   map[string]any{"model": map[string]interface{}{"id": "claude-4-sonnet"}},
			wantErrors: false,
		},
		{
			name: "string with default and no value - passes",
			inputs: map[string]*reliantv1.Input{
				"name": stringInput(&validStr),
			},
			provided:   map[string]any{},
			wantErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{
				Name:   "test",
				Inputs: tt.inputs,
			}

			result := ValidateInputs(wf, tt.provided)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" && result.HasErrors() {
				errStr := result.Error()
				if !strings.Contains(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

// TestValidateInputs_UnknownInputs tests that unknown inputs are rejected.
func TestValidateInputs_UnknownInputs(t *testing.T) {
	tests := []struct {
		name          string
		inputs        map[string]*reliantv1.Input
		provided      map[string]any
		wantErrors    bool
		errorContains string
	}{
		{
			name: "unknown top-level input rejected",
			inputs: map[string]*reliantv1.Input{
				"model": stringInput(ptr.Of("default")),
			},
			provided: map[string]any{
				"model":   "claude-4-sonnet",
				"unknown": "bad",
			},
			wantErrors:    true,
			errorContains: "unknown input(s): unknown",
		},
		{
			name: "unknown nested input in group rejected",
			inputs: map[string]*reliantv1.Input{
				"agent": groupInput(map[string]*reliantv1.Input{
					"model": modelInput(nil),
				}),
			},
			provided: map[string]any{
				"agent": map[string]any{
					"model":   map[string]interface{}{"id": "claude-4-sonnet"},
					"unknown": "bad",
				},
			},
			wantErrors:    true,
			errorContains: "unknown input(s): agent.unknown",
		},
		{
			name: "all known inputs pass",
			inputs: map[string]*reliantv1.Input{
				"model": stringInput(ptr.Of("default")),
				"agent": groupInput(map[string]*reliantv1.Input{
					"temperature": numberInput(nil, nil, nil),
				}),
			},
			provided: map[string]any{
				"model": "claude-4-sonnet",
				"agent": map[string]any{
					"temperature": 0.7,
				},
			},
			wantErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{
				Name:   "test",
				Inputs: tt.inputs,
			}

			result := ValidateInputs(wf, tt.provided)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" && result.HasErrors() {
				errStr := result.Error()
				if !strings.Contains(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

// TestValidateInputs_MissingRequired tests that missing required inputs are detected.
func TestValidateInputs_MissingRequired(t *testing.T) {
	tests := []struct {
		name          string
		inputs        map[string]*reliantv1.Input
		provided      map[string]any
		wantErrors    bool
		errorContains string
	}{
		{
			name: "missing required top-level input",
			inputs: map[string]*reliantv1.Input{
				"name": stringInput(nil), // No default = required
			},
			provided:      map[string]any{},
			wantErrors:    true,
			errorContains: "required input 'name' is not provided",
		},
		{
			name: "missing required nested input in group",
			inputs: map[string]*reliantv1.Input{
				"config": groupInput(map[string]*reliantv1.Input{
					"name": stringInput(nil), // No default = required
				}),
			},
			provided:      map[string]any{},
			wantErrors:    true,
			errorContains: "required input 'config.name' is not provided",
		},
		{
			name: "missing required nested input - group provided but field missing",
			inputs: map[string]*reliantv1.Input{
				"config": groupInput(map[string]*reliantv1.Input{
					"name": stringInput(nil),           // No default = required
					"temp": numberInput(nil, nil, nil), // No default = required
				}),
			},
			provided: map[string]any{
				"config": map[string]any{
					"temp": 0.7, // name is missing
				},
			},
			wantErrors:    true,
			errorContains: "required input 'config.name' is not provided",
		},
		{
			name: "optional with default - not required",
			inputs: map[string]*reliantv1.Input{
				"name": stringInput(ptr.Of("default-name")),
			},
			provided:   map[string]any{},
			wantErrors: false,
		},
		{
			name: "all required inputs provided",
			inputs: map[string]*reliantv1.Input{
				"model": modelInput(nil),
				"agent": groupInput(map[string]*reliantv1.Input{
					"temperature": numberInput(nil, nil, nil),
				}),
			},
			provided: map[string]any{
				"model": map[string]interface{}{"id": "claude-4-sonnet"},
				"agent": map[string]any{
					"temperature": 0.7,
				},
			},
			wantErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &reliantv1.Workflow{
				Name:   "test",
				Inputs: tt.inputs,
			}

			result := ValidateInputs(wf, tt.provided)

			if tt.wantErrors && !result.HasErrors() {
				t.Errorf("expected errors but got none")
			}
			if !tt.wantErrors && result.HasErrors() {
				t.Errorf("unexpected errors: %s", result.Error())
			}
			if tt.errorContains != "" && result.HasErrors() {
				errStr := result.Error()
				if !strings.Contains(errStr, tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, errStr)
				}
			}
		})
	}
}

func TestValidateInputs_ModelSelectorAdversarialCases(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name: "model-selector-adversarial",
		Inputs: map[string]*reliantv1.Input{
			"model": modelInput(nil),
		},
	}

	tests := []struct {
		name          string
		provided      map[string]any
		errorContains string
	}{
		{
			name:          "model selector object missing id and tags is rejected",
			provided:      map[string]any{"model": map[string]interface{}{"providers": []interface{}{"openai"}}},
			errorContains: "model selector expects 'id' or 'tags'",
		},
		{
			name:          "model selector as empty string is rejected",
			provided:      map[string]any{"model": ""},
			errorContains: "model must be an object",
		},
		{
			name:          "model selector as non-empty string is rejected",
			provided:      map[string]any{"model": "gpt-4o"},
			errorContains: "model must be an object",
		},
		{
			name:          "model selector as boolean is rejected",
			provided:      map[string]any{"model": true},
			errorContains: "expects model selector object",
		},
		{
			name:          "model selector as map-any with unknown shape is rejected",
			provided:      map[string]any{"model": map[string]interface{}{"selector": "gpt-5"}},
			errorContains: "model selector expects 'id' or 'tags'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateInputs(workflow, tt.provided)
			if !result.HasErrors() {
				t.Fatalf("expected validation error for %s", tt.name)
			}
			if !strings.Contains(result.Error(), tt.errorContains) {
				t.Fatalf("expected error containing %q, got %q", tt.errorContains, result.Error())
			}
		})
	}
}

func TestValidateInputs_GroupTypeMismatchAndAmbiguousShapes(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name: "group-shape-validation",
		Inputs: map[string]*reliantv1.Input{
			"agent": groupInput(map[string]*reliantv1.Input{
				"model": modelInput(nil),
			}),
		},
	}

	tests := []struct {
		name          string
		provided      map[string]any
		errorContains string
	}{
		{
			name:          "group provided as scalar still fails required nested input",
			provided:      map[string]any{"agent": "not-a-group"},
			errorContains: "required input 'agent.model' is not provided",
		},
		{
			name:          "group provided as array still fails required nested input",
			provided:      map[string]any{"agent": []interface{}{"bad-shape"}},
			errorContains: "required input 'agent.model' is not provided",
		},
		{
			name:          "group with nested model wrong type reports nested model error",
			provided:      map[string]any{"agent": map[string]any{"model": 42}},
			errorContains: "input 'agent.model': input 'model' expects model selector object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateInputs(workflow, tt.provided)
			if !result.HasErrors() {
				t.Fatalf("expected validation error for %s", tt.name)
			}
			if !strings.Contains(result.Error(), tt.errorContains) {
				t.Fatalf("expected error containing %q, got %q", tt.errorContains, result.Error())
			}
		})
	}
}

// ptrInt is a helper to create a pointer to an int value

// An enum that declares no values constrains only the shape — it must not
// reject every value with an empty "(allowed: )" list, which bricks in-flight
// workflows resumed against a schema loaded mid-edit.
func TestValidateInputs_EmptyEnumAcceptsAnyString(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name: "empty-enum",
		Inputs: map[string]*reliantv1.Input{
			"mode": enumInput(nil, false),
			"tags": enumInput([]string{}, true),
		},
	}

	result := ValidateInputs(workflow, map[string]any{"mode": "auto", "tags": []any{"x", "y"}})
	if result.HasErrors() {
		t.Fatalf("expected no errors for empty-enum inputs, got %v", result.Error())
	}

	// Shape is still enforced.
	result = ValidateInputs(workflow, map[string]any{"mode": 3, "tags": []any{"x"}})
	if !result.HasErrors() {
		t.Fatal("expected type error for non-string enum value")
	}
}

func TestValidateInputs_StrictConstraintValidation(t *testing.T) {
	workflow := &reliantv1.Workflow{
		Name: "strict-constraints",
		Inputs: map[string]*reliantv1.Input{
			"name":         stringInputWithMinLength(3, nil),
			"status":       enumInput([]string{"open", "closed"}, false),
			"status_multi": enumInput([]string{"a", "b", "c"}, true),
			"count":        integerInputWithBounds(ptr.Of(int64(1)), ptr.Of(int64(5)), nil),
			"items":        arrayInput(ptr.Of(int32(1)), ptr.Of(int32(2))),
			"config": objectInput(
				[]string{"enabled"},
				ptr.Of(false),
				map[string]*reliantv1.PropertySchema{
					"enabled": {Type: "boolean"},
					"retries": {Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(3)},
				},
			),
			"preset_single": presetInput(false),
			"preset_multi":  presetInput(true),
		},
	}

	tests := []struct {
		name          string
		provided      map[string]any
		errorContains string
	}{
		{name: "string min length", provided: map[string]any{"name": "ab", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "must be at least 3 character"},
		{name: "enum single invalid", provided: map[string]any{"name": "abcd", "status": "invalid", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "invalid enum value"},
		{name: "enum multi invalid type", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": "a", "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "expects array of enum values"},
		{name: "integer out of range", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 8, "items": []any{1}, "config": map[string]any{"enabled": true}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "must be <= 5"},
		{name: "array min items", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{}, "config": map[string]any{"enabled": true}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "requires at least 1 item"},
		{name: "object missing required property", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "missing required property"},
		{name: "object unknown property not allowed", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true, "x": 1}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "unknown property"},
		{name: "object nested property type", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true, "retries": "three"}, "preset_single": "p1", "preset_multi": []any{"p1"}}, errorContains: "expects integer"},
		{name: "preset single wrong type", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true}, "preset_single": []any{"p1"}, "preset_multi": []any{"p1"}}, errorContains: "preset_single' expects string"},
		{name: "preset multi wrong type", provided: map[string]any{"name": "abcd", "status": "open", "status_multi": []any{"a"}, "count": 2, "items": []any{1}, "config": map[string]any{"enabled": true}, "preset_single": "p1", "preset_multi": "p1"}, errorContains: "preset_multi' expects array of preset slugs"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := ValidateInputs(workflow, testCase.provided)
			if !result.HasErrors() {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(result.Error(), testCase.errorContains) {
				t.Fatalf("expected error containing %q, got %q", testCase.errorContains, result.Error())
			}
		})
	}
}
