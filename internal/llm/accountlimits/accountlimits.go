// Copyright (c) 2025 Reliant Labs

// Package accountlimits keeps what is known about each provider account's rate
// limits: it persists the snapshots LLM responses carry (Recorder), reads the
// providers' usage endpoints for the accounts responses do not cover (Usage,
// Poller), and turns the stored state into what the UI shows.
//
// Three writers feed one row per account in provider_account_limits:
//
//   - the Recorder, from the rate-limit headers of every response the account
//     served (an ratelimit.Observer; it never does I/O on the request path);
//   - a bench, when a request is refused with a usage limit (Recorder.Bench), or
//     a snapshot / usage reading shows a window exhausted;
//   - the Poller, which asks the provider's usage API about accounts nothing has
//     used recently, and the on-demand Refresher behind Settings → Providers.
//
// Everything is lazy: an account is limited iff limited_until is in the future,
// and a window whose reset has passed is a new window (Effective). Nothing
// sweeps, so no process needs a clock-race job.
//
// A usage endpoint that refuses us (401/403/404, or a body we cannot read) only
// ever costs that account its polled numbers: it keeps running on response
// headers, the failure is recorded on the row, and the poll backs off. It never
// fails an LLM call and never benches an account.
//
//forge:exclude-contract: the Recorder is a ratelimit.Observer factory and the Poller a background loop; consumers (call_llm, the provider_account handler) declare their own narrow interfaces
package accountlimits

import (
	"strings"
	"time"
)

// Where a limit state came from (provider_account_limits.source).
const (
	SourceHeaders    = "headers"
	SourceUsageAPI   = "usage_api"
	SourceLimitError = "usage_limit_error"
)

const (
	// headerFreshFor is how long response headers count as fresher than a
	// usage-API reading: an account served this recently is not polled.
	headerFreshFor = 10 * time.Minute

	// idleAfter is how long an account can go without traffic before it is no
	// longer polled in the background.
	idleAfter = 24 * time.Hour

	// confirmAfterReset is how long after a bench ends the poller waits before
	// confirming the reset and refreshing the bars.
	confirmAfterReset = 30 * time.Second

	// UsageFloor is the minimum gap between two calls to one account's usage
	// API, whoever makes them.
	UsageFloor = 60 * time.Second

	// heartbeat bounds how long an observed-but-unchanged snapshot waits before
	// it is written anyway, so observed_at and last_traffic_at keep saying the
	// account is in use.
	heartbeat = 60 * time.Second

	// usedMovedBy is how far a window's used fraction moves before the change
	// is written without waiting for the heartbeat.
	usedMovedBy = 0.02
)

// probeLadder is how long an account is benched when the provider refused it
// without saying when the limit lifts. The last rung repeats. It matches the
// workflow runtime's wait ladder, so a thread and the account it waits on agree.
var probeLadder = []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

// ProbeDelay is the bench length for the probes-th consecutive refusal with an
// unknown reset (0 is the first).
func ProbeDelay(probes int) time.Duration {
	if probes < 0 {
		probes = 0
	}
	if probes >= len(probeLadder) {
		probes = len(probeLadder) - 1
	}
	return probeLadder[probes]
}

// CanonicalWindow maps a window name to the vocabulary a bench is stored in
// (the one ratelimit.UsageLimitError uses): "5h" → "five_hour", "7d" →
// "seven_day". Other names pass through.
func CanonicalWindow(name string) string {
	switch name {
	case "5h":
		return "five_hour"
	case "7d":
		return "seven_day"
	}
	return name
}

// DisplayWindow is the inverse of CanonicalWindow, the form the UI compares
// against a usage window's name.
func DisplayWindow(name string) string {
	switch name {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	}
	return name
}

// isScoped reports whether a window name is scoped to one model or one limit
// family ("7d.opus", "codex_other.primary"), as opposed to binding the whole
// account ("5h", "7d", "primary", "secondary"). Only an unscoped window being
// exhausted benches the account.
func isScoped(name string) bool { return strings.Contains(name, ".") }
