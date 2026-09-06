// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"
	"os"
	"testing"
)

// ownerPIDFromDBName decides which databases reapStaleTestDBs is allowed to
// DROP, so it is the entire safety boundary of the reaper. These cases pin two
// separate properties: that every name shape this harness has ever produced
// yields its owning pid (otherwise those databases become unreachable garbage),
// and that a name from outside the harness is never parsed (otherwise the
// reaper could target something real).
func TestOwnerPIDFromDBName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		dbName  string
		wantPID int
		wantOK  bool
	}{
		{"per-test copy", testDBPrefix + "t12345_7", 12345, true},
		{"per-test copy, high sequence", testDBPrefix + "t99_100000", 99, true},
		{"template", testDBPrefix + "tpl726a9861e6_47789", 47789, true},
		{"schema-drift scratch", testDBPrefix + "schemasql_26360", 26360, true},
		{"legacy per-package", testDBPrefix + "144f361236_29615", 29615, true},

		// Anything the harness did not name must be left alone. A real
		// database cannot carry the prefix, but the parser must still refuse
		// rather than guess when the suffix is not a pid.
		{"no prefix", "reliant", 0, false},
		{"prefix but no pid", testDBPrefix + "nopid", 0, false},
		{"non-numeric pid", testDBPrefix + "t_abc_1", 0, false},
		{"zero pid", testDBPrefix + "schemasql_0", 0, false},
		{"negative pid", testDBPrefix + "schemasql_-4", 0, false},
		{"control-plane database", "control-plane", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pid, ok := ownerPIDFromDBName(tc.dbName)
			if ok != tc.wantOK {
				t.Fatalf("ownerPIDFromDBName(%q) ok = %v, want %v", tc.dbName, ok, tc.wantOK)
			}
			if ok && pid != tc.wantPID {
				t.Fatalf("ownerPIDFromDBName(%q) pid = %d, want %d", tc.dbName, pid, tc.wantPID)
			}
		})
	}
}

// The reaper drops a database only when its creating process is gone, so a
// live pid must never be reported dead — that is the check standing between a
// concurrent test run and having its database dropped mid-test.
func TestProcessAlive(t *testing.T) {
	t.Parallel()

	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive reported this very process as dead")
	}

	// PID 1 exists on every Unix and is owned by root, so for a non-root test
	// run it exercises the EPERM path: signalling fails, yet the process
	// plainly exists and its database must not be reaped.
	if !processAlive(1) {
		t.Fatal("processAlive reported pid 1 as dead; EPERM must count as alive")
	}
}

// A per-test database name must fit Postgres's 63-byte identifier limit, and it
// must be unique within the process. Truncation would silently make two tests
// share one database, which is the exact failure this design removes.
func TestPerTestDBNameIsShortAndUnique(t *testing.T) {
	t.Parallel()

	const maxIdentifierBytes = 63

	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("%st%d_%d", testDBPrefix, os.Getpid(), i)
		if len(name) > maxIdentifierBytes {
			t.Fatalf("database name %q is %d bytes, over Postgres's %d-byte limit", name, len(name), maxIdentifierBytes)
		}
		if seen[name] {
			t.Fatalf("duplicate database name generated: %q", name)
		}
		seen[name] = true
	}
}
