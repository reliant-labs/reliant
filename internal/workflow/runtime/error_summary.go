package runtime

import (
	"fmt"
	"regexp"
	"strings"
)

// llmAPIErrorJSON matches the JSON error payload from Anthropic/LLM streaming errors.
// Example: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}
var llmAPIErrorJSON = regexp.MustCompile(`\{"type":"error","error":\{[^}]*"type":"([^"]+)","message":"([^"]+)"\}`)

// knownAPIErrorTypes maps API error type strings to user-friendly descriptions.
var knownAPIErrorTypes = map[string]string{
	"overloaded_error":      "The AI provider is currently overloaded",
	"rate_limit_error":      "Rate limited by the AI provider",
	"api_error":             "AI provider internal server error",
	"authentication_error":  "Authentication failed with the AI provider",
	"permission_error":      "Permission denied by the AI provider",
	"not_found_error":       "Model or resource not found at the AI provider",
	"request_too_large":     "Request too large for the AI provider",
	"invalid_request_error": "Invalid request sent to the AI provider",
}

// knownErrorPatterns matches common error substrings to user-friendly descriptions.
var knownErrorPatterns = []struct {
	pattern string
	summary string
}{
	{"overloaded", "The AI provider is currently overloaded"},
	{"rate limit", "Rate limited by the AI provider"},
	{"too many requests", "Rate limited by the AI provider"},
	{"internal server error", "AI provider internal server error"},
	{"service unavailable", "The AI provider is temporarily unavailable"},
	{"bad gateway", "The AI provider returned a bad gateway error"},
	{"gateway timeout", "The AI provider timed out"},
	{"timeout", "Request to the AI provider timed out"},
	{"connection refused", "Could not connect to the AI provider"},
	{"connection reset", "Connection to the AI provider was reset"},
}

// extractProviderReconnectSummary recognizes provider-specific auth failures that
// should surface a concrete reconnect action instead of a generic auth error.
func extractProviderReconnectSummary(errLower string) string {
	switch {
	case strings.Contains(errLower, "claude session expired"), strings.Contains(errLower, "please reconnect claude"):
		return "Claude session expired. Please reconnect Claude"
	case strings.Contains(errLower, "codex session expired"), strings.Contains(errLower, "please reconnect codex"), strings.Contains(errLower, "codex authentication required"):
		return "Codex session expired. Please reconnect Codex"
	case strings.Contains(errLower, "api.anthropic.com"), strings.Contains(errLower, "authentication_error"), strings.Contains(errLower, "invalid authentication credentials"):
		return "Claude session expired. Please reconnect Claude"
	default:
		return ""
	}
}

// extractLLMErrorSummary extracts a clean, user-friendly error summary from a
// potentially deeply-nested error string. It looks for embedded JSON error payloads
// from LLM APIs (e.g. Anthropic's {"type":"error","error":{"type":"overloaded_error",...}})
// and known error patterns.
//
// Returns an empty string if no recognizable LLM error is found.
func extractLLMErrorSummary(errMsg string) string {
	errLower := strings.ToLower(errMsg)
	if reconnectSummary := extractProviderReconnectSummary(errLower); reconnectSummary != "" {
		return reconnectSummary
	}

	// First, try to extract from embedded JSON error payload
	if matches := llmAPIErrorJSON.FindStringSubmatch(errMsg); len(matches) == 3 {
		errorType := matches[1]
		errorMessage := matches[2]

		if friendly, ok := knownAPIErrorTypes[errorType]; ok {
			return fmt.Sprintf("%s (%s)", friendly, errorMessage)
		}
		// Unknown error type but we have the JSON — still better than the raw mess
		return fmt.Sprintf("AI provider error: %s", errorMessage)
	}

	// Fallback: check for known error patterns in the raw string
	for _, kp := range knownErrorPatterns {
		if strings.Contains(errLower, kp.pattern) {
			return kp.summary
		}
	}

	return ""
}
