// Copyright (c) 2025 Reliant Labs
package daemonruntime

import "sync/atomic"

// daemonIdentity holds this daemon's control-plane-assigned ID for handlers
// that run without access to the daemonClient (e.g. the registered command
// handlers, which have a static (ctx, payload) signature). It is seeded from
// persisted credentials at startup and updated when the gateway assigns/asserts
// identity in the RegistrationAck.
//
// It exists so exec.bg_list can stamp ProcessInfo.DaemonID, letting the
// orchestrator build the env-aware proxied preview URL for a detected dev
// server. Empty until registration completes (or in fully-local mode), in
// which case callers fall back to the daemon's loopback URL.
var daemonIdentity atomic.Value // string

// SetDaemonIdentity records the daemon's control-plane ID. Safe for concurrent
// use; a no-op for the empty string so a not-yet-assigned identity never
// clobbers a real one.
func SetDaemonIdentity(id string) {
	if id == "" {
		return
	}
	daemonIdentity.Store(id)
}

// DaemonIdentity returns the daemon's control-plane ID, or "" if unknown.
func DaemonIdentity() string {
	if v, ok := daemonIdentity.Load().(string); ok {
		return v
	}
	return ""
}
