// Copyright (c) 2025 Reliant Labs
//
// Process-global instrumentation. The exported vars are the collectors and
// registry the process registers once at init and updates from everywhere; a
// getter returns the same pointer and hides nothing. Behind an interface these
// would still be the single global sink they are today.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: ErrorReporter/ContextualErrorReporter (Sentry vs no-op) is already the seam; moving it to contract.go is deferred; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: error-reporting setup (Sentry vs no-op) selected once at startup
package telemetry

import (
	"os"
	"strconv"

	"github.com/reliant-labs/reliant/internal/runenv"
)

// NewReporterFromEnv builds the process error reporter based on the runtime
// environment and Sentry env vars. It centralizes the "prod yes, dev no" policy
// so every server entrypoint stays a single call.
//
// The environment is runenv's resolution — the same one the logger's DEBUG
// guard and auth use — so a pod with neither RELIANT_ENV nor NODE_ENV set,
// which is how prod runs, reports.
//
// A NoopReporter is returned (Sentry stays dark) when ANY of the following hold:
//   - the environment is dev or test
//   - SENTRY_ENABLED is explicitly "false"
//   - SENTRY_DSN is empty
//
// Otherwise a live SentryReporter is returned. This mirrors the gating the
// Electron main process (app.isPackaged) and web frontend (isDev) already use.
func NewReporterFromEnv() ErrorReporter {
	// Never report from dev or test runs.
	if !runenv.IsProd() {
		return NewNoopReporter()
	}

	// Explicit kill switch, independent of environment.
	if os.Getenv("SENTRY_ENABLED") == "false" {
		return NewNoopReporter()
	}

	// No DSN configured → nothing to send to.
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return NewNoopReporter()
	}

	var tracesSampleRate float64
	if raw := os.Getenv("SENTRY_TRACES_SAMPLE_RATE"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			tracesSampleRate = v
		}
	}

	reporter, err := NewSentryReporter(SentryConfig{
		Enabled:          true,
		DSN:              dsn,
		TracesSampleRate: tracesSampleRate,
	})
	if err != nil || reporter == nil {
		return NewNoopReporter()
	}
	return reporter
}
