// Copyright (c) 2025 Reliant Labs

package daemonquery

import (
	"encoding/json"
	"fmt"
)

// This file is the shared wire contract for the pull-RPC. Both the gateway
// (responder.go) and any client (client.go, or the control-plane mirror of
// this package) import these constants and types — so the subject string and
// JSON shape can only drift at the type level, which the compiler will catch.

const (
	// SubjectQueryPrefix is the common prefix for all pull-RPC subjects on a
	// per-daemon basis. The status subject is `<prefix><daemonID>.status`.
	SubjectQueryPrefix = "daemon.v1.query."
)

// SubjectStatus returns the NATS subject for the status query of a daemon.
// The control-plane uses the SAME helper to publish requests, so any drift
// is caught at the type level.
func SubjectStatus(daemonID string) string {
	return SubjectQueryPrefix + daemonID + ".status"
}

// Status is the wire payload returned to a status query. Intentionally
// minimal — anything richer should live in a separate query subject to keep
// the hot path small.
//
// The `LastActiveMs` field is unix milliseconds (not a Go time) so the JSON
// representation is stable across timezones and language boundaries.
type Status struct {
	Connected    bool   `json:"connected"`
	LastActiveMs int64  `json:"last_active_ms"`
	DaemonType   string `json:"daemon_type,omitempty"`
}

// ParseStatus decodes a Status from JSON. Exposed so callers (the
// control-plane query client) use the same parser as test fixtures.
func ParseStatus(data []byte) (Status, error) {
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("decoding daemon status: %w", err)
	}
	return s, nil
}
