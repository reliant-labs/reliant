// Copyright (c) 2025 Reliant Labs

// Package runenv answers one question: which environment tier is this process
// running in — dev, test or prod.
//
// It is the ONLY place that decides the tier. Auth bypass, feature flags,
// analytics and the log level all ask it, so they cannot
// disagree about where they are running. That is not hypothetical: the logger
// once kept its own copy that tested RELIANT_ENV for "prod", which prod never
// sets, so its "never DEBUG in prod" guard did not fire in prod while
// everything else correctly believed it was prod.
//
// The tier is CONFIGURED, not read from the environment here: the root config
// (proto/reliant/config/v1 RuntimeConfig.environment — RELIANT_ENV, else NODE_ENV) is
// loaded by each process's entrypoint, which hands the value to Configure
// before anything asks. Held here, set once at boot, rather than threaded to
// every caller, because the callers sit below the entrypoints in unrelated
// packages and all must agree.
//
// It imports nothing from this module on purpose. config imports logging and
// logging imports telemetry, so the resolution can only be shared by all three
// from a package below them.
//
//forge:exclude-contract: holds the dev/test/prod tier the entrypoint configured from the root config; no I/O
package runenv

import (
	"strings"
	"sync/atomic"
)

// Environment is a runtime environment tier.
type Environment string

const (
	Dev  Environment = "dev"
	Test Environment = "test"
	Prod Environment = "prod"
)

// configured is the tier Configure recorded; nil until it is called.
var configured atomic.Pointer[Environment]

// Configure records the tier this process runs in, from the root config's
// runtime environment exactly as the operator spelled it. An entrypoint calls
// it once, before anything asks Get.
func Configure(name string) {
	env := Parse(name)
	configured.Store(&env)
}

// Parse resolves an environment name to its tier, case-insensitively.
//
// This resolution FAILS CLOSED: dev is reachable only by naming it, and every
// value that is not recognised — an empty one, a typo, or the name of an
// environment that no longer exists — resolves to prod. Dev is the permissive
// tier (it drives auth bypass via IsDev), so an unrecognised value must never
// land there: that would let a stale deployment config silently disable
// authentication with no enum left to explain why.
func Parse(name string) Environment {
	switch strings.ToLower(name) {
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

// Get returns the tier Configure recorded. A process that configured none —
// it never loaded the root config — is prod, by the same fail-closed rule as
// Parse. Prod pods set neither variable, so they resolve to prod either way.
func Get() Environment {
	if env := configured.Load(); env != nil {
		return *env
	}
	return Prod
}

// IsTest reports whether the process runs in the test environment.
func IsTest() bool {
	return Get() == Test
}

// IsDev reports whether the process runs in the development environment.
// This is the auth-bypass tier: it must be true only when dev was asked for by
// name. See Parse for why the resolution fails closed.
func IsDev() bool {
	return Get() == Dev
}

// IsProd reports whether the process runs in production.
func IsProd() bool {
	return Get() == Prod
}

// cleanupRegistrar is the part of testing.TB ConfigureForTest needs, so this
// package does not import testing.
type cleanupRegistrar interface{ Cleanup(func()) }

// ConfigureForTest is Configure for the duration of one test: when t ends the
// process goes back to whatever it had before, configured or not. The tier is
// process-wide, so a test that sets it must not run in parallel with one that
// reads it.
func ConfigureForTest(t cleanupRegistrar, name string) {
	prev := configured.Load()
	t.Cleanup(func() { configured.Store(prev) })
	Configure(name)
}
