// Copyright (c) 2025 Reliant Labs
package auth

import (
	"testing"
)

// TestDaemonCredentials_DaemonIDRoundTrip verifies the stable daemon_id
// survives a Write -> Read cycle for an origin, and that a second origin's
// entry stays independent (per-origin persistence is the grain that makes
// multiple worktrees on distinct dynamic ports non-colliding).
func TestDaemonCredentials_DaemonIDRoundTrip(t *testing.T) {
	// Isolate ~/.reliant to a temp HOME so we never touch the real file.
	t.Setenv("HOME", t.TempDir())

	const originA = "http://localhost:3123"
	const originB = "http://localhost:8123"

	if err := WriteDaemonCredentials(&DaemonCredentials{
		PAT:       "rlnt_pat_a",
		ServerURL: originA,
		DaemonID:  "daemon-a-stable",
	}); err != nil {
		t.Fatalf("write origin A: %v", err)
	}

	got, err := ReadDaemonCredentials(originA)
	if err != nil {
		t.Fatalf("read origin A: %v", err)
	}
	if got == nil {
		t.Fatal("read origin A: got nil creds")
	}
	if got.DaemonID != "daemon-a-stable" {
		t.Fatalf("origin A daemon_id: got %q, want %q", got.DaemonID, "daemon-a-stable")
	}
	if got.PAT != "rlnt_pat_a" {
		t.Fatalf("origin A pat: got %q, want %q", got.PAT, "rlnt_pat_a")
	}

	// A second origin's entry must be independent — writing it must not
	// disturb origin A's daemon_id, and it carries its own id.
	if err := WriteDaemonCredentials(&DaemonCredentials{
		PAT:       "rlnt_pat_b",
		ServerURL: originB,
		DaemonID:  "daemon-b-stable",
	}); err != nil {
		t.Fatalf("write origin B: %v", err)
	}

	gotA, err := ReadDaemonCredentials(originA)
	if err != nil || gotA == nil {
		t.Fatalf("re-read origin A: creds=%v err=%v", gotA, err)
	}
	if gotA.DaemonID != "daemon-a-stable" {
		t.Fatalf("origin A daemon_id after writing B: got %q, want %q", gotA.DaemonID, "daemon-a-stable")
	}

	gotB, err := ReadDaemonCredentials(originB)
	if err != nil || gotB == nil {
		t.Fatalf("read origin B: creds=%v err=%v", gotB, err)
	}
	if gotB.DaemonID != "daemon-b-stable" {
		t.Fatalf("origin B daemon_id: got %q, want %q", gotB.DaemonID, "daemon-b-stable")
	}
}

// TestDaemonCredentials_DaemonIDOmittedWhenEmpty verifies an empty DaemonID is
// dropped from the on-disk JSON (omitempty) and round-trips back as empty.
func TestDaemonCredentials_DaemonIDOmittedWhenEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:3123"
	if err := WriteDaemonCredentials(&DaemonCredentials{
		PAT:       "rlnt_pat_x",
		ServerURL: origin,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ReadDaemonCredentials(origin)
	if err != nil || got == nil {
		t.Fatalf("read: creds=%v err=%v", got, err)
	}
	if got.DaemonID != "" {
		t.Fatalf("expected empty daemon_id, got %q", got.DaemonID)
	}
}

// TestDeleteDaemonCredentials_RemovesDaemonID verifies logout (delete-entry)
// clears the whole origin entry including its stable daemon_id.
func TestDeleteDaemonCredentials_RemovesDaemonID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:3123"
	if err := WriteDaemonCredentials(&DaemonCredentials{
		PAT:       "rlnt_pat_x",
		ServerURL: origin,
		DaemonID:  "daemon-x",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := DeleteDaemonCredentials(origin); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := ReadDaemonCredentials(origin)
	if err != nil {
		t.Fatalf("read after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil creds after delete, got %+v", got)
	}
}
