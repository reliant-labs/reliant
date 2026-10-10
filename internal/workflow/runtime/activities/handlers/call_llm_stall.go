// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
)

// Retry policy for a stalled stream. The step's blanket policy (5 attempts,
// 1-8s apart) was written for failures that cost seconds; every stall attempt
// has already cost its whole deadline, so a stall gets its own budget.
const (
	// A stream with no content block open is the credit-exhaustion signature,
	// or a provider too overloaded to start. Retrying the identical request
	// at once rarely helps either, so attempts are spaced out, and after three
	// (~16 minutes) the chat pauses with the reason instead of spending two
	// more five-minute attempts on it.
	stallAwaitingMaxAttempts  = 3
	stallAwaitingFirstBackoff = 15 * time.Second
	stallAwaitingMaxBackoff   = time.Minute

	// A block that went silent past its whole output budget is a provider
	// that wedged mid-reply. One fresh request is worth it; the deadline is
	// long, so a second wedge pauses rather than burning another half hour.
	stallMidBlockMaxAttempts = 2
	stallMidBlockBackoff     = 5 * time.Second
)

// streamStalledErrorType is the Temporal failure type a stalled turn fails with.
const streamStalledErrorType = "ProviderStreamStalled"

// streamStalledError is CallLLM's failure for a stream the transport cut as
// stalled (an llm.StreamStallError).
//
// Its text is one sentence a person can act on, the chat marker the UI routes
// on, and the transport's diagnosis for the logs:
//
//	AI provider stopped responding: claude-5.5-sonnet on anthropic accepted the
//	request but sent no output for 5m0s (usually an exhausted subscription or an
//	overloaded provider) [RELIANT_PROVIDER_STREAM_STALLED:anthropic]: llm stream
//	content stall timeout: provider sent only keepalives for 5m0s with no
//	content block open
//
// The sentence says what happened and nothing about what happens next. The
// same text heads the row that is retrying and the row that paused the chat;
// each surface adds which it is. The old text said "retrying" on both.
type streamStalledError struct {
	stall    *llm.StreamStallError
	provider string
	model    string
	// attempt is this activity attempt, 1-based.
	attempt int32
}

func newStreamStalledError(ctx context.Context, stall *llm.StreamStallError, provider, model string) *streamStalledError {
	return &streamStalledError{stall: stall, provider: provider, model: model, attempt: activityAttempt(ctx)}
}

func (e *streamStalledError) Error() string {
	return chatmarkers.Wrap(chatmarkers.KindProviderStreamStalled, e.provider, e.sentence()) + ": " + e.stall.Error()
}

// Unwrap exposes the transport's error, so errors.Is(err,
// llm.ErrStreamContentStalled) holds for the turn's failure too.
func (e *streamStalledError) Unwrap() error {
	return e.stall
}

func (e *streamStalledError) sentence() string {
	if e.stall.Phase == llm.StallMidBlock {
		return fmt.Sprintf("%s: %s on %s went silent partway through its reply for %s",
			chatmarkers.ProviderStreamStalledLead, e.model, e.provider, e.stall.Timeout)
	}
	return fmt.Sprintf("%s: %s on %s accepted the request but sent no output for %s (usually an exhausted subscription or an overloaded provider)",
		chatmarkers.ProviderStreamStalledLead, e.model, e.provider, e.stall.Timeout)
}

// RetryAfter reports whether Temporal should try this turn again, and after
// how long. The activity boundary (internal/workflow/runtime classifyError)
// turns it into the failure's NonRetryable flag and NextRetryDelay; a
// non-retryable stall pauses the chat with this error, exactly as an exhausted
// ladder does, so the user's next message retries it.
func (e *streamStalledError) RetryAfter() (time.Duration, bool) {
	attempt := max(e.attempt, 1)
	if e.stall.Phase == llm.StallMidBlock {
		if attempt >= stallMidBlockMaxAttempts {
			return 0, false
		}
		return stallMidBlockBackoff, true
	}
	if attempt >= stallAwaitingMaxAttempts {
		return 0, false
	}
	return min(stallAwaitingFirstBackoff<<(attempt-1), stallAwaitingMaxBackoff), true
}

// TemporalErrorType names this failure on the workflow side.
func (e *streamStalledError) TemporalErrorType() string {
	return streamStalledErrorType
}

// activityAttempt is the current activity attempt, or 1 outside an activity.
func activityAttempt(ctx context.Context) int32 {
	if !activity.IsActivity(ctx) {
		return 1
	}
	return activity.GetInfo(ctx).Attempt
}
