// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"strings"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
)

// copilotDeviceNamespace is a fixed, package-private namespace UUID used to
// derive stable per-user device identifiers deterministically (RFC 4122 v5).
// It is an arbitrary constant — it only needs to be stable so the same user
// always maps to the same device id across process restarts.
var copilotDeviceNamespace = uuid.MustParse("b3d2f8a1-6c74-4e9b-9a1d-2f7c0e5a83b4")

// deviceID derives the per-install device UUID sent as `x-client-machine-id`.
// It is STABLE per user (derived deterministically from the Reliant user id, no
// storage, no restart drift) and falls back to a random UUID when no user id is
// available.
func deviceID(opts llm.DriverOptions) string {
	userID := strings.TrimSpace(opts.UserID)
	if userID == "" {
		return uuid.New().String()
	}
	// keyID (the upstream account / credential id, when available) ensures two
	// upstream accounts for the same Reliant user derive distinct device ids.
	keyID := strings.TrimSpace(opts.AccountUUID)
	return uuid.NewSHA1(copilotDeviceNamespace, []byte(userID+":"+keyID+":copilot-device")).String()
}

// sessionID returns the per-conversation session UUID (opts.SessionID), sent as
// `x-client-session-id`. Falls back to a random UUID when unset.
func sessionID(opts llm.DriverOptions) string {
	if opts.SessionID != nil {
		if s := strings.TrimSpace(*opts.SessionID); s != "" {
			return s
		}
	}
	return uuid.New().String()
}
