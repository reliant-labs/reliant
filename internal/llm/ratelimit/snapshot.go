// Copyright (c) 2025 Reliant Labs

// Package ratelimit turns the rate-limit / usage-window headers an LLM provider
// sends on EVERY response into a provider-neutral Snapshot, and hands each one
// to an Observer.
//
// A Snapshot says how much of each limit window is used and when it resets. It
// deliberately carries no credential or account identity: the caller that
// installs the Observer (WithObserver) knows which credential the request used
// and attaches that itself, so a later phase can persist a snapshot per
// credential without this package learning what a credential is.
//
// Every parser here is pure (headers + a clock in, Snapshot out) and defensive:
// header names the providers do not document are parsed best-effort and a
// malformed value drops that one field, never the snapshot.
//
//forge:exclude-contract: pure header parsers plus a one-method consumer-declared Observer; no outbound I/O
package ratelimit

import (
	"time"
)

// Status is the provider's verdict on the credential at the time of a response.
type Status string

const (
	// StatusUnknown means the response carried no verdict and none could be derived.
	StatusUnknown Status = "unknown"
	// StatusAllowed means requests are being served.
	StatusAllowed Status = "allowed"
	// StatusWarning means requests are served but a window is close to its limit.
	StatusWarning Status = "warning"
	// StatusRejected means a limit is exhausted and requests are refused until it resets.
	StatusRejected Status = "rejected"
)

// Window is one limit window as of a response.
type Window struct {
	// Name identifies the window within its provider: "5h", "7d", "primary",
	// "secondary", "requests", "tokens", "input-tokens", "output-tokens". A
	// non-default Codex limit family is prefixed ("codex_other.primary").
	Name string
	// UsedFraction is the share of the window consumed, 0..1, when the provider
	// reports utilization directly. Nil when unknown; see Used.
	UsedFraction *float64
	// Limit and Remaining are the absolute counts for count-based windows
	// (requests / tokens). Nil when the provider does not send them.
	Limit     *int64
	Remaining *int64
	// ResetsAt is when the window resets; zero when unknown.
	ResetsAt time.Time
	// Length is the window's duration when the provider states it or the name
	// encodes it ("5h", "7d"); zero when unknown.
	Length time.Duration
}

// Used returns the consumed fraction, derived from Limit/Remaining when the
// provider sent no utilization. ok is false when neither is available.
func (w Window) Used() (fraction float64, ok bool) {
	if w.UsedFraction != nil {
		return *w.UsedFraction, true
	}
	if w.Limit != nil && w.Remaining != nil && *w.Limit > 0 {
		return 1 - float64(*w.Remaining)/float64(*w.Limit), true
	}
	return 0, false
}

// Snapshot is the rate-limit state a single response reported.
type Snapshot struct {
	// Provider is the driver the response came from ("anthropic", "claude-code",
	// "codex", "openai"). Parsers leave it empty; Observe stamps it.
	Provider string
	// Status is the provider's verdict (or one derived from the windows).
	Status Status
	// ObservedAt is when the response was parsed.
	ObservedAt time.Time
	// Windows lists every window the response described.
	Windows []Window
	// RepresentativeWindow names the window the provider says binds the
	// request ("5h", "7d"); empty when not stated.
	RepresentativeWindow string
	// LimitID is the metered limit family (Codex "codex", "codex_other"); empty
	// when the provider has no such notion.
	LimitID string
	// PlanType is the subscription plan when the response states it (Codex).
	PlanType string
	// OverageDisabledReason is why paid overage could not absorb a refused
	// request (Anthropic "out_of_credits"); empty otherwise.
	OverageDisabledReason string
}

// Window returns the named window, or false.
func (s Snapshot) Window(name string) (Window, bool) {
	for _, w := range s.Windows {
		if w.Name == name {
			return w, true
		}
	}
	return Window{}, false
}
