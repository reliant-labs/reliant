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
	"github.com/reliant-labs/reliant/internal/runenv"
)

// NewReporter builds the process error reporter from its Sentry config. It
// centralizes the "prod yes, dev no" policy so every server entrypoint stays a
// single call.
//
// The environment tier is runenv's resolution — the same one the logger's
// DEBUG guard and auth use — so a pod with neither RELIANT_ENV nor NODE_ENV
// set, which is how prod runs, reports.
//
// A NoopReporter is returned (Sentry stays dark) when ANY of the following hold:
//   - the environment is dev or test
//   - cfg.Enabled is false (SENTRY_ENABLED=false)
//   - cfg.DSN is empty
//
// Otherwise a live SentryReporter is returned. This mirrors the gating the
// Electron main process (app.isPackaged) and web frontend (isDev) already use.
// An empty cfg.Environment lets clientOptions guess from the version string,
// which is right for a desktop build and only coincidentally right for a
// server: a prerelease tag deployed to prod would file prod's errors under
// "prerelease" — so a deploy states it.
func NewReporter(cfg SentryConfig) ErrorReporter {
	// Never report from dev or test runs.
	if !runenv.IsProd() {
		return NewNoopReporter()
	}
	if !cfg.Enabled || cfg.DSN == "" {
		return NewNoopReporter()
	}
	reporter, err := NewSentryReporter(cfg)
	if err != nil || reporter == nil {
		return NewNoopReporter()
	}
	return reporter
}
