// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"os"
	"strconv"
)

// NewReporterFromEnv builds the process error reporter based on the runtime
// environment and Sentry env vars. It centralizes the "prod yes, dev no" policy
// so every server entrypoint stays a single call.
//
// devOrTest is supplied by the caller (via config.IsDevelopmentEnvironment /
// config.IsTestEnvironment) rather than read here — telemetry cannot import
// config without an import cycle (config -> logging -> telemetry).
//
// A NoopReporter is returned (Sentry stays dark) when ANY of the following hold:
//   - devOrTest is true (dev/staging/preprod/test)
//   - SENTRY_ENABLED is explicitly "false"
//   - SENTRY_DSN is empty
//
// Otherwise a live SentryReporter is returned. This mirrors the gating the
// Electron main process (app.isPackaged) and web frontend (isDev) already use.
func NewReporterFromEnv(devOrTest bool) ErrorReporter {
	// Never report from dev/staging/preprod or test runs.
	if devOrTest {
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