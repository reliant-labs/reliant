package commands

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/builddefaults"
)

// A plain `go install github.com/reliant-labs/reliant/cmd/reliant@latest` build
// — no ldflags, no environment — MUST talk to production.
//
// The compiled defaults carry the hosted endpoints, so this holds as long as
// nothing in the resolution path prefers an empty or loopback value. The
// NeutralServerURL fallback (http://localhost:8080) is reachable only when the
// compiled default is blank, which a release build never leaves it.
func TestGoInstallBuildDefaultsToProduction(t *testing.T) {
	// Explicitly unset: t.Setenv restores the previous value at test end, so
	// this does not disturb a developer's shell or other tests.
	t.Setenv(envServerURL, "")
	t.Setenv(envGatewayURL, "")

	if got, want := defaultServerURL(), "https://api.reliantapi.com"; got != want {
		t.Errorf("defaultServerURL() = %q, want the production API %q", got, want)
	}
	if got, want := defaultGatewayURL(), "https://gateway.reliantapi.com"; got != want {
		t.Errorf("defaultGatewayURL() = %q, want the production gateway %q", got, want)
	}
}

// The neutral loopback address must never be what a shipped binary resolves
// to. It exists for callers that deliberately point at their own stack; if it
// ever became the default, every `go install` user would silently target a
// server on their own machine that is not running.
func TestNeutralURLIsNotTheShippedDefault(t *testing.T) {
	t.Setenv(envServerURL, "")

	if defaultServerURL() == builddefaults.NeutralServerURL {
		t.Fatalf("shipped default resolved to the neutral loopback %q — "+
			"builddefaults.ServerURL is empty in this build",
			builddefaults.NeutralServerURL)
	}
}

// Dev overrides still work, and they are the ONLY way to leave production.
// This is the property that makes "default to prod" safe to rely on: pointing
// at a dev stack is an explicit, visible act on the command line.
func TestExplicitEnvOverridesProduction(t *testing.T) {
	t.Setenv(envServerURL, "http://localhost:8151")
	t.Setenv(envGatewayURL, "http://localhost:29190")

	if got, want := defaultServerURL(), "http://localhost:8151"; got != want {
		t.Errorf("defaultServerURL() = %q, want the explicit override %q", got, want)
	}
	if got, want := defaultGatewayURL(), "http://localhost:29190"; got != want {
		t.Errorf("defaultGatewayURL() = %q, want the explicit override %q", got, want)
	}
}
