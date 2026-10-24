// Copyright (c) 2025 Reliant Labs

package vault_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

// kekRotation is one database with values sealed under an "old" KEK, plus the
// raw keyring strings a rotation moves between.
type kekRotation struct {
	db       orm.Context
	old, new string // "old:<b64>" and "new:<b64>"
	sealed   map[string][]byte
}

func newKEKRotation(t *testing.T) *kekRotation {
	t.Helper()
	repo, _, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	r := &kekRotation{db: repo.ORM(), old: newKeyring(t, "old"), new: newKeyring(t, "new"), sealed: map[string][]byte{}}
	v := r.vault(t, r.old)
	for _, user := range []string{"alice", "bob", "carol"} {
		ct, err := v.Seal(context.Background(), vault.UserTenant(user), []byte("secret-of-"+user), []byte("aad"))
		if err != nil {
			t.Fatal(err)
		}
		r.sealed[user] = ct
	}
	return r
}

func (r *kekRotation) ring(t *testing.T, raw string) *crypto.Keyring {
	t.Helper()
	ring, err := crypto.ParseKeyring(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func (r *kekRotation) vault(t *testing.T, raw string) vault.Service {
	t.Helper()
	return vault.New(vault.Deps{DB: r.db, Keys: vault.NewEnvKeyWrapper(r.ring(t, raw))})
}

// wrappings snapshots every stored wrapping, to prove what a step did and did not write.
func (r *kekRotation) wrappings(t *testing.T) map[string][]byte {
	t.Helper()
	var rows []struct {
		ID         string `bun:"id"`
		WrappedDek []byte `bun:"wrapped_dek"`
	}
	if err := r.db.Bun().NewSelect().Table("vault_keys").Column("id", "wrapped_dek").Scan(context.Background(), &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, row := range rows {
		out[row.ID] = row.WrappedDek
	}
	return out
}

func TestRewrapMovesEveryDataKeyAndKeepsSealedValuesOpen(t *testing.T) {
	r := newKEKRotation(t)
	ctx := context.Background()
	both := r.ring(t, r.new+","+r.old)

	// Before the rewrap, verify refuses: every key still needs the old KEK.
	if _, err := vault.VerifyWrapped(ctx, r.db, both); err == nil || !strings.Contains(err.Error(), "not wrapped under the primary KEK new") {
		t.Fatalf("VerifyWrapped before rewrap = %v, want not-on-primary", err)
	}

	before := r.wrappings(t)
	dry, err := vault.Rewrap(ctx, r.db, both, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Total != 3 || dry.Pending != 3 || dry.Rewrapped != 0 || dry.ByKEK["old"] != 3 {
		t.Fatalf("dry run = %+v, want 3 pending on old, none rewrapped", dry)
	}
	for id, w := range r.wrappings(t) {
		if !bytes.Equal(w, before[id]) {
			t.Fatalf("dry run rewrote %s", id)
		}
	}

	rep, err := vault.Rewrap(ctx, r.db, both, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 3 || rep.Rewrapped != 3 || rep.Primary != "new" {
		t.Fatalf("rewrap = %+v, want 3 rewrapped onto new", rep)
	}
	again, err := vault.Rewrap(ctx, r.db, both, false)
	if err != nil || again.Rewrapped != 0 || again.ByKEK["new"] != 3 {
		t.Fatalf("second rewrap = %+v, %v; want a no-op", again, err)
	}

	// The old KEK can go: verify passes under the new KEK alone, and every value
	// sealed before the rotation opens through a vault that has only the new KEK.
	newOnly := r.ring(t, r.new)
	if _, err := vault.VerifyWrapped(ctx, r.db, newOnly); err != nil {
		t.Fatalf("VerifyWrapped under the new KEK alone: %v", err)
	}
	v := r.vault(t, r.new)
	for user, ct := range r.sealed {
		pt, err := v.Open(ctx, vault.UserTenant(user), ct, []byte("aad"))
		if err != nil || string(pt) != "secret-of-"+user {
			t.Fatalf("Open(%s) after rotation = %q, %v", user, pt, err)
		}
	}
	// And the old KEK alone opens nothing any more.
	if _, err := r.vault(t, r.old).Open(ctx, vault.UserTenant("alice"), r.sealed["alice"], []byte("aad")); err == nil {
		t.Fatal("the retired KEK still unwraps a data key")
	}
}

func TestRewrapWritesNothingWhenAKeyDoesNotOpen(t *testing.T) {
	r := newKEKRotation(t)
	ctx := context.Background()
	before := r.wrappings(t)

	// The old KEK is missing from the ring: no key opens, so nothing may move.
	_, err := vault.Rewrap(ctx, r.db, r.ring(t, r.new), false)
	if err == nil || !strings.Contains(err.Error(), "wrapped under KEK old") {
		t.Fatalf("Rewrap without the old KEK = %v, want does-not-open", err)
	}
	for id, w := range r.wrappings(t) {
		if !bytes.Equal(w, before[id]) {
			t.Fatalf("a failed rewrap rewrote %s", id)
		}
	}
}

func TestDataKeysCreatedMidRotationLandOnTheNewKEK(t *testing.T) {
	r := newKEKRotation(t)
	ctx := context.Background()
	// Step 1 of the rotation: servers run with "new,old". A first seal for a new
	// tenant wraps its DEK under new, so the rewrap has nothing to do for it.
	if _, err := r.vault(t, r.new+","+r.old).Seal(ctx, vault.UserTenant("dave"), []byte("x"), nil); err != nil {
		t.Fatal(err)
	}
	rep, err := vault.Rewrap(ctx, r.db, r.ring(t, r.new+","+r.old), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 4 || rep.ByKEK["new"] != 1 || rep.Pending != 3 {
		t.Fatalf("dry run = %+v, want dave on new and the 3 originals pending", rep)
	}
}
