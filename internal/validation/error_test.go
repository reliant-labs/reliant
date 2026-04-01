// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"
)

func TestError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name: "simple error",
			err: &Error{
				Category: CategoryWorkflow,
				Severity: SeverityError,
				Message:  "invalid workflow",
			},
			expected: "[workflow]: invalid workflow",
		},
		{
			name: "error with source",
			err: &Error{
				Category: CategoryConfig,
				Severity: SeverityError,
				Source:   "config.yaml",
				Message:  "missing field",
			},
			expected: "[config] config.yaml: missing field",
		},
		{
			name: "error with path",
			err: &Error{
				Category: CategoryWorkflow,
				Severity: SeverityError,
				Source:   "parallel-compete",
				Path:     []string{"review", "builtin://agent"},
				Message:  "invalid reference",
			},
			expected: "[workflow] parallel-compete -> review -> builtin://agent: invalid reference",
		},
		{
			name: "error with field",
			err: &Error{
				Category: CategoryWorkflow,
				Severity: SeverityError,
				Source:   "my-workflow",
				Field:    "args.content",
				Message:  "references unknown step",
			},
			expected: "[workflow] my-workflow [args.content]: references unknown step",
		},
		{
			name: "error with suggestion",
			err: &Error{
				Category:   CategoryWorkflow,
				Severity:   SeverityError,
				Source:     "my-workflow",
				Message:    "unknown step 'reveiw'",
				Suggestion: "Did you mean 'review'?",
			},
			expected: "[workflow] my-workflow: unknown step 'reveiw' (Did you mean 'review'?)",
		},
		{
			name: "full error",
			err: &Error{
				Category:   CategoryWorkflow,
				Severity:   SeverityError,
				Source:     "parallel-compete",
				Path:       []string{"inject_result"},
				Field:      "args.content",
				Message:    "references 'nodes.review' but node 'review' does not exist",
				Suggestion: "Available nodes: create_worktree_1, create_worktree_2",
			},
			expected: "[workflow] parallel-compete -> inject_result [args.content]: references 'nodes.review' but node 'review' does not exist (Available nodes: create_worktree_1, create_worktree_2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.expected {
				t.Errorf("Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestError_Key(t *testing.T) {
	err1 := &Error{
		Category: CategoryWorkflow,
		Source:   "test.yaml",
		Path:     []string{"step1", "step2"},
		Field:    "input",
		Message:  "error message",
	}

	err2 := &Error{
		Category: CategoryWorkflow,
		Source:   "test.yaml",
		Path:     []string{"step1", "step2"},
		Field:    "input",
		Message:  "error message",
	}

	err3 := &Error{
		Category: CategoryWorkflow,
		Source:   "test.yaml",
		Path:     []string{"step1"},
		Field:    "input",
		Message:  "error message",
	}

	if err1.Key() != err2.Key() {
		t.Error("identical errors should have same key")
	}

	if err1.Key() == err3.Key() {
		t.Error("different errors should have different keys")
	}
}

func TestError_IsError_IsWarning(t *testing.T) {
	err := &Error{Severity: SeverityError}
	warn := &Error{Severity: SeverityWarning}

	if !err.IsError() {
		t.Error("error should return true for IsError()")
	}
	if err.IsWarning() {
		t.Error("error should return false for IsWarning()")
	}

	if warn.IsError() {
		t.Error("warning should return false for IsError()")
	}
	if !warn.IsWarning() {
		t.Error("warning should return true for IsWarning()")
	}
}

func TestBuilder(t *testing.T) {
	err := NewError(CategoryWorkflow, "test message").
		Source("workflow.yaml").
		Path("step1", "step2").
		Field("inputs").
		Suggestion("try this instead").
		Detail("key1", "value1").
		Detail("key2", 42).
		Build()

	if err.Category != CategoryWorkflow {
		t.Errorf("Category = %v, want %v", err.Category, CategoryWorkflow)
	}
	if err.Severity != SeverityError {
		t.Errorf("Severity = %v, want %v", err.Severity, SeverityError)
	}
	if err.Message != "test message" {
		t.Errorf("Message = %v, want %v", err.Message, "test message")
	}
	if err.Source != "workflow.yaml" {
		t.Errorf("Source = %v, want %v", err.Source, "workflow.yaml")
	}
	if len(err.Path) != 2 || err.Path[0] != "step1" || err.Path[1] != "step2" {
		t.Errorf("Path = %v, want [step1 step2]", err.Path)
	}
	if err.Field != "inputs" {
		t.Errorf("Field = %v, want %v", err.Field, "inputs")
	}
	if err.Suggestion != "try this instead" {
		t.Errorf("Suggestion = %v, want %v", err.Suggestion, "try this instead")
	}
	if err.Details["key1"] != "value1" {
		t.Errorf("Details[key1] = %v, want %v", err.Details["key1"], "value1")
	}
	if err.Details["key2"] != 42 {
		t.Errorf("Details[key2] = %v, want %v", err.Details["key2"], 42)
	}
}

func TestNewWarning(t *testing.T) {
	warn := NewWarning(CategoryConfig, "warning message").Build()

	if warn.Severity != SeverityWarning {
		t.Errorf("Severity = %v, want %v", warn.Severity, SeverityWarning)
	}
}

func TestConvenienceConstructors(t *testing.T) {
	tests := []struct {
		name     string
		builder  *Builder
		category Category
		severity Severity
	}{
		{"WorkflowError", WorkflowError("src", "msg"), CategoryWorkflow, SeverityError},
		{"WorkflowWarning", WorkflowWarning("src", "msg"), CategoryWorkflow, SeverityWarning},
		{"ConfigError", ConfigError("src", "msg"), CategoryConfig, SeverityError},
		{"ConfigWarning", ConfigWarning("src", "msg"), CategoryConfig, SeverityWarning},
		{"PresetError", PresetError("src", "msg"), CategoryPreset, SeverityError},
		{"PresetWarning", PresetWarning("src", "msg"), CategoryPreset, SeverityWarning},
		{"MCPError", MCPError("src", "msg"), CategoryMCP, SeverityError},
		{"MCPWarning", MCPWarning("src", "msg"), CategoryMCP, SeverityWarning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.builder.Build()
			if err.Category != tt.category {
				t.Errorf("Category = %v, want %v", err.Category, tt.category)
			}
			if err.Severity != tt.severity {
				t.Errorf("Severity = %v, want %v", err.Severity, tt.severity)
			}
			if err.Source != "src" {
				t.Errorf("Source = %v, want 'src'", err.Source)
			}
			if err.Message != "msg" {
				t.Errorf("Message = %v, want 'msg'", err.Message)
			}
		})
	}
}

func TestBuilder_Details(t *testing.T) {
	details := map[string]any{
		"available": []string{"step1", "step2"},
		"count":     3,
	}

	err := NewError(CategoryWorkflow, "test").
		Details(details).
		Build()

	if len(err.Details) != 2 {
		t.Errorf("Details length = %d, want 2", len(err.Details))
	}

	// Verify Details() replaces previous Detail() calls
	err2 := NewError(CategoryWorkflow, "test").
		Detail("key1", "value1").
		Details(details).
		Build()

	if _, exists := err2.Details["key1"]; exists {
		t.Error("Details() should replace previous Detail() calls")
	}
}
