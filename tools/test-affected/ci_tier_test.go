package main

import (
	"os"
	"testing"
)

// TestCITestTierReachedGoTest proves that the Go test tier CI says it runs is
// the one `go test` actually received.
//
// PR CI runs Go tests in two tiers (.github/workflows/pr-ci.yml, the backend
// job): `short` on a pull request (`go test -short`) and `full` on main and
// nightly (no `-short`, so every testing.Short()-gated test runs). The job
// exports its tier as RELIANT_TEST_TIER and derives the flag from it. This test
// is the check that the two never drift apart: an edit that hardcodes `-short`
// into the full tier, or drops it from the short one, fails here in the very
// run it lands in instead of silently merging the tiers. The full tier is the
// only place the short-gated tests run, so losing it would be invisible
// otherwise.
//
// It deliberately has no testing.Short() gate of its own: it must run in both
// tiers. Outside CI (RELIANT_TEST_TIER unset) there is no declared tier to
// check, so it skips.
func TestCITestTierReachedGoTest(t *testing.T) {
	tier := os.Getenv("RELIANT_TEST_TIER")
	switch tier {
	case "":
		t.Skip("RELIANT_TEST_TIER is unset: not a CI tiered run")
	case "short":
		if !testing.Short() {
			t.Fatal("RELIANT_TEST_TIER=short but `go test` ran without -short: the PR tier is running the full suite")
		}
	case "full":
		if testing.Short() {
			t.Fatal("RELIANT_TEST_TIER=full but `go test` ran with -short: the full tier is skipping every short-gated test, and nothing else runs them")
		}
	default:
		t.Fatalf("RELIANT_TEST_TIER=%q: want short or full", tier)
	}
}
