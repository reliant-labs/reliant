package compat

import "strings"

// ClassifyError normalizes raw server/tool errors for retry policy decisions.
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ErrorKindNone
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "-32602") || strings.Contains(msg, "invalid parameters") {
		return ErrorKindInvalidParams
	}

	hasParamsPath := strings.Contains(msg, "[\"params\"") || strings.Contains(msg, "'params'") || strings.Contains(msg, " params")
	hasSchemaPath := strings.Contains(msg, "path")
	if hasParamsPath && hasSchemaPath {
		if strings.Contains(msg, "required") || (strings.Contains(msg, "expected") && strings.Contains(msg, "object")) {
			return ErrorKindSchemaMismatch
		}
	}

	return ErrorKindNonRetryable
}

// ShouldRetry decides whether to move to next attempt based on classification and index.
func ShouldRetry(kind ErrorKind, attemptIndex int, totalAttempts int) bool {
	if attemptIndex >= totalAttempts-1 {
		return false
	}
	switch kind {
	case ErrorKindInvalidParams, ErrorKindSchemaMismatch:
		return true
	default:
		return false
	}
}
