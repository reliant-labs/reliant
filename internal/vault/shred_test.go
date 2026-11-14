// Copyright (c) 2025 Reliant Labs

package vault_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

// clock is a settable time source shared by two vaults.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Deleting a tenant's vault_keys rows is the crypto-shred, and it reaches
// every process: two vaults that had the tenant's key cached (each opened a
// blob) both refuse that blob once KeyRecheckInterval has passed, while
// another tenant's blob still opens. Sealing for the shredded tenant again
// starts a new key rather than reusing the forgotten one.
func TestDeletedKeyStopsOpeningInEveryWarmedVault(t *testing.T) {
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	ring, err := crypto.ParseKeyring(newKeyring(t, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}
	a := vault.New(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring), Now: clk.now})
	b := vault.New(vault.Deps{DB: ormOver(t, raw), Keys: vault.NewEnvKeyWrapper(ring), Now: clk.now})
	ctx := context.Background()
	gone, kept := vault.OrgTenant("org-gone"), vault.OrgTenant("org-kept")
	aad := []byte("provider_accounts\x00acct\x00secret_sealed")

	goneBlob, err := a.Seal(ctx, gone, []byte("sk-gone"), aad)
	if err != nil {
		t.Fatal(err)
	}
	keptBlob, err := a.Seal(ctx, kept, []byte("sk-kept"), aad)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []vault.Service{a, b} { // warm both caches
		if _, err := v.Open(ctx, gone, goneBlob, aad); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := raw.ExecContext(ctx, `DELETE FROM vault_keys WHERE tenant_kind = 'org' AND tenant_id = 'org-gone'`); err != nil {
		t.Fatal(err)
	}
	clk.advance(vault.KeyRecheckInterval)

	for name, v := range map[string]vault.Service{"a": a, "b": b} {
		if _, err := v.Open(ctx, gone, goneBlob, aad); !errors.Is(err, vault.ErrKeyNotFound) {
			t.Fatalf("vault %s opened a shredded tenant's blob: err=%v, want ErrKeyNotFound", name, err)
		}
		// Forgotten, not just refused once.
		if _, err := v.Open(ctx, gone, goneBlob, aad); !errors.Is(err, vault.ErrKeyNotFound) {
			t.Fatalf("vault %s reopened a shredded key from its cache: err=%v", name, err)
		}
		plain, err := v.Open(ctx, kept, keptBlob, aad)
		if err != nil || string(plain) != "sk-kept" {
			t.Fatalf("vault %s: another tenant's blob = %q, %v", name, plain, err)
		}
	}

	again, err := a.Seal(ctx, gone, []byte("sk-new"), aad)
	if err != nil {
		t.Fatalf("seal after the shred: %v", err)
	}
	oldID, _ := vault.KeyIDOf(goneBlob)
	newID, _ := vault.KeyIDOf(again)
	if oldID == newID {
		t.Fatal("a seal after the shred reused the forgotten key")
	}
	if plain, err := b.Open(ctx, gone, again, aad); err != nil || string(plain) != "sk-new" {
		t.Fatalf("the new key opens elsewhere: %q, %v", plain, err)
	}
}
