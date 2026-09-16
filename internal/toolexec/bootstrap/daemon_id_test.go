// Copyright (c) 2025 Reliant Labs
package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDaemonID_DistinctPerInstance is the regression guard for the identity
// collision that distinct data directories alone do NOT fix.
//
// The stable daemon id used to live in ~/.reliant/daemon.json keyed by ORIGIN
// alone. Two worktrees on one machine, against one server and one account,
// therefore read back the SAME id and each re-asserted it as
// DaemonRegister.DaemonId — a field the gateway trusts verbatim. The gateway
// keys connections by daemon id, so every registration evicted the other
// daemon and the pair fought until one was killed.
//
// Two instances means two data directories means two ids.
func TestDaemonID_DistinctPerInstance(t *testing.T) {
	instanceA := filepath.Join(t.TempDir(), "worktree-a")
	instanceB := filepath.Join(t.TempDir(), "worktree-b")

	if err := WriteDaemonID(instanceA, "daemon-assigned-a"); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := WriteDaemonID(instanceB, "daemon-assigned-b"); err != nil {
		t.Fatalf("write B: %v", err)
	}

	if got := ReadDaemonID(instanceA); got != "daemon-assigned-a" {
		t.Fatalf("instance A id: got %q, want %q", got, "daemon-assigned-a")
	}
	if got := ReadDaemonID(instanceB); got != "daemon-assigned-b" {
		t.Fatalf("instance B id: got %q, want %q", got, "daemon-assigned-b")
	}
}

// TestDaemonID_AbsentIsEmptyNotAnError pins first-ever registration: no file
// means no id, the daemon registers with an empty one, and the gateway mints
// it. Absence must never be an error — refusing to start is not recoverable,
// whereas re-registering as new always is.
func TestDaemonID_AbsentIsEmptyNotAnError(t *testing.T) {
	if got := ReadDaemonID(t.TempDir()); got != "" {
		t.Fatalf("absent id must read empty, got %q", got)
	}
	if got := ReadDaemonID(""); got != "" {
		t.Fatalf("empty data dir must read empty, got %q", got)
	}
	if got := ReadDaemonID(filepath.Join(t.TempDir(), "never-created")); got != "" {
		t.Fatalf("missing directory must read empty, got %q", got)
	}
}

// TestDaemonID_RoundTripsThroughRestart pins the whole point of persisting it:
// a daemon that stops and starts again re-asserts the identity it already had
// rather than being issued a new one.
func TestDaemonID_RoundTripsThroughRestart(t *testing.T) {
	dataDir := t.TempDir()

	if err := WriteDaemonID(dataDir, "daemon-stable"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadDaemonID(dataDir); got != "daemon-stable" {
		t.Fatalf("after first write: got %q", got)
	}

	// A re-registration that assigns the same id rewrites harmlessly.
	if err := WriteDaemonID(dataDir, "daemon-stable"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got := ReadDaemonID(dataDir); got != "daemon-stable" {
		t.Fatalf("after rewrite: got %q", got)
	}
}

// TestDaemonID_CorruptFileReadsEmpty pins the recovery posture: a truncated or
// garbled file means "register as new", not "fail to start".
func TestDaemonID_CorruptFileReadsEmpty(t *testing.T) {
	dataDir := t.TempDir()

	// A directory where the file should be — unreadable as a file.
	if err := os.MkdirAll(DaemonIDPath(dataDir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := ReadDaemonID(dataDir); got != "" {
		t.Fatalf("unreadable id must read empty, got %q", got)
	}
}

// TestDaemonID_LivesBesideTheRuntimeRecord pins the location. The data
// directory is the one thing that is already one-per-instance, so the id
// belongs in it — anywhere coarser (the per-origin credentials store it came
// from) reintroduces the collision.
func TestDaemonID_LivesBesideTheRuntimeRecord(t *testing.T) {
	dataDir := t.TempDir()
	if got, want := DaemonIDPath(dataDir), filepath.Join(dataDir, DaemonIDFileName); got != want {
		t.Fatalf("id path: got %q, want %q", got, want)
	}
}
