// Copyright (c) 2025 Reliant Labs
package activities

import (
	"errors"
	"fmt"
	"strings"

	"go.temporal.io/sdk/temporal"
)

// Error classification for smart retry behavior
// - Terminal errors: Don't retry, save error to DB, return non-retryable to Temporal
// - Transient errors: Retry with backoff (default Temporal behavior)

// TerminalError indicates an error that should not be retried
// These are business logic errors, validation failures, etc.
type TerminalError struct {
	Message string
	Cause   error
}

func (e *TerminalError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *TerminalError) Unwrap() error {
	return e.Cause
}

// NewTerminalError creates a terminal error that should not be retried
func NewTerminalError(message string, cause error) error {
	return &TerminalError{
		Message: message,
		Cause:   cause,
	}
}

// TransientError indicates an error that might succeed on retry
// These are network errors, rate limits, temporary service unavailability, etc.
type TransientError struct {
	Message string
	Cause   error
}

func (e *TransientError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *TransientError) Unwrap() error {
	return e.Cause
}

// NewTransientError creates a transient error that should be retried
func NewTransientError(message string, cause error) error {
	return &TransientError{
		Message: message,
		Cause:   cause,
	}
}

// ClassifyError determines if an error is terminal or transient
// Returns a Temporal-compatible error that controls retry behavior
func ClassifyError(err error) error {
	if err == nil {
		return nil
	}

	// Check if already classified
	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		// Return as non-retryable ApplicationError
		return temporal.NewNonRetryableApplicationError(terminalErr.Error(), "TerminalError", terminalErr.Cause)
	}

	var transientErr *TransientError
	if errors.As(err, &transientErr) {
		// Return as retryable error (normal error)
		return fmt.Errorf("%w", transientErr)
	}

	// Auto-classify based on error content
	return autoClassify(err)
}

// autoClassify attempts to automatically classify errors based on their content
func autoClassify(err error) error {
	errStr := strings.ToLower(err.Error())

	// Check for specific transient error types first (before pattern matching)
	// MalformedJSONError from streaming indicates network/API issues - always retry
	if strings.Contains(errStr, "malformed json in tool input (transient)") {
		return err // Transient - Temporal will retry
	}

	// SDK-level JSON parsing errors during streaming accumulation
	// These occur when the API sends malformed partial JSON chunks
	// Error format: "json: error calling MarshalJSON for type json.RawMessage: ..."
	if strings.Contains(errStr, "json.rawmessage") &&
		(strings.Contains(errStr, "unexpected end of json") ||
			strings.Contains(errStr, "invalid character")) {
		return err // Transient - Temporal will retry
	}

	// Terminal errors - validation, business logic, not found, etc.
	terminalPatterns := []string{
		"not found",
		"does not exist",
		"invalid",
		"malformed",
		"forbidden",
		"unauthorized",
		"permission denied",
		"quota exceeded",
		"bad request",
		"cannot be empty",
		"cannot be nil",
		"unknown model",
		"unsupported",
		// User hasn't configured an API key for any matching provider
		"no available provider",
		// CEL errors are deterministic — retrying won't help
		"cel compilation error",
		"cel evaluation error",
		"cel evaluation failed",
		"undeclared reference",
		"no matching overload",
		// Proto decode errors are deterministic
		"unknown field",
		"unable to decode",
	}

	for _, pattern := range terminalPatterns {
		if strings.Contains(errStr, pattern) {
			return temporal.NewNonRetryableApplicationError(err.Error(), "TerminalError", err)
		}
	}

	// Transient errors - network, timeout, rate limit, service issues
	transientPatterns := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"network",
		"rate limit",
		"too many requests",
		"service unavailable",
		"internal server error",
		"bad gateway",
		"gateway timeout",
		"deadline exceeded",
		"context deadline exceeded",
		"context canceled",
		"temporary failure",
		"try again",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(errStr, pattern) {
			// Return as normal error - Temporal will retry
			return err
		}
	}

	// Default: treat as transient (retryable)
	// Better to retry too much than fail permanently on recoverable errors
	return err
}

// IsTerminal checks if an error is terminal (non-retryable)
func IsTerminal(err error) bool {
	if err == nil {
		return false
	}

	// Check for TerminalError type
	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return true
	}

	// Check for Temporal ApplicationError
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Type() == "TerminalError"
	}

	return false
}

// ErrorCategory represents the category of an error
type ErrorCategory string

const (
	ErrorCategoryTerminal   ErrorCategory = "terminal"
	ErrorCategoryTransient  ErrorCategory = "transient"
	ErrorCategoryValidation ErrorCategory = "validation"
	ErrorCategoryNotFound   ErrorCategory = "not_found"
	ErrorCategoryNetwork    ErrorCategory = "network"
	ErrorCategoryRateLimit  ErrorCategory = "rate_limit"
	ErrorCategoryUnknown    ErrorCategory = "unknown"
)

// CategorizeError returns the category of an error for logging/metrics
func CategorizeError(err error) ErrorCategory {
	if err == nil {
		return ErrorCategoryUnknown
	}

	errStr := strings.ToLower(err.Error())

	if strings.Contains(errStr, "not found") || strings.Contains(errStr, "does not exist") {
		return ErrorCategoryNotFound
	}

	if strings.Contains(errStr, "invalid") || strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "cannot be empty") || strings.Contains(errStr, "cannot be nil") {
		return ErrorCategoryValidation
	}

	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "too many requests") {
		return ErrorCategoryRateLimit
	}

	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "network") ||
		strings.Contains(errStr, "connection") || strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "no such host") || strings.Contains(errStr, "dial tcp") {
		return ErrorCategoryNetwork
	}

	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return ErrorCategoryTerminal
	}

	var transientErr *TransientError
	if errors.As(err, &transientErr) {
		return ErrorCategoryTransient
	}

	return ErrorCategoryUnknown
}
