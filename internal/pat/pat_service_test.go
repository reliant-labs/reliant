// Copyright (c) 2025 Reliant Labs
package pat

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestCreatePATForDaemon_SetsDaemonIDAndStoresHash verifies the managed-daemon
// mint flow: the returned raw token is a valid PAT, the persisted record is
// bound to the daemon_id, the stored token_hash is the SHA-256 of the raw token
// (never the raw token itself), and lookup-by-hash resolves the bound daemon ID.
func TestCreatePATForDaemon_SetsDaemonIDAndStoresHash(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	svc := NewService(repo)

	raw, patID, err := svc.CreatePATForDaemon(ctx, "test-user", "daemon-xyz", "managed", nil)
	if err != nil {
		t.Fatalf("CreatePATForDaemon: %v", err)
	}
	if patID == "" {
		t.Fatal("patID is empty")
	}
	if !auth.IsPATFormat(raw) {
		t.Fatalf("raw token %q is not a valid PAT format", raw)
	}

	// Lookup by hash (what the validator does) must resolve the bound daemon ID.
	got, err := repo.GetDaemonPATByTokenHash(ctx, auth.HashPAT(raw))
	if err != nil {
		t.Fatalf("GetDaemonPATByTokenHash: %v", err)
	}
	if got == nil {
		t.Fatal("PAT not found by hash")
	}
	if got.DaemonID != "daemon-xyz" {
		t.Errorf("DaemonID = %q, want %q", got.DaemonID, "daemon-xyz")
	}
	if got.UserID != "test-user" {
		t.Errorf("UserID = %q, want %q", got.UserID, "test-user")
	}
	if got.Ephemeral {
		t.Errorf("managed daemon PAT must not be ephemeral")
	}
	// The stored hash must be the SHA-256 of the raw token, not the raw token.
	if got.TokenHash == raw {
		t.Errorf("token_hash equals raw token — raw token leaked into storage")
	}
	if got.TokenHash != auth.HashPAT(raw) {
		t.Errorf("token_hash is not SHA-256(raw)")
	}

	// Revocation by daemon ID invalidates the minted token.
	count, err := svc.RevokeManagedDaemonPATs(ctx, "daemon-xyz")
	if err != nil {
		t.Fatalf("RevokeManagedDaemonPATs: %v", err)
	}
	if count != 1 {
		t.Errorf("revoked count = %d, want 1", count)
	}
	// GetDaemonPATByTokenHash only returns live tokens, so the revoked one is gone.
	after, err := repo.GetDaemonPATByTokenHash(ctx, auth.HashPAT(raw))
	if err == nil && after != nil {
		t.Errorf("revoked PAT still resolvable by hash")
	}
}

// TestCreatePATForDaemon_Validation guards the required-arg checks.
func TestCreatePATForDaemon_Validation(t *testing.T) {
	svc := NewService(nil) // repo never reached on validation failure
	ctx := context.Background()

	cases := []struct{ user, daemon, name string }{
		{"", "d", "n"},
		{"u", "", "n"},
		{"u", "d", ""},
	}
	for _, c := range cases {
		if _, _, err := svc.CreatePATForDaemon(ctx, c.user, c.daemon, c.name, nil); err == nil {
			t.Errorf("CreatePATForDaemon(%q,%q,%q) = nil error, want error", c.user, c.daemon, c.name)
		}
	}
}
