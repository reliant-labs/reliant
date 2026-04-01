// Copyright (c) 2025 Reliant Labs
package drivererrors

import "fmt"

// ErrEmptyInput is a sentinel error for LLM calls that have no usable input.
// Callers can use errors.Is(err, ErrEmptyInput) to classify deterministically.
var ErrEmptyInput = fmt.Errorf("llm request has no input")

// EmptyInputError describes a request that could not be sent because message
// conversion produced no input items.
type EmptyInputError struct {
	Provider     string
	MessageCount int
	PromptCount  int
	ToolCount    int
}

func (e *EmptyInputError) Error() string {
	return fmt.Sprintf(
		"%s request has no input items after message conversion (messages=%d prompts=%d tools=%d)",
		e.Provider,
		e.MessageCount,
		e.PromptCount,
		e.ToolCount,
	)
}

// Is enables errors.Is(err, ErrEmptyInput).
func (e *EmptyInputError) Is(target error) bool {
	return target == ErrEmptyInput
}

// NewEmptyInputError creates a typed empty-input error for request preflight.
func NewEmptyInputError(provider string, messages int, prompts int, tools int) error {
	return &EmptyInputError{
		Provider:     provider,
		MessageCount: messages,
		PromptCount:  prompts,
		ToolCount:    tools,
	}
}
