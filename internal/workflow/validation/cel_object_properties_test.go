package validation

import (
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateCEL_ObjectPropertyAccess tests that CEL validation properly validates
// access to object input properties defined in JSON Schema.
func TestValidateCEL_ObjectPropertyAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		workflow  string
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid property access on object input",
			workflow: `
name: test-object-property
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
      enabled:
        type: boolean
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - timeout exists in config schema
    condition: "inputs.config.timeout > 0"
`,
			wantError: false,
		},
		{
			name: "access to undefined property should error",
			workflow: `
name: test-undefined-property
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should error - unknown_field not in config schema
    condition: "inputs.config.unknown_field > 0"
`,
			wantError: true,
			errorMsg:  "unknown_field",
		},
		{
			name: "type checking for boolean property",
			workflow: `
name: test-boolean-property
entry: [step1]
inputs:
  config:
    type: object
    properties:
      enabled:
        type: boolean
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - enabled is boolean
    condition: "inputs.config.enabled == true"
`,
			wantError: false,
		},
		// TODO: Type checking for object property operations requires typed CEL variables.
		// Currently inputs are declared as DynType, so type mismatches aren't caught.
		// This test is disabled until we implement typed input declarations.
		// {
		// 	name: "type mismatch - using boolean as string",
		// 	workflow: `
		// name: test-type-mismatch
		// entry: [step1]		// inputs:
		//   config:
		//     type: object
		//     properties:
		//       enabled:
		//         type: boolean
		// nodes:
		//   - id: step1
		//     type: call_llm
		//     model:
		//       tags: [flagship]
		// outputs:
		//   # Should warn/error - enabled is boolean, not string
		//   message: "Status: {{inputs.config.enabled + ' done'}}"
		// `,
		// 	wantError: true,
		// 	errorMsg:  "type",
		// },
		{
			name: "nested object property access",
			workflow: `
name: test-nested-object
entry: [step1]
inputs:
  settings:
    type: object
    properties:
      database:
        type: object
        properties:
          host:
            type: string
          port:
            type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - nested property access
    condition: "inputs.settings.database.port == 5432"
`,
			wantError: false,
		},
		{
			name: "object input without schema - dynamic access allowed",
			workflow: `
name: test-no-schema
entry: [step1]
inputs:
  data:
    type: object
    # No properties defined - schema-less object
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - no schema means any field access is allowed
    condition: "has(inputs.data.anything)"
`,
			wantError: false,
		},
		{
			name: "complex expression with multiple property accesses",
			workflow: `
name: test-complex-expression
entry: [step1]
inputs:
  review_schema:
    type: object
    required: [verdict, findings]
    properties:
      verdict:
        type: boolean
      confidence:
        type: integer
        minimum: 1
        maximum: 10
      findings:
        type: string
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
  - id: step2
    type: call_llm
    model:
      tags: [flagship]
edges:
  - from: step1
    cases:
      - to: step2
        # Complex condition using multiple properties
        condition: "inputs.review_schema.verdict == true && inputs.review_schema.confidence >= 8"
`,
			wantError: false,
		},
		{
			name: "string property with length check",
			workflow: `
name: test-string-property
entry: [step1]
inputs:
  user:
    type: object
    properties:
      name:
        type: string
        minLength: 3
        maxLength: 50
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - size() on string property
    condition: "size(inputs.user.name) > 0"
`,
			wantError: false,
		},
		{
			name: "integer property with arithmetic",
			workflow: `
name: test-integer-arithmetic
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
        minimum: 0
        maximum: 300
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  # Should be valid - arithmetic on integer property
  timeout_seconds: "{{inputs.config.timeout * 1000}}"
`,
			wantError: false,
		},
		{
			name: "enum property validation",
			workflow: `
name: test-enum-property
entry: [step1]
inputs:
  request:
    type: object
    properties:
      priority:
        type: string
        enum: [low, medium, high, critical]
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - comparing enum value
    condition: "inputs.request.priority == 'high' || inputs.request.priority == 'critical'"
`,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.workflow))
			require.NoError(t, err, "failed to parse workflow YAML")

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.wantError {
				require.NotEmpty(t, errors, "expected validation errors")
				if tt.errorMsg != "" {
					found := false
					for _, e := range errors {
						t.Logf("Error: %s - %s", e.Path, e.Message)
						if containsAny(e.Message, tt.errorMsg) {
							found = true
						}
					}
					assert.True(t, found, "expected error containing '%s'", tt.errorMsg)
				}
			} else {
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors")
			}
		})
	}
}

// TestValidateCEL_ObjectArrayProperties tests CEL validation for array properties in objects.
func TestValidateCEL_ObjectArrayProperties(t *testing.T) {
	t.Parallel()
	workflowYAML := `
name: test-array-property
entry: [step1]
inputs:
  data:
    type: object
    properties:
      tags:
        type: array
        items:
          type: string
      scores:
        type: array
        items:
          type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - array operations
    condition: "size(inputs.data.tags) > 0"
  - id: step2
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - checking if array contains value
    condition: "'bug' in inputs.data.tags"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors for array operations")
}

// TestValidateCEL_ObjectRequiredFields tests that required fields are properly checked.
func TestValidateCEL_ObjectRequiredFields(t *testing.T) {
	t.Parallel()
	// This test verifies that the type context properly includes required field information
	workflowYAML := `
name: test-required-fields
entry: [step1]
inputs:
  request:
    type: object
    required: [user_id, action]
    properties:
      user_id:
        type: string
      action:
        type: string
      optional_note:
        type: string
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - accessing required field
    condition: "size(inputs.request.user_id) > 0"
  - id: step2
    type: call_llm
    model:
      tags: [flagship]
    # Should be valid - using has() for optional field
    condition: "has(inputs.request.optional_note) && size(inputs.request.optional_note) > 0"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors for required fields")
}

// TestSchemaTypeChecker_TypeMismatch tests that type mismatches in operations
// are caught by the AST-based schema type checker.
func TestSchemaTypeChecker_TypeMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		workflow  string
		wantError bool
		errorMsg  string
	}{
		{
			name: "integer plus string literal - type mismatch",
			workflow: `
name: test-type-mismatch-add
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  message: "{{inputs.config.timeout + ' seconds'}}"
`,
			wantError: true,
			errorMsg:  "no matching overload", // CEL reports type errors as "found no matching overload"
		},
		{
			name: "string plus integer property - type mismatch",
			workflow: `
name: test-type-mismatch-add2
entry: [step1]
inputs:
  config:
    type: object
    properties:
      name:
        type: string
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  combined: "{{inputs.config.name + inputs.config.timeout}}"
`,
			wantError: true,
			errorMsg:  "no matching overload", // CEL reports type errors as "found no matching overload"
		},
		{
			name: "arithmetic on string - type mismatch",
			workflow: `
name: test-type-mismatch-arithmetic
entry: [step1]
inputs:
  config:
    type: object
    properties:
      name:
        type: string
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  doubled: "{{inputs.config.name * 2}}"
`,
			wantError: true,
			errorMsg:  "no matching overload", // CEL reports "no matching overload for '_*_'"
		},
		{
			name: "comparison of string and integer - type mismatch",
			workflow: `
name: test-type-mismatch-compare
entry: [step1]
inputs:
  config:
    type: object
    properties:
      name:
        type: string
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "inputs.config.name > inputs.config.timeout"
`,
			wantError: true,
			errorMsg:  "no matching overload", // CEL reports "no matching overload for '_>_'"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.workflow))
			require.NoError(t, err, "failed to parse workflow YAML")

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.wantError {
				require.NotEmpty(t, errors, "expected validation errors")
				if tt.errorMsg != "" {
					found := false
					for _, e := range errors {
						t.Logf("Error: %s - %s", e.Path, e.Message)
						if containsAny(e.Message, tt.errorMsg) {
							found = true
						}
					}
					assert.True(t, found, "expected error containing '%s'", tt.errorMsg)
				}
			} else {
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors")
			}
		})
	}
}

// TestSchemaTypeChecker_ValidOperations tests that valid operations pass type checking.
func TestSchemaTypeChecker_ValidOperations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		workflow string
	}{
		{
			name: "integer arithmetic",
			workflow: `
name: test-valid-int-arithmetic
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  doubled: "{{inputs.config.timeout * 2}}"
  offset: "{{inputs.config.timeout + 5}}"
`,
		},
		{
			name: "string concatenation",
			workflow: `
name: test-valid-string-concat
entry: [step1]
inputs:
  config:
    type: object
    properties:
      name:
        type: string
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  greeting: "{{inputs.config.name + ' is here'}}"
`,
		},
		{
			name: "integer comparison",
			workflow: `
name: test-valid-int-compare
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
      retries:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "inputs.config.timeout > 0 && inputs.config.retries < 10"
`,
		},
		{
			name: "number arithmetic",
			workflow: `
name: test-valid-number-arithmetic
entry: [step1]
inputs:
  config:
    type: object
    properties:
      rate:
        type: number
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  scaled: "{{inputs.config.rate * 1.5}}"
`,
		},
		{
			name: "mixed integer and number arithmetic with cast",
			workflow: `
name: test-valid-mixed-numeric
entry: [step1]
inputs:
  config:
    type: object
    properties:
      count:
        type: integer
      factor:
        type: number
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  result: "{{double(inputs.config.count) * inputs.config.factor}}"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.workflow))
			require.NoError(t, err, "failed to parse workflow YAML")

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			for _, e := range errors {
				t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
			}
			assert.Empty(t, errors, "expected no validation errors")
		})
	}
}

// TestSchemaTypeChecker_NestedObjects tests type checking for nested object properties.
func TestSchemaTypeChecker_NestedObjects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		workflow  string
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid nested integer comparison",
			workflow: `
name: test-nested-valid
entry: [step1]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          port:
            type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "inputs.config.database.port > 1000"
`,
			wantError: false,
		},
		{
			name: "nested string plus integer - type mismatch",
			workflow: `
name: test-nested-mismatch
entry: [step1]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          host:
            type: string
          port:
            type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
outputs:
  connection: "{{inputs.config.database.host + inputs.config.database.port}}"
`,
			wantError: true,
			errorMsg:  "no matching overload", // CEL reports type errors as "found no matching overload"
		},
		{
			name: "undefined nested property",
			workflow: `
name: test-nested-undefined
entry: [step1]
inputs:
  config:
    type: object
    properties:
      database:
        type: object
        properties:
          host:
            type: string
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "size(inputs.config.database.unknown) > 0"
`,
			wantError: true,
			errorMsg:  "undefined", // CEL type provider now catches undefined fields at compile time
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.workflow))
			require.NoError(t, err, "failed to parse workflow YAML")

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.wantError {
				require.NotEmpty(t, errors, "expected validation errors")
				if tt.errorMsg != "" {
					found := false
					for _, e := range errors {
						t.Logf("Error: %s - %s", e.Path, e.Message)
						if containsAny(e.Message, tt.errorMsg) {
							found = true
						}
					}
					assert.True(t, found, "expected error containing '%s'", tt.errorMsg)
				}
			} else {
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors")
			}
		})
	}
}

// TestSchemaTypeChecker_AdditionalProperties tests behavior when additionalProperties is set.
func TestSchemaTypeChecker_AdditionalProperties(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		workflow  string
		wantError bool
		errorMsg  string
	}{
		{
			name: "additionalProperties false - unknown field error",
			workflow: `
name: test-no-additional
entry: [step1]
inputs:
  config:
    type: object
    additional_properties: false
    properties:
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "inputs.config.unknown_field > 0"
`,
			wantError: true,
			errorMsg:  "undefined property",
		},
		{
			name: "additionalProperties true - unknown field allowed",
			workflow: `
name: test-allow-additional
entry: [step1]
inputs:
  config:
    type: object
    additional_properties: true
    properties:
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "has(inputs.config.unknown_field)"
`,
			wantError: false,
		},
		{
			name: "additionalProperties nil (default false) - unknown field error",
			workflow: `
name: test-default-no-additional
entry: [step1]
inputs:
  config:
    type: object
    properties:
      timeout:
        type: integer
nodes:
  - id: step1
    type: call_llm
    model:
      tags: [flagship]
    condition: "inputs.config.typo > 0"
`,
			wantError: true,
			errorMsg:  "undefined property",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(tt.workflow))
			require.NoError(t, err, "failed to parse workflow YAML")

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.wantError {
				require.NotEmpty(t, errors, "expected validation errors")
				if tt.errorMsg != "" {
					found := false
					for _, e := range errors {
						t.Logf("Error: %s - %s", e.Path, e.Message)
						if containsAny(e.Message, tt.errorMsg) {
							found = true
						}
					}
					assert.True(t, found, "expected error containing '%s'", tt.errorMsg)
				}
			} else {
				for _, e := range errors {
					t.Logf("Unexpected error: %s - %s", e.Path, e.Message)
				}
				assert.Empty(t, errors, "expected no validation errors")
			}
		})
	}
}
