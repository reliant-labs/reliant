// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"strings"
)

// Severity indicates whether a validation issue is an error or warning.
type Severity int

const (
	// SeverityError indicates validation failed - workflow cannot be used.
	SeverityError Severity = iota
	// SeverityWarning indicates a potential issue that doesn't prevent execution.
	SeverityWarning
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "unknown"
	}
}

// Category classifies validation errors for filtering and handling.
type Category string

const (
	CategoryStructure         Category = "structure"          // Graph structure, edges, nodes
	CategoryCELSyntax         Category = "cel_syntax"         // CEL expression syntax errors
	CategoryCELSemantic       Category = "cel_semantic"       // CEL semantic errors (unknown fields, etc.)
	CategoryInput             Category = "input"              // Input validation errors
	CategoryOutput            Category = "output"             // Output reference errors
	CategoryCrossWorkflow     Category = "cross_workflow"     // Child workflow contract errors
	CategoryParallelWrite     Category = "parallel_write"     // Thread conflict errors
	CategoryActivityConfig    Category = "activity_config"    // Activity input field errors
	CategoryRuntime           Category = "runtime"            // Runtime validation errors
	CategoryConditionalAccess Category = "conditional_access" // Unsafe access to conditional node outputs
	CategoryNodeOrdering      Category = "node_ordering"      // nodes.<id> references to nodes not guaranteed to have executed
)

// Error represents a single validation issue.
type Error struct {
	Severity   Severity // Error or Warning
	Category   Category // Classification of the error
	Path       []string // Location path (e.g., ["workflow", "nodes", "call_llm", "args", "model"])
	Field      string   // Specific field name if applicable
	Message    string   // Human-readable error message
	Suggestion string   // Suggested fix (optional)
}

// Error implements the error interface.
func (e *Error) Error() string {
	var sb strings.Builder

	// Build path string
	if len(e.Path) > 0 {
		sb.WriteString(strings.Join(e.Path, "."))
		if e.Field != "" {
			sb.WriteString(".")
			sb.WriteString(e.Field)
		}
		sb.WriteString(": ")
	} else if e.Field != "" {
		sb.WriteString(e.Field)
		sb.WriteString(": ")
	}

	sb.WriteString(e.Message)

	if e.Suggestion != "" {
		sb.WriteString(" (")
		sb.WriteString(e.Suggestion)
		sb.WriteString(")")
	}

	return sb.String()
}

// Result holds all validation errors and warnings from a validation pass.
type Result struct {
	errors []*Error
}

// NewResult creates an empty validation result.
func NewResult() *Result {
	return &Result{
		errors: make([]*Error, 0),
	}
}

// Add adds a validation error to the result.
func (r *Result) Add(err *Error) {
	r.errors = append(r.errors, err)
}

// AddError is a convenience method to add an error with common fields.
func (r *Result) AddError(category Category, path []string, field, message string) {
	r.Add(&Error{
		Severity: SeverityError,
		Category: category,
		Path:     path,
		Field:    field,
		Message:  message,
	})
}

// AddErrorWithSuggestion adds an error with a suggested fix.
func (r *Result) AddErrorWithSuggestion(category Category, path []string, field, message, suggestion string) {
	r.Add(&Error{
		Severity:   SeverityError,
		Category:   category,
		Path:       path,
		Field:      field,
		Message:    message,
		Suggestion: suggestion,
	})
}

// AddWarning is a convenience method to add a warning.
func (r *Result) AddWarning(category Category, path []string, field, message string) {
	r.Add(&Error{
		Severity: SeverityWarning,
		Category: category,
		Path:     path,
		Field:    field,
		Message:  message,
	})
}

// Merge combines another result into this one.
func (r *Result) Merge(other *Result) {
	if other == nil {
		return
	}
	r.errors = append(r.errors, other.errors...)
}

// HasErrors returns true if there are any errors (not warnings).
func (r *Result) HasErrors() bool {
	for _, err := range r.errors {
		if err.Severity == SeverityError {
			return true
		}
	}
	return false
}

// HasWarnings returns true if there are any warnings.
func (r *Result) HasWarnings() bool {
	for _, err := range r.errors {
		if err.Severity == SeverityWarning {
			return true
		}
	}
	return false
}

// HasAny returns true if there are any errors or warnings.
func (r *Result) HasAny() bool {
	return len(r.errors) > 0
}

// Errors returns only the errors (not warnings).
func (r *Result) Errors() []*Error {
	var errs []*Error
	for _, err := range r.errors {
		if err.Severity == SeverityError {
			errs = append(errs, err)
		}
	}
	return errs
}

// Warnings returns only the warnings.
func (r *Result) Warnings() []*Error {
	var warnings []*Error
	for _, err := range r.errors {
		if err.Severity == SeverityWarning {
			warnings = append(warnings, err)
		}
	}
	return warnings
}

// All returns all errors and warnings.
func (r *Result) All() []*Error {
	return r.errors
}

// ByCategory returns errors/warnings filtered by category.
func (r *Result) ByCategory(category Category) []*Error {
	var filtered []*Error
	for _, err := range r.errors {
		if err.Category == category {
			filtered = append(filtered, err)
		}
	}
	return filtered
}

// Error implements the error interface for Result.
// Returns nil if there are no errors.
func (r *Result) Error() string {
	if !r.HasErrors() {
		return ""
	}

	var sb strings.Builder
	errs := r.Errors()
	fmt.Fprintf(&sb, "validation failed with %d error(s):\n", len(errs))

	for i, err := range errs {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, err.Error())
	}

	return sb.String()
}

// AsError returns the Result as an error if it has errors, nil otherwise.
// Useful for returning from functions that return error.
func (r *Result) AsError() error {
	if r.HasErrors() {
		return r
	}
	return nil
}
