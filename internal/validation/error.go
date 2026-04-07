// Copyright (c) 2025 Reliant Labs

// Package validation provides unified error types and collectors for validating
// configuration, workflows, and other user-defined resources.
package validation

import (
	"fmt"
	"strings"
)

// Category categorizes validation errors by their source.
type Category string

const (
	CategoryPreset   Category = "preset"
	CategoryWorkflow Category = "workflow"
	CategoryMCP      Category = "mcp"
	CategoryConfig   Category = "config"
)

// Severity indicates how serious the error is.
type Severity string

const (
	SeverityError   Severity = "error"   // Prevents functionality
	SeverityWarning Severity = "warning" // Degraded but functional
)

// Error represents a validation error that should be surfaced to users.
// This unified type replaces ConfigError, WorkflowTreeError, and similar types.
type Error struct {
	// Category categorizes the error source (preset, workflow, mcp, config)
	Category Category `json:"category"`

	// Severity indicates if this is an error or warning
	Severity Severity `json:"severity"`

	// Source is the file path or identifier where the error originated
	Source string `json:"source"`

	// Path is the location within the source (e.g., workflow path for nested workflows)
	// For workflows: ["parent-workflow", "child-step", "builtin://agent"]
	// For configs: ["servers", "github", "command"]
	Path []string `json:"path,omitempty"`

	// Field is the specific field that has the error (e.g., "message", "inputs.content")
	Field string `json:"field,omitempty"`

	// Message is a human-readable description of the error
	Message string `json:"message"`

	// Details provides additional context (e.g., missing param names, valid options)
	Details map[string]any `json:"details,omitempty"`

	// Suggestion provides a recommended fix (e.g., "Did you mean 'nodes.review'?")
	Suggestion string `json:"suggestion,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	var sb strings.Builder

	// Category prefix
	fmt.Fprintf(&sb, "[%s]", e.Category)

	// Source
	if e.Source != "" {
		fmt.Fprintf(&sb, " %s", e.Source)
	}

	// Path (for nested locations)
	if len(e.Path) > 0 {
		fmt.Fprintf(&sb, " -> %s", strings.Join(e.Path, " -> "))
	}

	// Field
	if e.Field != "" {
		fmt.Fprintf(&sb, " [%s]", e.Field)
	}

	// Message
	fmt.Fprintf(&sb, ": %s", e.Message)

	// Suggestion
	if e.Suggestion != "" {
		fmt.Fprintf(&sb, " (%s)", e.Suggestion)
	}

	return sb.String()
}

// Key returns a unique key for deduplication.
// Errors with the same key are considered duplicates.
func (e *Error) Key() string {
	pathStr := strings.Join(e.Path, "|")
	return fmt.Sprintf("%s|%s|%s|%s|%s", e.Category, e.Source, pathStr, e.Field, e.Message)
}

// IsError returns true if this is an error (not a warning).
func (e *Error) IsError() bool {
	return e.Severity == SeverityError
}

// IsWarning returns true if this is a warning (not an error).
func (e *Error) IsWarning() bool {
	return e.Severity == SeverityWarning
}

// Builder provides a fluent API for constructing validation errors.
type Builder struct {
	err *Error
}

// NewError creates a new validation error builder with the given category and message.
func NewError(category Category, message string) *Builder {
	return &Builder{
		err: &Error{
			Category: category,
			Severity: SeverityError,
			Message:  message,
		},
	}
}

// NewWarning creates a new validation warning builder with the given category and message.
func NewWarning(category Category, message string) *Builder {
	return &Builder{
		err: &Error{
			Category: category,
			Severity: SeverityWarning,
			Message:  message,
		},
	}
}

// Source sets the source file/identifier.
func (b *Builder) Source(source string) *Builder {
	b.err.Source = source
	return b
}

// Path sets the path within the source.
func (b *Builder) Path(path ...string) *Builder {
	b.err.Path = path
	return b
}

// Field sets the specific field.
func (b *Builder) Field(field string) *Builder {
	b.err.Field = field
	return b
}

// Suggestion sets a recommended fix.
func (b *Builder) Suggestion(suggestion string) *Builder {
	b.err.Suggestion = suggestion
	return b
}

// Detail adds a single detail key-value pair.
func (b *Builder) Detail(key string, value any) *Builder {
	if b.err.Details == nil {
		b.err.Details = make(map[string]any)
	}
	b.err.Details[key] = value
	return b
}

// Details sets all details at once.
func (b *Builder) Details(details map[string]any) *Builder {
	b.err.Details = details
	return b
}

// Build returns the constructed error.
func (b *Builder) Build() *Error {
	return b.err
}

// Convenience constructors for common error patterns

// WorkflowError creates a workflow validation error.
func WorkflowError(source, message string) *Builder {
	return NewError(CategoryWorkflow, message).Source(source)
}

// WorkflowWarning creates a workflow validation warning.
func WorkflowWarning(source, message string) *Builder {
	return NewWarning(CategoryWorkflow, message).Source(source)
}

// ConfigError creates a config validation error.
func ConfigError(source, message string) *Builder {
	return NewError(CategoryConfig, message).Source(source)
}

// ConfigWarning creates a config validation warning.
func ConfigWarning(source, message string) *Builder {
	return NewWarning(CategoryConfig, message).Source(source)
}

// PresetError creates a preset validation error.
func PresetError(source, message string) *Builder {
	return NewError(CategoryPreset, message).Source(source)
}

// PresetWarning creates a preset validation warning.
func PresetWarning(source, message string) *Builder {
	return NewWarning(CategoryPreset, message).Source(source)
}

// MCPError creates an MCP validation error.
func MCPError(source, message string) *Builder {
	return NewError(CategoryMCP, message).Source(source)
}

// MCPWarning creates an MCP validation warning.
func MCPWarning(source, message string) *Builder {
	return NewWarning(CategoryMCP, message).Source(source)
}
