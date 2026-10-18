// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
)

// LimitInfo describes an exhausted usage window as read off a refused response.
type LimitInfo struct {
	// ResetAt is when the provider says the limit lifts; zero when not stated.
	ResetAt time.Time
	// Window names the limit hit in the vocabulary UsageLimitError renders
	// ("five_hour", "seven_day"); empty when not stated.
	Window string
	// OverageReason is why paid overage could not absorb the request
	// ("out_of_credits"); empty when not stated.
	OverageReason string
}

// UsageLimitError is a provider refusal that retrying inside one request
// cannot fix: a subscription usage window (Anthropic's 5-hour / 7-day limit,
// Codex's primary / secondary window) is exhausted, or the provider asked for
// a wait longer than a driver will sleep. Its message carries a
// chatmarkers.KindProviderUsageLimit marker, which is what makes the workflow
// fail the turn at once (no retries against a window measured in hours) and
// show this sentence in the chat instead of a generic "rate limited".
//
// The marker payload is the reset time, RFC 3339 UTC, or empty when the
// provider did not say (the contract in chatmarkers).
type UsageLimitError struct {
	// Provider is the display name in the message ("Anthropic", "Codex", "OpenAI").
	Provider   string
	StatusCode int
	// RetryAfter is the provider's Retry-After when it sent one.
	RetryAfter time.Duration
	// ResetAt is when the limit lifts; zero when unknown.
	ResetAt time.Time
	// Window is the limit that was hit ("seven_day", "five_hour"); "" when not sent.
	Window string
	// OverageReason is why paid overage could not absorb the request.
	OverageReason string
	Err           error
}

func (e *UsageLimitError) Error() string {
	detail := e.Provider
	switch e.Window {
	case "":
	case "seven_day":
		detail += ", 7-day window"
	case "five_hour":
		detail += ", 5-hour window"
	default:
		detail += ", " + strings.ReplaceAll(e.Window, "_", " ") + " window"
	}
	if e.OverageReason != "" {
		detail += ", overage unavailable: " + strings.ReplaceAll(e.OverageReason, "_", " ")
	}
	resets := "reset time unknown"
	payload := ""
	if !e.ResetAt.IsZero() {
		payload = e.ResetAt.UTC().Format(time.RFC3339)
		resets = fmt.Sprintf("resets %s (in %s)", e.ResetAt.UTC().Format("Mon Jan 2 15:04 UTC"), time.Until(e.ResetAt).Round(time.Minute))
	}
	msg := fmt.Sprintf("%s (%s) — %s. Switch to another provider, or send a message after it resets",
		chatmarkers.ProviderUsageLimitLead, detail, resets)
	out := chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, payload, msg)
	if e.Err != nil {
		out += ": " + e.Err.Error()
	}
	return out
}

func (e *UsageLimitError) Unwrap() error { return e.Err }

// ErrorClass: a usage window is the user's to wait out or switch providers on —
// a user error that logs at INFO and never reaches Sentry.
func (e *UsageLimitError) ErrorClass() svcerr.Class { return svcerr.ClassUser }
