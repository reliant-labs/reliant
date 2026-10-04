// Copyright (c) 2025 Reliant Labs

package vault_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

func newKeyring(t *testing.T, ids ...string) string {
	t.Helper()
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, id+":"+base64.StdEncoding.EncodeToString(k))
	}
	return strings.Join(parts, ",")
}

func newVault(t *testing.T) (*vault.Vault, *sql.DB) {
	t.Helper()
	_, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	ring, err := crypto.ParseKeyring(newKeyring(t, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	return vault.New(raw, vault.NewEnvKeyWrapper(ring)), raw
}

func TestSealOpenRoundTrip(t *testing.T) {
	v, _ := newVault(t)
	ctx := context.Background()
	alice := vault.UserTenant("alice")
	aad := []byte("api_keys|row1|api_key")

	ct, err := v.Seal(ctx, alice, []byte("sk-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), "sk-secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	pt, err := v.Open(ctx, alice, ct, aad)
	if err != nil || string(pt) != "sk-secret" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	sec, err := v.OpenSecret(ctx, alice, ct, aad)
	if err != nil {
		t.Fatal(err)
	}
	_ = sec.Use(func(b []byte) error {
		if string(b) != "sk-secret" {
			t.Errorf("OpenSecret plaintext = %q", b)
		}
		return nil
	})
}

func TestOpenRejectsTransplantedCiphertext(t *testing.T) {
	v, _ := newVault(t)
	ctx := context.Background()
	alice, bob := vault.UserTenant("alice"), vault.UserTenant("bob")

	ct, err := v.Seal(ctx, alice, []byte("sk-alice"), []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, alice, ct, []byte("row-2")); err == nil {
		t.Error("opened under another row's AAD")
	}
	if _, err := v.Open(ctx, alice, ct, nil); err == nil {
		t.Error("opened with nil AAD")
	}
	if _, err := v.Open(ctx, bob, ct, []byte("row-1")); !errors.Is(err, vault.ErrTenantMismatch) {
		t.Errorf("opened under another user: err = %v, want ErrTenantMismatch", err)
	}
	// Bob has his own DEK; a blob sealed by bob under alice's AAD still must
	// not open as alice.
	ctBob, err := v.Seal(ctx, bob, []byte("sk-bob"), []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, alice, ctBob, []byte("row-1")); err == nil {
		t.Error("alice opened bob's ciphertext")
	}
}

func TestDEKCreationIsLazyAndSingularPerTenant(t *testing.T) {
	v, h := newVault(t)
	ctx := context.Background()
	count := func(tenant string) int {
		var n int
		if err := h.QueryRowContext(ctx,
			`SELECT count(*) FROM vault_keys WHERE tenant_kind='user' AND tenant_id=$1`, tenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("carol"); n != 0 {
		t.Fatalf("DEK exists before first use: %d", n)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Seal(ctx, vault.UserTenant("carol"), []byte("x"), []byte("a")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := count("carol"); n != 1 {
		t.Fatalf("concurrent first use created %d DEKs, want 1", n)
	}

	// A second Vault over the same DB (another process) reuses the row.
	if _, err := v.Seal(ctx, vault.UserTenant("carol"), []byte("y"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	if n := count("carol"); n != 1 {
		t.Fatalf("later Seal created another DEK: %d", n)
	}

	var kekID string
	if err := h.QueryRowContext(ctx, `SELECT kek_id FROM vault_keys WHERE tenant_id='carol'`).Scan(&kekID); err != nil {
		t.Fatal(err)
	}
	if kekID != "v1" {
		t.Errorf("kek_id = %q, want v1", kekID)
	}
}

func TestWrongKEKCannotUnwrap(t *testing.T) {
	v, h := newVault(t)
	ctx := context.Background()
	ct, err := v.Seal(ctx, vault.UserTenant("dave"), []byte("x"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := crypto.ParseKeyring(newKeyring(t, "v1"))
	v2 := vault.New(h, vault.NewEnvKeyWrapper(other))
	if _, err := v2.Open(ctx, vault.UserTenant("dave"), ct, []byte("a")); err == nil {
		t.Fatal("a different KEK unwrapped the DEK")
	}
}
