// Copyright (c) 2025 Reliant Labs
package db

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// DB-backed tests take their server from DATABASE_URL and nowhere else. With it
// unset there is no fallback address: they skip — or fail, under
// REQUIRE_TEST_DB=1 — without opening a connection. The harness used to fall
// back to localhost:5433, so a bare `go test` created and dropped scratch
// databases on a shared server that nobody had chosen.

func TestTestDBTarget(t *testing.T) {
	t.Parallel()

	const explicit = "postgres://alice:s3cret@db.example:6543/reliant?sslmode=disable"

	cases := []struct {
		name        string
		databaseURL string
		probeErr    error
		wantDSN     string
		wantProbed  []string
		wantReason  []string // substrings of the "unavailable" explanation; empty means none
		notInReason []string
	}{
		{
			name:        "unset dials nothing",
			databaseURL: "",
			wantReason:  []string{"DATABASE_URL is not set", "make postgres-up", "make test"},
		},
		{
			name:        "whitespace-only counts as unset",
			databaseURL: " \t\n",
			wantReason:  []string{"DATABASE_URL is not set"},
		},
		{
			name:        "explicit DSN is probed once and used",
			databaseURL: "  " + explicit + "\n",
			wantDSN:     explicit,
			wantProbed:  []string{explicit},
		},
		{
			name:        "unreachable explicit DSN is named without its password",
			databaseURL: explicit,
			probeErr:    errors.New("connection refused"),
			wantProbed:  []string{explicit},
			wantReason:  []string{"no test database reachable", "db.example:6543", "connection refused", "make postgres-up"},
			notInReason: []string{"s3cret"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var probed []string
			probe := func(dsn string) error {
				probed = append(probed, dsn)
				return tc.probeErr
			}

			dsn, reason := testDBTarget(tc.databaseURL, probe)

			if !slices.Equal(probed, tc.wantProbed) {
				t.Errorf("probed %q, want %q — a probe is a connection, and only an explicitly set DATABASE_URL may be dialed",
					probed, tc.wantProbed)
			}
			if dsn != tc.wantDSN {
				t.Errorf("dsn = %q, want %q", dsn, tc.wantDSN)
			}
			if gotReason, wantReason := reason != "", len(tc.wantReason) > 0; gotReason != wantReason {
				t.Fatalf("unavailable = %q, want a reason: %v", reason, wantReason)
			}
			for _, want := range tc.wantReason {
				if !strings.Contains(reason, want) {
					t.Errorf("reason does not mention %q:\n%s", want, reason)
				}
			}
			for _, leak := range tc.notInReason {
				if strings.Contains(reason, leak) {
					t.Errorf("reason leaks %q:\n%s", leak, reason)
				}
			}
		})
	}
}

// The two tests below drive SetupTestDB for real, in a re-executed copy of this
// test binary. A REQUIRE_TEST_DB failure cannot be observed from inside the
// test that fails, and the child gives both cases the same environment — no
// DATABASE_URL — whatever the parent was run with.
const (
	noDSNChildEnv = "RELIANT_TESTDB_NO_DSN_CHILD"
	dialMarker    = "RELIANT-TESTDB-DIAL-ATTEMPTED"
	setupReturned = "RELIANT-TESTDB-SETUP-RETURNED"
)

func TestSetupTestDBSkipsWithoutDatabaseURL(t *testing.T) {
	if os.Getenv(noDSNChildEnv) != "" {
		runSetupTestDBChild(t)
		return
	}
	t.Parallel()

	out, err := runNoDSNChild(t, false)
	if err != nil {
		t.Fatalf("child exited with %v; with DATABASE_URL unset SetupTestDB must skip, not fail:\n%s", err, out)
	}
	assertNoDial(t, out)
	for _, want := range []string{"--- SKIP", "SKIPPING DB-BACKED TEST", "DATABASE_URL is not set"} {
		if !strings.Contains(out, want) {
			t.Errorf("child output does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, setupReturned) {
		t.Errorf("SetupTestDB returned a database although DATABASE_URL was unset:\n%s", out)
	}
}

func TestSetupTestDBFailsWithoutDatabaseURLWhenRequired(t *testing.T) {
	if os.Getenv(noDSNChildEnv) != "" {
		runSetupTestDBChild(t)
		return
	}
	t.Parallel()

	out, err := runNoDSNChild(t, true)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child exited with %v; REQUIRE_TEST_DB=1 with DATABASE_URL unset must fail:\n%s", err, out)
	}
	assertNoDial(t, out)
	for _, want := range []string{"--- FAIL", "REQUIRE_TEST_DB=1 but DATABASE_URL is not set"} {
		if !strings.Contains(out, want) {
			t.Errorf("child output does not contain %q:\n%s", want, out)
		}
	}
}

// runSetupTestDBChild is the child half: call SetupTestDB exactly as a
// DB-backed test does, with the reachability probe swapped for one that
// reports instead of connecting. Nothing here can reach a server, whatever is
// listening on this machine.
func runSetupTestDBChild(t *testing.T) {
	probeTestDB = func(dsn string) error {
		fmt.Printf("%s %s\n", dialMarker, redactDSN(dsn))
		return errors.New("dial blocked by test")
	}
	_, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	fmt.Println(setupReturned)
}

// runNoDSNChild re-executes the calling test in a child process with
// DATABASE_URL removed, and REQUIRE_TEST_DB set only when requireDB is true.
func runNoDSNChild(t *testing.T, requireDB bool) (string, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0],
		"-test.run=^"+t.Name()+"$",
		"-test.v",
		"-test.count=1",
		"-test.timeout=60s",
		// -short skips DB-backed tests before DATABASE_URL is consulted, for a
		// different reason. Off, so it cannot mask the behaviour under test.
		"-test.short=false",
	)
	cmd.Env = append(envWithout(os.Environ(), "DATABASE_URL", "REQUIRE_TEST_DB", noDSNChildEnv), noDSNChildEnv+"=1")
	if requireDB {
		cmd.Env = append(cmd.Env, "REQUIRE_TEST_DB=1")
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func envWithout(env []string, names ...string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, name) {
			kept = append(kept, kv)
		}
	}
	return kept
}

func assertNoDial(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, dialMarker) {
		t.Errorf("SetupTestDB tried to reach a database server although DATABASE_URL was unset; "+
			"it must never pick one on its own:\n%s", out)
	}
}
