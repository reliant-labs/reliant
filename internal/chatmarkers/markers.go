// Copyright (c) 2025 Reliant Labs

// Package chatmarkers centralizes the cross-process "chat-error marker"
// contract shared between the Go side (Temporal workflow activities, LLM
// driver error wrappers) and the TypeScript side (reliant/web's chat-error
// UI).
//
// A "chat marker" is a stable bracketed tail planted into an error message
// before the error crosses a serialization boundary that drops typed-error
// shape — most notably Temporal's JSON stringification of activity / workflow
// errors. The frontend recovers the structured signal by scanning for the
// marker substring in ErrorUpdate.error_message.
//
// Wire format:
//
//		<human-readable message> [<KIND>:<payload>]
//
//	  - <KIND>     is one of the exported Kind* constants below.
//	  - <payload>  is opaque to this package (URL, integer count, …).
//	  - The bracket pair, colon separator, and KIND identifiers are the wire
//	    contract — DO NOT mutate them. The TS mirror in
//	    reliant/web/src/lib/chatMarkers.ts holds the matching constants and
//	    parsing logic.
//
// Why a substring marker (and not a typed error / proto code):
//
//   - Temporal activity & workflow errors are surfaced to the frontend as
//     plain strings in ErrorUpdate.error_message after Temporal stringifies
//     the wrapped Go error. Typed errors, error codes, and structured details
//     are all lost at that boundary.
//
//   - The chat error stream is the only artifact the user sees for these
//     workflow-internal failure classes. A substring marker is the cheapest
//     thing that survives every wrapping path (fmt.Errorf, connect.Wrap,
//     Temporal serialization).
//
// Adding a new marker:
//
//  1. Append a new exported Kind* constant below.
//  2. Mirror it in reliant/web/src/lib/chatMarkers.ts.
//  3. Add an entry to the cross-process drift-guard tests on both sides
//     (markers_test.go here, chatMarkers.test.ts in web/).
//  4. Producer call sites use chatmarkers.Wrap(kind, payload); consumer
//     call sites use Extract / Strip.
package chatmarkers

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind identifies a marker variant on the wire. It is a typed alias over
// string so call sites read clearly without losing the ability to use these
// values directly in fmt.Sprintf / strings.Contains.
type Kind string

// Exported marker kinds. These string literals are the cross-process wire
// contract — DO NOT rename without coordinated changes in
// reliant/web/src/lib/chatMarkers.ts and the drift-guard tests.
const (
	// KindReliantManagedQuotaExhausted signals the reliant-managed
	// (LiteLLM virtual key) free-tier global budget has been exhausted by
	// the user. Payload is the upgrade URL the frontend should route to.
	// Producer: internal/llm/drivers/reliant/driver.go.
	KindReliantManagedQuotaExhausted Kind = "RELIANT_MANAGED_QUOTA_EXHAUSTED"

	// KindDaemonOfflineHalt signals DynamicWorkflow self-terminated because
	// the user's workspace daemon stayed offline for N consecutive turns.
	// Payload is the integer turn count (decimal-encoded).
	// Producer: internal/workflow/runtime/daemon_offline_tracker.go.
	KindDaemonOfflineHalt Kind = "RELIANT_DAEMON_OFFLINE_HALT"
)

// markerRegex matches any `[<KIND>:<payload>]` tail, allowing an optional
// run of leading whitespace so Strip removes the separator between the
// human-readable prefix and the bracketed tail cleanly. Payload may be
// empty; payload cannot contain a literal `]`. KIND is restricted to
// upper-case ASCII + digits + underscore — broad enough for any future
// SCREAMING_SNAKE_CASE name we'd reasonably add, narrow enough to avoid
// matching unrelated bracketed text users might paste into prompts.
var markerRegex = regexp.MustCompile(`\s*\[([A-Z0-9_]+):([^\]]*)\]`)

// Wrap formats a marker tail and appends it to the given message. The
// returned string has the shape `<message> [<KIND>:<payload>]`. If message
// is empty the leading space is omitted: `[<KIND>:<payload>]`.
//
// Producer call sites should ALWAYS use Wrap rather than hand-formatting the
// brackets — that keeps the wire format in exactly one place.
func Wrap(kind Kind, payload string, message string) string {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return fmt.Sprintf("[%s:%s]", string(kind), payload)
	}
	return fmt.Sprintf("%s [%s:%s]", msg, string(kind), payload)
}

// Extract scans msg for the FIRST `[<KIND>:<payload>]` marker tail and
// returns the kind, payload, and a found flag. Multi-marker support is
// intentionally out of scope — every producer today plants exactly one
// marker per error. If a future producer needs to compose multiple markers,
// extend this into ExtractAll without breaking the single-marker contract.
//
// Returns ("", "", false) when no marker is present.
func Extract(msg string) (kind Kind, payload string, found bool) {
	if msg == "" {
		return "", "", false
	}
	m := markerRegex.FindStringSubmatch(msg)
	if m == nil {
		return "", "", false
	}
	return Kind(m[1]), m[2], true
}

// Strip removes the marker tail (and any whitespace immediately preceding
// it) from msg, then trims the result. The returned string is what the
// human-facing chat UI should display when the consumer has already routed
// on the marker out-of-band.
//
// Strip is a no-op on messages with no marker. It only strips the first
// marker — mirroring Extract's single-marker contract.
func Strip(msg string) string {
	if msg == "" {
		return ""
	}
	return strings.TrimSpace(markerRegex.ReplaceAllString(msg, ""))
}
