// Copyright (c) 2025 Reliant Labs
package drivererrors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ErrEmptyInput is a sentinel error for LLM calls that have no usable input.
// Callers can use errors.Is(err, ErrEmptyInput) to classify deterministically.
var ErrEmptyInput = fmt.Errorf("llm request has no input")

// ErrNoServableProvider matches a model resolution that found no connected,
// credential-accepted provider able to serve the requested model. It is
// deterministic for the life of a retry ladder — only the user reconnecting a
// provider or picking another model changes it — so it is terminal: the call
// fails once, with its explanation, instead of retrying the same failure.
//
// For the same reason it is a USER error (svcerr.ClassUser): it logs at INFO
// and never reaches Sentry, whichever error wraps it.
var ErrNoServableProvider = svcerr.WithClass(errors.New("no servable provider"), svcerr.ClassUser)

// ErrStreamEndedEarly matches a stream that closed cleanly — no transport
// error — before the provider sent the event that terminates a response
// (`response.completed` / `response.incomplete` / `response.failed` on the
// Responses API).
//
// The stream guards in internal/llm (idle, content stall, progress, first
// token) all fire on a stream that stays OPEN without progress. This is the
// opposite fault: the stream ends, but too soon. A proxy or load
// balancer that closes an HTTP/2 stream mid-response does exactly that, and
// the SSE reader sees an orderly EOF. Treating that as a finished turn ended
// the run as a success with half-streamed text, and dropped any tool call
// whose arguments were still arriving. Codex CLI raises "stream closed before
// response.completed" for the same condition and retries it with backoff
// (codex-rs codex-api/src/sse/responses.rs); so do opencode and hermes.
//
// The runtime's classifiers (runtime.classifyError, activities.ClassifyError)
// recognise it by identity, before any text rule, and retry it; it lives here,
// in a leaf package, because internal/llm imports the workflow runtime and the
// runtime cannot import it back. It is still phrased to read as transient
// ("network"), so a copy that lost its identity is retried too. Drivers wrap it
// with what was missing.
var ErrStreamEndedEarly = errors.New("llm stream ended early: the provider closed the stream before its terminal event (a network or proxy cut the response short)")

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

const (
	// ProviderCreditExhaustedSummary is the user-facing summary for provider-side
	// quota / billing exhaustion. It deliberately does not mention Reliant credit:
	// BYO provider keys and provider subscriptions have their own billing
	// relationship, while reliant-managed credit uses a separate marker path.
	ProviderCreditExhaustedSummary = "AI provider quota or credits are exhausted — check provider billing or switch providers" //nolint:gosec // G101: user-facing message text, not a credential

	// ProviderLongContextCreditsRequiredSummary is more specific than the generic
	// quota summary because the provider is telling the user how to recover:
	// either add credits for long context, or choose a shorter-context model.
	ProviderLongContextCreditsRequiredSummary = "AI provider credits are required for long context requests — add provider credits or choose a shorter-context model"
)

// SummarizeProviderCreditExhaustion recognizes provider-side credit, quota, and
// billing exhaustion messages that are terminal within a Temporal retry ladder.
// It intentionally avoids broad strings such as "remaining credit": stream-stall
// retry guidance uses that wording while describing a retryable transport stall.
func SummarizeProviderCreditExhaustion(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "usage credits are required for long context requests") {
		return ProviderLongContextCreditsRequiredSummary
	}
	if containsProviderCreditExhaustionSignal(lower) {
		return ProviderCreditExhaustedSummary
	}
	return ""
}

// IsProviderCreditExhaustion reports whether err is provider-side billing/quota
// exhaustion. A true result means retrying the same request cannot succeed until
// the user changes billing/quota state or switches providers.
func IsProviderCreditExhaustion(err error) bool {
	if err == nil {
		return false
	}
	return SummarizeProviderCreditExhaustion(err.Error()) != ""
}

// IsProviderPromptRefusal reports whether err is the provider refusing the
// prompt under its usage policy: OpenAI's invalid_prompt, "your prompt was
// flagged as potentially violating our usage policy", and its cyber_policy,
// "this content was flagged for possible cybersecurity risk" (prod
// reliant-temporal-worker c1ef7abd, 2026-11-20, logged as an ERROR). The content is the
// user's; resending the same prompt is refused the same way, and only the user
// can change it. Matched on the provider's own code and sentence, never on a
// bare 400.
func IsProviderPromptRefusal(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, `"code":"invalid_prompt"`) ||
		strings.Contains(lower, "flagged as potentially violating our usage policy") ||
		strings.Contains(lower, `"code":"cyber_policy"`) ||
		strings.Contains(lower, "flagged for possible cybersecurity risk")
}

func containsProviderCreditExhaustionSignal(lower string) bool {
	for _, signal := range []string{
		"insufficient_quota",
		"quota_exceeded",
		"quota exceeded",
		"out of credits",
		"billing hard limit",
		"payment required",
		"exceeded your current quota",
	} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}
