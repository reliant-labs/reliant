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
	{"reauthentication is needed", "Local Google Cloud auth expired. Run `gcloud auth application-default login` and retry"},
	{"application-default login", "Local Google Cloud auth expired. Run `gcloud auth application-default login` and retry"},
	{"overloaded", "The AI provider is currently overloaded"},
	{"rate limit", "Rate limited by the AI provider"},
	{"too many requests", "Rate limited by the AI provider"},
	{"internal server error", "AI provider internal server error"},
	{"service unavailable", "The AI provider is temporarily unavailable"},
	{"bad gateway", "The AI provider returned a bad gateway error"},
	{"gateway timeout", "The AI provider timed out"},
	{"timeout", "Request to the AI provider timed out"},
}

// networkFailureSummary is what we tell a user whose transport never reached the
// provider. It deliberately names no provider and proposes no re-authentication:
// the credentials were never presented, so nothing about them is known.
const networkFailureSummary = "Cannot reach the AI provider — check your network connection"

// networkFailureSignals appear only when the request failed below HTTP — the
// socket was never opened, or died before a response arrived. Any of them alone
// is conclusive.
var networkFailureSignals = []string{
	"no such host",
	"dial tcp",
	"dial udp",
	"connection refused",
	"connection reset",
	"broken pipe",
	"i/o timeout",
	"network is unreachable",
	"no route to host",
	"tls handshake timeout",
}

// transportLayerSignals name the syscall that failed. They qualify errors that
// are ambiguous on their own — "context deadline exceeded" is a plain request
// timeout everywhere else in this codebase.
var transportLayerSignals = []string{"dial tcp", "dial udp", "read tcp", "write tcp"}

// isNetworkFailure reports whether the transport never produced a usable HTTP
// response. It must be consulted before any provider-specific matching: a
// provider's hostname appears in the URL of every failed request to it, so
// matching on the hostname alone reports DNS and connectivity outages as
// expired credentials and sends users to re-authenticate for no reason.
func isNetworkFailure(errLower string) bool {
	for _, signal := range networkFailureSignals {
		if strings.Contains(errLower, signal) {
			return true
		}
	}

	// A truncated stream reads as EOF. Anchor on the delimiter so an unrelated
	// word that merely ends in those three letters cannot match.
	if strings.Contains(errLower, "unexpected eof") ||
		strings.Contains(errLower, ": eof") ||
		strings.HasSuffix(errLower, " eof") {
		return true
	}

	if strings.Contains(errLower, "context deadline exceeded") {
		for _, signal := range transportLayerSignals {
			if strings.Contains(errLower, signal) {
				return true
			}
		}
	}

	return false
}

// Temporal wraps every failure in scaffolding frames naming the history event
// that failed. Those identifiers mean nothing outside the Temporal history UI
// and crowd out the actual cause. These patterns mirror the Error() methods in
// go.temporal.io/sdk/internal/error.go and must track the version in go.mod.
var temporalFrames = []*regexp.Regexp{
	regexp.MustCompile(`activity error \(type: [^,)]*, scheduledEventID: \d+, startedEventID: \d+, identity: [^)]*\): ?`),
	regexp.MustCompile(`child workflow execution error \(type: [^,)]*, workflowID: [^,)]*, runID: [^,)]*, initiatedEventID: \d+, startedEventID: \d+\): ?`),
	regexp.MustCompile(`workflow execution error \(type: [^,)]*, workflowID: [^,)]*, runID: [^)]*\): ?`),
}

// applicationErrorSuffix matches the Go error type and retry disposition that
// ApplicationError appends at every layer. Retryability is already conveyed by
// the retry badge in the UI header.
var applicationErrorSuffix = regexp.MustCompile(` \(type: [^,)]*, retryable: (?:true|false)\)`)

// cleanTemporalError strips Temporal's bookkeeping from an error string,
// leaving the causal chain that explains the failure. It returns the input
// unchanged when the scaffolding turns out to be the whole message.
func cleanTemporalError(errMsg string) string {
	if errMsg == "" {
		return errMsg
	}

	cleaned := errMsg
	for _, frame := range temporalFrames {
		cleaned = frame.ReplaceAllString(cleaned, "")
	}
	cleaned = applicationErrorSuffix.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(strings.TrimLeft(cleaned, ": "))

	if cleaned == "" {
		return errMsg
	}
	return cleaned
}

// codexBackendHost identifies a request to Codex's responses API. A 401 against
// this host is a Codex credential problem no matter how the body is worded, so
// it is the signal that lets us name Codex without letting a bare "401" from
// some other provider claim a Codex-specific message.
const codexBackendHost = "chatgpt.com/backend-api/codex"

// anthropicAPIHost identifies a request to Claude's messages API. Like
// codexBackendHost it only names the provider — it must be paired with an auth
// signal to mean anything, because this hostname appears in the URL of every
// failed Anthropic request, including overloads, 500s and outages.
const anthropicAPIHost = "api.anthropic.com"

// extractProviderReconnectSummary recognizes provider-specific auth failures that
// should surface a concrete reconnect action instead of a generic auth error.
func extractProviderReconnectSummary(errLower string) string {
	unauthorized := strings.Contains(errLower, "401 unauthorized")
	tokenExpired := strings.Contains(errLower, "token_expired")

	switch {
	case strings.Contains(errLower, "claude session expired"), strings.Contains(errLower, "please reconnect claude"):
		return "Claude session expired. Please reconnect Claude"
	case strings.Contains(errLower, "codex session expired"), strings.Contains(errLower, "please reconnect codex"), strings.Contains(errLower, "codex authentication required"):
		return "Codex session expired. Please reconnect Codex"
	case strings.Contains(errLower, codexBackendHost) && (unauthorized || tokenExpired):
		return "Codex session expired. Please reconnect Codex"
	case strings.Contains(errLower, anthropicAPIHost) && (unauthorized || tokenExpired):
		return "Claude session expired. Please reconnect Claude"
	case strings.Contains(errLower, "authentication_error"), strings.Contains(errLower, "invalid authentication credentials"):
		return "Claude session expired. Please reconnect Claude"
	case tokenExpired:
		return "Authentication token expired. Please reconnect your AI provider"
	case unauthorized:
		return "Authentication failed with the AI provider"
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

	// Before anything else: if the request never reached the provider, no
	// provider-specific claim about it can be true.
	if isNetworkFailure(errLower) {
		return networkFailureSummary
	}

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
