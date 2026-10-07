// Copyright (c) 2025 Reliant Labs
package db

import "testing"

// -short is the hermetic fast tier, so DB-backed tests stay out of it unless
// REQUIRE_TEST_DB=1 asks for them. The full lane is unaffected.
func TestShortModeDBSkipReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		short    bool
		required bool
		wantSkip bool
	}{
		{"short skips", true, false, true},
		{"short with REQUIRE_TEST_DB runs", true, true, false},
		{"full lane runs", false, false, false},
		{"full lane with REQUIRE_TEST_DB runs", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reason := shortModeDBSkipReason(tc.short, tc.required)
			if gotSkip := reason != ""; gotSkip != tc.wantSkip {
				t.Fatalf("shortModeDBSkipReason(short=%v, required=%v) = %q, want skip=%v",
					tc.short, tc.required, reason, tc.wantSkip)
			}
		})
	}
}

// End to end through the harness: under -short a DB-backed test must skip
// before it reaches Postgres, even on a box where one is reachable.
func TestNewTestRepoSkipsUnderShort(t *testing.T) {
	if !testing.Short() {
		t.Skip("pins the -short behaviour; there is nothing to assert without -short")
	}
	t.Setenv("REQUIRE_TEST_DB", "")

	reachedBody := false
	t.Run("db-backed", func(t *testing.T) {
		NewTestRepo(t)
		reachedBody = true
	})
	if reachedBody {
		t.Fatal("NewTestRepo returned a repo under -short; DB-backed tests must skip in the fast tier")
	}
}
