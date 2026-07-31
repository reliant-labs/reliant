// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// TestDaemonPATKindStore exercises the unified PAT store end-to-end against a
// real database (migrations included): kind defaulting, kind/user_email
// round-tripping through create + hash lookup, kind-scoped listing, and the
// owner+kind-scoped revoke used by the api-token management surface.
func TestDaemonPATKindStore(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	mk := func(id, userID, kind, name, hash string, expiresAt *time.Time) *DaemonPAT {
		t.Helper()
		p := &DaemonPAT{
			ID:          id,
			UserID:      userID,
			UserEmail:   userID + "@example.com",
			Kind:        kind,
			Name:        name,
			TokenHash:   hash,
			TokenPrefix: "rlnt_pat_" + id,
			ExpiresAt:   expiresAt,
		}
		if err := repo.CreateDaemonPAT(ctx, p); err != nil {
			t.Fatalf("CreateDaemonPAT(%s): %v", id, err)
		}
		return p
	}

	mk("api-1", "user-a", DaemonPATKindAPI, "first", "hash-api-1", nil)
	mk("api-2", "user-a", DaemonPATKindAPI, "second", "hash-api-2", nil)
	mk("daemon-1", "user-a", DaemonPATKindDaemon, "laptop", "hash-daemon-1", nil)
	mk("api-3", "user-b", DaemonPATKindAPI, "other", "hash-api-3", nil)
	// Empty kind must default to 'daemon' (legacy caller shape).
	mk("legacy-1", "user-a", "", "legacy", "hash-legacy-1", nil)

	// Hash lookup round-trips kind and user_email.
	got, err := repo.GetDaemonPATByTokenHash(ctx, "hash-api-1")
	if err != nil {
		t.Fatalf("GetDaemonPATByTokenHash: %v", err)
	}
	if got.Kind != DaemonPATKindAPI {
		t.Errorf("kind = %q, want %q", got.Kind, DaemonPATKindAPI)
	}
	if got.UserEmail != "user-a@example.com" {
		t.Errorf("user_email = %q", got.UserEmail)
	}

	// Empty-kind create landed as 'daemon'.
	if got, err := repo.GetDaemonPATByTokenHash(ctx, "hash-legacy-1"); err != nil {
		t.Fatalf("GetDaemonPATByTokenHash(legacy): %v", err)
	} else if got.Kind != DaemonPATKindDaemon {
		t.Errorf("empty kind stored as %q, want default %q", got.Kind, DaemonPATKindDaemon)
	}

	// Kind-scoped listing.
	apiToks, err := repo.ListDaemonPATsByUserIDAndKind(ctx, "user-a", DaemonPATKindAPI)
	if err != nil {
		t.Fatalf("ListDaemonPATsByUserIDAndKind(api): %v", err)
	}
	if len(apiToks) != 2 {
		t.Fatalf("user-a api tokens = %d, want 2", len(apiToks))
	}
	daemonToks, err := repo.ListDaemonPATsByUserIDAndKind(ctx, "user-a", DaemonPATKindDaemon)
	if err != nil {
		t.Fatalf("ListDaemonPATsByUserIDAndKind(daemon): %v", err)
	}
	if len(daemonToks) != 2 { // daemon-1 + legacy-1
		t.Fatalf("user-a daemon tokens = %d, want 2", len(daemonToks))
	}
	// The unscoped list still returns everything.
	all, err := repo.ListDaemonPATsByUserID(ctx, "user-a")
	if err != nil {
		t.Fatalf("ListDaemonPATsByUserID: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("user-a all tokens = %d, want 4", len(all))
	}

	// Owner+kind-scoped revoke.
	if ok, err := repo.RevokeDaemonPATByUserID(ctx, "user-b", "api-1", DaemonPATKindAPI); err != nil || ok {
		t.Errorf("foreign revoke = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := repo.RevokeDaemonPATByUserID(ctx, "user-a", "daemon-1", DaemonPATKindAPI); err != nil || ok {
		t.Errorf("cross-kind revoke = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := repo.RevokeDaemonPATByUserID(ctx, "user-a", "api-1", DaemonPATKindAPI); err != nil || !ok {
		t.Errorf("owner revoke = (%v, %v), want (true, nil)", ok, err)
	}
	// Idempotent: second revoke reports false.
	if ok, err := repo.RevokeDaemonPATByUserID(ctx, "user-a", "api-1", DaemonPATKindAPI); err != nil || ok {
		t.Errorf("double revoke = (%v, %v), want (false, nil)", ok, err)
	}
	// Revoked rows disappear from the live hash lookup.
	if _, err := repo.GetDaemonPATByTokenHash(ctx, "hash-api-1"); err == nil {
		t.Error("revoked token still resolvable by hash")
	}

	// Expired rows are filtered by the live hash lookup too.
	past := time.Now().Add(-time.Hour).UTC()
	mk("api-4", "user-a", DaemonPATKindAPI, "expired", "hash-api-4", &past)
	if _, err := repo.GetDaemonPATByTokenHash(ctx, "hash-api-4"); err == nil {
		t.Error("expired token still resolvable by hash")
	}
}
