// Copyright (c) 2025 Reliant Labs
package core

import "time"

// ProviderBackoff is one thread's LLM-provider backoff state: whether it is
// parked in a provider's retry ladder right now, and how much of its life it has
// spent there.
//
// This is the second kind of durable wait marker in the schema. The first —
// questions, approvals, pause — parks a run on a HUMAN, and every one of those
// park points writes its marker before parking so `workflow ps` can tell waiting
// from wedged. A provider retry ladder parks a run just as completely, but it
// runs inside a single Temporal activity attempt, so it wrote nothing at all:
// no message, no step execution, no status change. A supervisor reading any
// surface saw a unit that looked like it was thinking.
type ProviderBackoff struct {
	ThreadID string
	ChatID   string
	// WaitingSince is when the current sleep began; zero when the thread is not
	// currently parked.
	WaitingSince time.Time
	// ResumeAt is when the current sleep is due to end; zero when not parked.
	ResumeAt time.Time
	// Attempt is the request number that failed and triggered the current wait.
	Attempt int64
	// MaxAttempts is the driver's retry ceiling.
	MaxAttempts int64
	// StatusCode is the provider HTTP status that triggered the wait (429, 503…).
	StatusCode int64
	// Reason is the driver's retry-decision reason ("http_429", …).
	Reason string
	// Retries is the cumulative number of provider retries on this thread.
	Retries int64
	// Provider names who imposed a usage limit ("Anthropic"), for the reasons
	// below; empty for the driver's in-activity ladder.
	Provider string
	// WaitedMs is the cumulative time this thread has spent asleep in provider
	// backoff. It survives the run so post-hoc forensics can answer what fraction
	// of a unit's life went to rate limiting.
	WaitedMs  int64
	UpdatedAt time.Time
}

// Reasons a workflow-level usage-limit wait writes (a driver's retry ladder
// writes its own, "http_429" and friends). A usage window clears in hours, so
// the run parks on a durable workflow timer instead of sleeping inside an
// activity.
const (
	// ReasonUsageLimit: the provider named when the window resets; ResumeAt is
	// that time plus a margin.
	ReasonUsageLimit = "usage_limit"
	// ReasonUsageLimitProbe: the provider named no reset, so ResumeAt is the
	// next rung of the probe ladder.
	ReasonUsageLimitProbe = "usage_limit_probe"
)

// IsUsageLimit reports whether the marker is a workflow-level usage-limit wait.
func (b ProviderBackoff) IsUsageLimit() bool {
	return b.Reason == ReasonUsageLimit || b.Reason == ReasonUsageLimitProbe
}

// Waiting reports whether the thread is parked in provider backoff right now.
func (b ProviderBackoff) Waiting() bool { return !b.WaitingSince.IsZero() }
