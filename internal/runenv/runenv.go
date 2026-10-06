// Copyright (c) 2025 Reliant Labs

// Package runenv answers one question: which environment tier is this process
// running in — dev, test or prod.
//
// It is the ONLY place that reads RELIANT_ENV / NODE_ENV. Auth bypass, feature
// flags, analytics, the Sentry reporter and the log level all ask it, so they
// cannot disagree about where they are running. That is not hypothetical: the
// logger once kept its own copy that tested RELIANT_ENV for "prod", which prod
// never sets, so its "never DEBUG in prod" guard did not fire in prod while
// everything else correctly believed it was prod.
//
// It imports nothing from this module on purpose. config imports logging and
// logging imports telemetry, so the resolution can only be shared by all three
// from a package below them.
package runenv

import (
	"os"
	"strings"
)

// Environment is a runtime environment tier.
type Environment string

const (
	Dev  Environment = "dev"
	Test Environment = "test"
	Prod Environment = "prod"
)

// Get returns the current environment from environment variables.
//
// This resolution FAILS CLOSED: dev is reachable only by naming it, and every
// value that is not recognised — an unset variable, a typo, or the name of an
// environment that no longer exists — resolves to prod. Dev is the permissive
// tier (it drives auth bypass via IsDev), so an unrecognised value must never
// land there: that would let a stale deployment config silently disable
// authentication with no enum left to explain why.
//
// Prod pods set neither variable, so they resolve to prod by this rule.
func Get() Environment {
	env := strings.ToLower(os.Getenv("RELIANT_ENV"))
	if env == "" {
		env = strings.ToLower(os.Getenv("NODE_ENV"))
	}

	switch env {
	case "test", "testing":
		return Test
	// "e2e" is the in-cluster end-to-end harness (control-plane's
	// deploy/kcl/e2e/main.k sets RELIANT_ENV=e2e on the reliant pods). It runs
	// against fake secrets and needs the dev-tier behaviours, so it is named
	// here explicitly rather than relying on a permissive fallback.
	case "dev", "development", "local", "e2e":
		return Dev
	default:
		return Prod
	}
}

// IsTest reports whether the process runs in the test environment.
func IsTest() bool {
	return Get() == Test
}

// IsDev reports whether the process runs in the development environment.
// This is the auth-bypass tier: it must be true only when dev was asked for by
// name. See Get for why the resolution fails closed.
func IsDev() bool {
	return Get() == Dev
}

// IsProd reports whether the process runs in production.
func IsProd() bool {
	return Get() == Prod
}
