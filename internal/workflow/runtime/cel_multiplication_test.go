// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
)

// TestCELMultiplicationWithMapIntegerValues validates the hypothesis that CEL evaluation
// fails when multiplying integer values from Go maps (like save_tool_results.thread_token_count * 10).
//
// This test replicates the exact pattern used in the workflow engine where:
// 1. Step outputs are stored in a map[string]interface{}
// 2. The map contains integer values (thread_token_count: 12226)
// 3. CEL conditions try to multiply these values (save_tool_results.thread_token_count * 10 > 200000 * 8)
//
// The hypothesis is that CEL may fail with "no such overload" when the integer type from the
// Go map doesn't match the expected CEL type declaration.
func TestCELMultiplicationWithMapIntegerValues(t *testing.T) {
	tests := []struct {
		name           string
		nodeOutputs    map[string]interface{}
		condition      string
		expectedResult bool
		expectError    bool
		errorContains  string
	}{
		{
			name: "multiply map integer by constant - should work with int",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": 12226, // int type
				},
			},
			condition:      "save_tool_results.thread_token_count * 10 > 200000 * 8",
			expectedResult: false, // 12226 * 10 = 122,260 which is NOT > 1,600,000
			expectError:    false,
		},
		{
			name: "multiply map integer by constant - boundary case (exactly at threshold)",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": 160000, // int type
				},
			},
			condition:      "save_tool_results.thread_token_count * 10 > 200000 * 8",
			expectedResult: false, // 160000 * 10 = 1,600,000 which is NOT > 1,600,000 (equal)
			expectError:    false,
		},
		{
			name: "multiply map integer by constant - exceeds threshold",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": 160001, // int type
				},
			},
			condition:      "save_tool_results.thread_token_count * 10 > 200000 * 8",
			expectedResult: true, // 160001 * 10 = 1,600,010 which IS > 1,600,000
			expectError:    false,
		},
		{
			name: "multiply map int64 by constant - common case from JSON unmarshalling",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": int64(12226), // int64 type (from JSON)
				},
			},
			condition:      "save_tool_results.thread_token_count * 10 > 200000 * 8",
			expectedResult: false,
			expectError:    false,
		},
		{
			name: "multiply with less-than-or-equal comparison",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": 12226, // int type
				},
			},
			condition:      "save_tool_results.thread_token_count * 10 <= 200000 * 8",
			expectedResult: true, // 12226 * 10 = 122,260 which IS <= 1,600,000
			expectError:    false,
		},
		{
			name: "nested map access with multiplication",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"metrics": map[string]interface{}{
						"token_count": 5000,
					},
				},
			},
			condition:      "save_tool_results.metrics.token_count * 10 > 40000",
			expectedResult: true, // 5000 * 10 = 50,000 which IS > 40,000
			expectError:    false,
		},
		{
			name: "type mismatch - string instead of int should error",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": "12226", // string instead of int
				},
			},
			condition:     "save_tool_results.thread_token_count * 10 > 200000 * 8",
			expectError:   true,
			errorContains: "no such overload",
		},
		{
			name: "float value multiplication - KNOWN BUG: causes 'no such overload'",
			nodeOutputs: map[string]interface{}{
				"save_tool_results": map[string]interface{}{
					"thread_token_count": 12226.5, // float64 type
				},
			},
			condition:     "save_tool_results.thread_token_count * 10 > 200000 * 8",
			expectError:   true,
			errorContains: "no such overload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build CEL context exactly as the workflow engine does
			context := buildTestCELContext(tt.nodeOutputs)

			// Evaluate the condition using the same pattern as engine.go
			result, err := evaluateTestCEL(tt.condition, context)

			// Check error expectations
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errorContains, err)
				}
				return
			}

			// Check for unexpected errors
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Check result
			if result != tt.expectedResult {
				t.Errorf("expected result %v, got %v", tt.expectedResult, result)
			}
		})
	}
}

// TestCELMultiplicationRealWorldScenario tests the exact scenario from agent.yaml
// where save_tool_results.thread_token_count is compared to determine if compaction is needed.
func TestCELMultiplicationRealWorldScenario(t *testing.T) {
	// This simulates the actual step outputs from the save_tool_results step
	// ThreadTokenCount is returned as an int from SaveMessageOutput
	nodeOutputs := map[string]interface{}{
		"save_tool_results": map[string]interface{}{
			"message_id":         "msg-abc123",
			"thread_token_count": 12226,           // This is the actual value from the test scenario
			"message_count":      5,               // Number of messages in thread
			"context_sequence":   int64(0),        // Current context sequence
			"tool_calls":         []interface{}{}, // Pass through for routing
		},
	}

	// Build CEL context as the workflow engine does
	context := buildTestCELContext(nodeOutputs)

	t.Run("compact_needed condition", func(t *testing.T) {
		// This is the condition from agent.yaml line 129
		condition := "save_tool_results.thread_token_count * 10 > 200000 * 8"

		result, err := evaluateTestCEL(condition, context)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}

		// With thread_token_count=12226, we have 12226*10=122,260
		// This should be less than 200000*8=1,600,000, so result should be false
		if result != false {
			t.Errorf("expected compaction NOT needed (false), got: %v", result)
		}
	})

	t.Run("continue_with_results condition", func(t *testing.T) {
		// This is the condition from agent.yaml line 135
		condition := "save_tool_results.thread_token_count * 10 <= 200000 * 8"

		result, err := evaluateTestCEL(condition, context)
		if err != nil {
			t.Fatalf("CEL evaluation failed: %v", err)
		}

		// With thread_token_count=12226, we have 12226*10=122,260
		// This should be less than or equal to 200000*8=1,600,000, so result should be true
		if result != true {
			t.Errorf("expected to continue with results (true), got: %v", result)
		}
	})
}

// TestCELTypeCoercion tests how CEL handles different numeric types from Go
//
// FINDINGS:
// - CEL works correctly with: int, int32, int64
// - CEL FAILS with "no such overload" for: float64, uint
//
// This is because when using decls.Dyn (dynamic type), CEL can handle signed integers
// but has issues with floating point numbers and unsigned integers when performing
// arithmetic operations like multiplication.
func TestCELTypeCoercion(t *testing.T) {
	tests := []struct {
		name        string
		value       interface{}
		condition   string
		expectError bool
	}{
		{
			name:        "Go int - works correctly",
			value:       int(100),
			condition:   "value * 10 > 500",
			expectError: false,
		},
		{
			name:        "Go int32 - works correctly",
			value:       int32(100),
			condition:   "value * 10 > 500",
			expectError: false,
		},
		{
			name:        "Go int64 - works correctly",
			value:       int64(100),
			condition:   "value * 10 > 500",
			expectError: false,
		},
		{
			name:        "Go float64 - FAILS with 'no such overload'",
			value:       float64(100.0),
			condition:   "value * 10 > 500",
			expectError: true,
		},
		{
			name:        "Go uint - FAILS with 'no such overload'",
			value:       uint(100),
			condition:   "value * 10 > 500",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := map[string]interface{}{
				"value": tt.value,
			}

			// Create CEL environment with dynamic variable
			env, err := cel.NewEnv(
				cel.Declarations(
					decls.NewVar("value", decls.Dyn), // Use Dyn to accept any type
				),
			)
			if err != nil {
				t.Fatalf("failed to create CEL environment: %v", err)
			}

			// Compile condition
			ast, issues := env.Compile(tt.condition)
			if issues != nil && issues.Err() != nil {
				if tt.expectError {
					return // Expected error
				}
				t.Fatalf("CEL compilation error: %v", issues.Err())
			}

			// Create program
			prg, err := env.Program(ast)
			if err != nil {
				if tt.expectError {
					return // Expected error
				}
				t.Fatalf("failed to create CEL program: %v", err)
			}

			// Evaluate
			out, _, err := prg.Eval(context)
			if err != nil {
				if tt.expectError {
					return // Expected error
				}
				t.Fatalf("CEL evaluation error: %v", err)
			}

			// Verify we got a boolean result
			result, ok := out.Value().(bool)
			if !ok {
				t.Errorf("expected boolean result, got %T: %v", out.Value(), out.Value())
			}

			// For all these values (100 * 10 = 1000 > 500), result should be true
			if !result {
				t.Errorf("expected true, got false")
			}

			t.Logf("Successfully evaluated %s (type: %T) with result: %v", tt.name, tt.value, result)
		})
	}
}

// ============================================================================
// HELPER FUNCTIONS (replicate workflow engine logic)
// ============================================================================

// buildTestCELContext replicates the buildCELContext function from engine.go
func buildTestCELContext(nodeOutputs map[string]interface{}) map[string]interface{} {
	context := make(map[string]interface{})

	// Add step outputs to context (matches engine.go:375-380)
	for stepID, output := range nodeOutputs {
		context[stepID] = output
	}

	// Add default namespaces that engine.go provides
	context["event"] = map[string]interface{}{}
	context["thread"] = map[string]interface{}{}
	context["model"] = map[string]interface{}{}
	context["chat"] = map[string]interface{}{}
	context["step"] = map[string]interface{}{}
	context["approval"] = map[string]interface{}{}
	context["workflow"] = map[string]interface{}{}
	context["data"] = map[string]interface{}{}
	context["output"] = map[string]interface{}{}

	return context
}

// evaluateTestCEL replicates the evaluateCEL function from engine.go:104-205
func evaluateTestCEL(condition string, context map[string]interface{}) (bool, error) {
	// Start with base variable declarations (matches engine.go:106-129)
	baseDecls := []cel.EnvOption{
		cel.Declarations(
			decls.NewVar("tool_name", decls.String),
			decls.NewVar("thread", decls.String),
			decls.NewVar("message_id", decls.String),
			decls.NewVar("tool_call_id", decls.String),
			decls.NewVar("source_step", decls.String),
			decls.NewVar("has_tools", decls.Bool),
			decls.NewVar("event", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("model", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("chat", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("step", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("approval", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("workflow", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("output", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("data", decls.NewMapType(decls.String, decls.Dyn)),
		),
	}

	// Dynamically declare variables from context (matches engine.go:131-156)
	knownVars := map[string]bool{
		"tool_name": true, "thread": true, "message_id": true, "tool_call_id": true,
		"source_step": true, "has_tools": true, "event": true,
		"model": true, "chat": true, "step": true, "approval": true,
		"workflow": true, "output": true, "data": true,
	}

	dynamicDecls := make([]cel.EnvOption, 0)
	for key := range context {
		if !knownVars[key] {
			// Declare as dynamic map type for step outputs
			dynamicDecls = append(dynamicDecls, cel.Declarations(
				decls.NewVar(key, decls.NewMapType(decls.String, decls.Dyn)),
			))
		}
	}

	// Combine base and dynamic declarations
	allDecls := append(baseDecls, dynamicDecls...)

	// Create CEL environment (matches engine.go:160-164)
	env, err := cel.NewEnv(allDecls...)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	// Parse and compile (matches engine.go:166-170)
	ast, issues := env.Compile(condition)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("CEL compilation error: %w", issues.Err())
	}

	// Create program (matches engine.go:172-176)
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL program: %w", err)
	}

	// Build evaluation context (matches engine.go:178-190)
	evalCtx := make(map[string]interface{})
	for k, v := range context {
		evalCtx[k] = v
	}

	// Ensure safe defaults
	defaults := []string{"event", "thread", "model", "chat", "step", "approval", "workflow", "data", "output"}
	for _, key := range defaults {
		if _, ok := evalCtx[key]; !ok {
			evalCtx[key] = make(map[string]interface{})
		}
	}

	// Evaluate (matches engine.go:192-196)
	out, _, err := prg.Eval(evalCtx)
	if err != nil {
		return false, fmt.Errorf("CEL evaluation error: %w", err)
	}

	// Convert to boolean (matches engine.go:198-202)
	result, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression did not return boolean, got %T", out.Value())
	}

	return result, nil
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsRec(s, substr))
}

func containsRec(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
