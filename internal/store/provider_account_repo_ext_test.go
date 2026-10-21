// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Tests for the accounts lane (P5b L6): provider_accounts, provider_account_limits,
// thread_provider_accounts and legacy_credential_copy. Each ported statement is checked against
// the Repository on the same rows (internal/db/SCHEMA.md rule 6): writes through one layer,
// reads through the other. The Repository's AccountStore seals with storetest's throwaway
// vault, so an account written through it carries real ciphertext.

func l6Upsert(user, acct, secret, refresh string) core.AccountUpsert {
	return core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey(acct, "org"), Email: acct + "@example.com",
		ExternalAccountID: acct, OrganizationID: "org", OrganizationName: "Org",
		Secrets:   core.AccountSecrets{Secret: secret, Refresh: refresh},
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
}

func l6Account(t *testing.T, env *storetest.Env, u core.AccountUpsert) core.ProviderAccount {
	t.Helper()
	a, _, err := env.Repo.UpsertProviderAccount(context.Background(), u)
	if err != nil {
		t.Fatalf("Repo.UpsertProviderAccount(%s): %v", u.IdentityKey, err)
	}
	return a
}

func l6Sealed(t *testing.T, env *storetest.Env, accountID string) (secret, refresh, idToken []byte) {
	t.Helper()
	if err := env.SQL.QueryRow(`SELECT secret_sealed, refresh_sealed, id_token_sealed FROM provider_accounts WHERE id = $1`,
		accountID).Scan(&secret, &refresh, &idToken); err != nil {
		t.Fatalf("read sealed columns of %s: %v", accountID, err)
	}
	return secret, refresh, idToken
}

// l6TryInOtherSession runs a boolean probe in a transaction of its own (another session than
// the ORM's), and rolls it back so nothing it took outlives the probe.
func l6TryInOtherSession(t *testing.T, env *storetest.Env, probe string, args ...any) bool {
	t.Helper()
	tx, err := env.SQL.Begin()
	if err != nil {
		t.Fatalf("begin probe: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var got bool
	if err := tx.QueryRow(probe, args...).Scan(&got); err != nil {
		t.Fatalf("probe %q: %v", probe, err)
	}
	return got
}

// l6InTx runs f in one ORM transaction, as AccountStore does.
func l6InTx(t *testing.T, env *storetest.Env, f func(ctx context.Context) error) {
	t.Helper()
	if err := env.ORM.RunTx(context.Background(), orm.Serializable, f); err != nil {
		t.Fatalf("transaction: %v", err)
	}
}

// TestProviderAccountSecretsRoundTripSealed: a secret written through the Repository (whose
// AccountStore seals, then writes through internal/store) is ciphertext on disk, the store
// hands back exactly those bytes, and it opens to the plaintext again.
func TestProviderAccountSecretsRoundTripSealed(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	u := l6Upsert("u1", "alice", "PLAINTEXT-ACCESS", "PLAINTEXT-REFRESH")
	u.Secrets.IDToken = "PLAINTEXT-ID"
	a := l6Account(t, env, u)

	secret, refresh, idToken := l6Sealed(t, env, a.ID)
	for name, pair := range map[string][2][]byte{
		"secret": {secret, []byte("PLAINTEXT-ACCESS")}, "refresh": {refresh, []byte("PLAINTEXT-REFRESH")}, "id_token": {idToken, []byte("PLAINTEXT-ID")},
	} {
		if len(pair[0]) == 0 || bytes.Contains(pair[0], pair[1]) {
			t.Fatalf("%s_sealed = %q, want ciphertext that does not contain the plaintext", name, pair[0])
		}
	}

	got, err := store.GetUserProviderAccountSealed(ctx, env.ORM, "u1", a.ID)
	if err != nil {
		t.Fatalf("GetUserProviderAccountSealed: %v", err)
	}
	if !bytes.Equal(got.Secret, secret) || !bytes.Equal(got.Refresh, refresh) || !bytes.Equal(got.IDToken, idToken) {
		t.Fatal("GetUserProviderAccountSealed did not return the stored ciphertext byte for byte")
	}
	opened, err := env.Repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
	if err != nil {
		t.Fatalf("Repo.GetProviderAccountWithSecrets: %v", err)
	}
	if want := (core.AccountSecrets{Secret: "PLAINTEXT-ACCESS", Refresh: "PLAINTEXT-REFRESH", IDToken: "PLAINTEXT-ID"}); opened.Secrets != want {
		t.Fatalf("opened secrets = %+v, want %+v", opened.Secrets, want)
	}

	// A token swap re-seals: new ciphertext, still not plaintext, still opens.
	ok, err := env.Repo.CompareAndSwapAccountTokens(ctx, "u1", a.ID, "PLAINTEXT-REFRESH", core.AccountTokens{Secret: "NEXT-ACCESS", Refresh: "NEXT-REFRESH"})
	if err != nil || !ok {
		t.Fatalf("CompareAndSwapAccountTokens = %v, %v; want a swap", ok, err)
	}
	secret2, _, _ := l6Sealed(t, env, a.ID)
	if bytes.Equal(secret2, secret) || bytes.Contains(secret2, []byte("NEXT-ACCESS")) {
		t.Fatalf("secret_sealed after a swap = %q, want fresh ciphertext", secret2)
	}
	opened, err = env.Repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
	if err != nil || opened.Secrets.Secret != "NEXT-ACCESS" || opened.Secrets.Refresh != "NEXT-REFRESH" || opened.Secrets.IDToken != "PLAINTEXT-ID" {
		t.Fatalf("after the swap opened = %+v, %v; want the next tokens and the kept id token", opened, err)
	}
}

// TestProviderAccountReads ports the metadata and sealed reads.
func TestProviderAccountReads(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	a := l6Account(t, env, l6Upsert("u1", "alice", "s1", "r1"))
	b := l6Account(t, env, l6Upsert("u1", "bob", "s2", ""))
	key := l6Account(t, env, core.AccountUpsert{UserID: "u1", Provider: "openai", AuthKind: core.AuthKindAPIKey,
		IdentityKey: "k1", KeyHint: "abcd", Secrets: core.AccountSecrets{Secret: "sk-1"}})
	other := l6Account(t, env, l6Upsert("u2", "carol", "s3", "r3"))

	meta, err := store.ListUserProviderAccounts(ctx, env.ORM, "u1")
	if err != nil {
		t.Fatalf("ListUserProviderAccounts: %v", err)
	}
	twin, err := env.Repo.ListProviderAccounts(ctx, "u1")
	if err != nil {
		t.Fatalf("Repo.ListProviderAccounts: %v", err)
	}
	requireSame(t, "ListUserProviderAccounts", meta, twin)
	if len(meta) != 3 || meta[0].ID != a.ID || meta[1].ID != b.ID || meta[2].ID != key.ID {
		t.Fatalf("ListUserProviderAccounts order = %v, want claude by priority, then openai", meta)
	}

	sealed, err := store.ListUserProviderAccountsSealed(ctx, env.ORM, "u1")
	if err != nil {
		t.Fatalf("ListUserProviderAccountsSealed: %v", err)
	}
	withSecrets, err := env.Repo.ListProviderAccountsWithSecrets(ctx, "u1")
	if err != nil {
		t.Fatalf("Repo.ListProviderAccountsWithSecrets: %v", err)
	}
	if len(sealed) != len(withSecrets) {
		t.Fatalf("sealed list has %d rows, the Repository's %d", len(sealed), len(withSecrets))
	}
	for i := range sealed {
		requireSame(t, "ListUserProviderAccountsSealed metadata", sealed[i].ProviderAccount, withSecrets[i].ProviderAccount)
		s, r, _ := l6Sealed(t, env, sealed[i].ID)
		if !bytes.Equal(sealed[i].Secret, s) || !bytes.Equal(sealed[i].Refresh, r) {
			t.Fatalf("sealed list row %s does not carry the stored ciphertext", sealed[i].ID)
		}
	}
	if sealed[1].Refresh != nil {
		t.Fatalf("bob has no refresh token, got ciphertext %q", sealed[1].Refresh)
	}

	got, err := store.GetUserProviderAccountSealed(ctx, env.ORM, "u1", a.ID)
	if err != nil {
		t.Fatalf("GetUserProviderAccountSealed: %v", err)
	}
	requireSame(t, "GetUserProviderAccountSealed", got.ProviderAccount, withSecrets[0].ProviderAccount)
	if _, err := store.GetUserProviderAccountSealed(ctx, env.ORM, "u1", other.ID); !errors.Is(err, core.ErrProviderAccountNotFound) {
		t.Fatalf("another user's account: err = %v, want ErrProviderAccountNotFound", err)
	}
	if _, err := env.Repo.GetProviderAccountWithSecrets(ctx, "u1", other.ID); !errors.Is(err, core.ErrProviderAccountNotFound) {
		t.Fatalf("Repo: another user's account: err = %v, want ErrProviderAccountNotFound", err)
	}

	users, err := store.ListUserIDsWithProviderAccount(ctx, env.ORM, "claude")
	if err != nil {
		t.Fatalf("ListUserIDsWithProviderAccount: %v", err)
	}
	twinUsers, err := env.Repo.ListUserIDsWithProviderAccount(ctx, "claude")
	if err != nil {
		t.Fatalf("Repo.ListUserIDsWithProviderAccount: %v", err)
	}
	requireSame(t, "ListUserIDsWithProviderAccount", users, twinUsers)
	if fmt.Sprint(users) != "[u1 u2]" {
		t.Fatalf("users with a claude account = %v, want [u1 u2] (distinct, by id)", users)
	}

	next, err := store.NextProviderAccountPriority(ctx, env.ORM, "u1", "claude")
	if err != nil || next != 2 {
		t.Fatalf("NextProviderAccountPriority(claude) = %d, %v; want 2", next, err)
	}
	if next, err := store.NextProviderAccountPriority(ctx, env.ORM, "u1", "gemini"); err != nil || next != 0 {
		t.Fatalf("NextProviderAccountPriority(none) = %d, %v; want 0", next, err)
	}
	if src, err := store.ProviderAccountLegacySource(ctx, env.ORM, a.ID); err != nil || src != nil {
		t.Fatalf("ProviderAccountLegacySource(native) = %v, %v; want nil", src, err)
	}
	if user, err := store.UserProviderAccountProvider(ctx, env.ORM, "u1", key.ID); err != nil || user != "openai" {
		t.Fatalf("UserProviderAccountProvider = %q, %v; want openai", user, err)
	}
	if _, err := store.UserProviderAccountProvider(ctx, env.ORM, "u2", key.ID); !errors.Is(err, core.ErrProviderAccountNotFound) {
		t.Fatalf("UserProviderAccountProvider(another user) err = %v, want ErrProviderAccountNotFound", err)
	}
}

// TestProviderAccountLocks: the row locks and the advisory lock. The advisory key is the one
// every earlier release takes, so old and new writers still serialize.
func TestProviderAccountLocks(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	a := l6Account(t, env, l6Upsert("u1", "alice", "s1", "r1"))
	b := l6Account(t, env, l6Upsert("u1", "bob", "s2", "r2"))
	mustExec(t, env, `UPDATE provider_accounts SET legacy_source = 'claude_auth_tokens' WHERE id IN ($1, $2)`, a.ID, b.ID)
	twin, err := env.Repo.ListProviderAccounts(ctx, "u1")
	if err != nil {
		t.Fatalf("Repo.ListProviderAccounts: %v", err)
	}

	l6InTx(t, env, func(ctx context.Context) error {
		if err := store.LockUserProviderAccounts(ctx, env.ORM, "u1", "claude"); err != nil {
			return err
		}
		// The previous release's key, taken from another session, is held.
		key := fmt.Sprintf("provider-accounts:%d:%s:%s", len("u1"), "u1", "claude")
		if l6TryInOtherSession(t, env, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, key) {
			t.Error("the store's advisory lock is not the previous release's key")
		}

		byIdentity, err := store.LockProviderAccountByIdentity(ctx, env.ORM, "u1", "claude", a.IdentityKey)
		if err != nil {
			return err
		}
		requireSame(t, "LockProviderAccountByIdentity", *byIdentity, twin[0])
		if none, err := store.LockProviderAccountByIdentity(ctx, env.ORM, "u1", "claude", "nobody"); err != nil || none != nil {
			t.Errorf("LockProviderAccountByIdentity(absent) = %v, %v; want nil, nil", none, err)
		}
		byID, err := store.LockUserProviderAccount(ctx, env.ORM, "u1", b.ID)
		if err != nil {
			return err
		}
		requireSame(t, "LockUserProviderAccount", *byID, twin[1])
		if _, err := store.LockUserProviderAccount(ctx, env.ORM, "u2", b.ID); !errors.Is(err, core.ErrProviderAccountNotFound) {
			t.Errorf("LockUserProviderAccount(another user) err = %v, want ErrProviderAccountNotFound", err)
		}
		mirror, err := store.LockLegacyMirror(ctx, env.ORM, "u1", "claude", "claude_auth_tokens")
		if err != nil {
			return err
		}
		if mirror == nil || mirror.ID != a.ID {
			t.Errorf("LockLegacyMirror = %v, want the first mirror by priority (%s)", mirror, a.ID)
		}
		if none, err := store.LockLegacyMirror(ctx, env.ORM, "u1", "claude", "api_keys"); err != nil || none != nil {
			t.Errorf("LockLegacyMirror(other source) = %v, %v; want nil, nil", none, err)
		}
		ids, err := store.LockProviderAccountIDs(ctx, env.ORM, "u1", "claude")
		if err != nil {
			return err
		}
		if len(ids) != 2 {
			t.Errorf("LockProviderAccountIDs = %v, want both accounts", ids)
		}
		// The rows are locked: another session cannot take them.
		if l6TryInOtherSession(t, env, `SELECT EXISTS (SELECT 1 FROM provider_accounts WHERE id = $1 FOR UPDATE SKIP LOCKED)`, a.ID) {
			t.Error("a row read FOR UPDATE was not locked")
		}
		return nil
	})
	if src, err := store.ProviderAccountLegacySource(ctx, env.ORM, a.ID); err != nil || src == nil || *src != "claude_auth_tokens" {
		t.Fatalf("ProviderAccountLegacySource = %v, %v; want claude_auth_tokens", src, err)
	}
}

// TestProviderAccountCredentialWrites ports the insert and the two credential rewrites.
func TestProviderAccountCredentialWrites(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	inserted, err := store.InsertProviderAccount(ctx, env.ORM, "acct-1", "u1", "openai", 0, store.ProviderAccountCredential{
		AuthKind: core.AuthKindAPIKey, IdentityKey: "k1", KeyHint: "wxyz", Secret: []byte("ciphertext-1"),
		LegacySource: "api_keys", UpdatedAt: at.Add(time.Minute),
	}, at)
	if err != nil {
		t.Fatalf("InsertProviderAccount: %v", err)
	}
	twin, err := env.Repo.ListProviderAccounts(ctx, "u1")
	if err != nil || len(twin) != 1 {
		t.Fatalf("Repo.ListProviderAccounts = %v, %v", twin, err)
	}
	requireSame(t, "InsertProviderAccount", inserted, twin[0])
	if inserted.Email != "" || !inserted.ExpiresAt.IsZero() || !inserted.CreatedAt.Equal(at) || !inserted.UpdatedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("inserted = %+v, want NULL email/expiry and the caller's timestamps", inserted)
	}
	if src, _ := store.ProviderAccountLegacySource(ctx, env.ORM, "acct-1"); src == nil || *src != "api_keys" {
		t.Fatalf("legacy_source = %v, want api_keys", src)
	}
	if _, err := store.InsertProviderAccount(ctx, env.ORM, "acct-2", "u1", "openai", 1, store.ProviderAccountCredential{
		AuthKind: core.AuthKindAPIKey, IdentityKey: "k1", Secret: []byte("x"), UpdatedAt: at}, at); err == nil {
		t.Fatal("a second account with the same identity inserted")
	}

	// The Repository's write is read back by the store's rewrite (which keeps what is empty).
	a := l6Account(t, env, l6Upsert("u1", "alice", "s1", "r1"))
	_, refresh, _ := l6Sealed(t, env, a.ID)
	rewritten, err := store.RewriteProviderAccountCredential(ctx, env.ORM, "u1", a.ID, store.ProviderAccountCredential{
		AuthKind: core.AuthKindOAuth, IdentityKey: a.IdentityKey, Plan: "max", Secret: []byte("ciphertext-2"), UpdatedAt: at.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("RewriteProviderAccountCredential: %v", err)
	}
	if rewritten.Email != a.Email || rewritten.Plan != "max" || !rewritten.ExpiresAt.IsZero() || !rewritten.UpdatedAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("rewritten = %+v, want email kept, plan set, expiry cleared, updated_at stamped", rewritten)
	}
	secret2, refresh2, _ := l6Sealed(t, env, a.ID)
	if string(secret2) != "ciphertext-2" || !bytes.Equal(refresh2, refresh) {
		t.Fatal("a rewrite without a refresh token must keep the stored one and write the secret")
	}
	twin, _ = env.Repo.ListProviderAccounts(ctx, "u1")
	requireSame(t, "RewriteProviderAccountCredential", rewritten, twin[0])
	mustExec(t, env, `UPDATE provider_accounts SET needs_reauth_at = now(), needs_reauth_reason = 'expired' WHERE id = $1`, a.ID)
	if r, err := store.RewriteProviderAccountCredential(ctx, env.ORM, "u1", a.ID, store.ProviderAccountCredential{
		AuthKind: core.AuthKindOAuth, IdentityKey: a.IdentityKey, Secret: []byte("ciphertext-3"), UpdatedAt: at}); err != nil || r.NeedsReauthAt != nil || r.NeedsReauthReason != "" {
		t.Fatalf("a rewrite must clear needs-reauth: %+v, %v", r, err)
	}
	if _, err := store.RewriteProviderAccountCredential(ctx, env.ORM, "u2", a.ID, store.ProviderAccountCredential{
		AuthKind: core.AuthKindOAuth, IdentityKey: "x", Secret: []byte("x"), UpdatedAt: at}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rewriting another user's account: err = %v, want no row", err)
	}

	// The compare-and-swap writes only while the witness holds; a NULL refresh is a witness too.
	b := l6Account(t, env, l6Upsert("u1", "bob", "s2", ""))
	wSecret, wRefresh, found, err := store.ProviderAccountWitness(ctx, env.ORM, "u1", b.ID)
	if err != nil || !found || wRefresh != nil || len(wSecret) == 0 {
		t.Fatalf("ProviderAccountWitness = %q, %q, %v, %v; want the secret and a NULL refresh", wSecret, wRefresh, found, err)
	}
	if _, _, found, err := store.ProviderAccountWitness(ctx, env.ORM, "u2", b.ID); err != nil || found {
		t.Fatalf("ProviderAccountWitness(another user) found=%v, %v", found, err)
	}
	next := store.ProviderAccountCredential{Secret: []byte("swapped"), Refresh: []byte("swapped-refresh"), Plan: "pro", UpdatedAt: at}
	if ok, err := store.SwapProviderAccountTokens(ctx, env.ORM, "u1", b.ID, next, []byte("stale"), wRefresh); err != nil || ok {
		t.Fatalf("swap on a stale witness = %v, %v; want no write", ok, err)
	}
	if ok, err := store.SwapProviderAccountTokens(ctx, env.ORM, "u1", b.ID, next, wSecret, wRefresh); err != nil || !ok {
		t.Fatalf("swap on the current witness = %v, %v; want a write", ok, err)
	}
	if ok, err := store.SwapProviderAccountTokens(ctx, env.ORM, "u1", b.ID, next, wSecret, wRefresh); err != nil || ok {
		t.Fatalf("a second swap on the same witness = %v, %v; want no write", ok, err)
	}
	twin, _ = env.Repo.ListProviderAccounts(ctx, "u1")
	for _, tw := range twin {
		if tw.ID == b.ID && (tw.Plan != "pro" || tw.Email != b.Email || !tw.UpdatedAt.Equal(at)) {
			t.Fatalf("after the swap the Repository reads %+v", tw)
		}
	}
}

// TestProviderAccountStandingWrites ports the patch, reorder, merge and delete statements.
func TestProviderAccountStandingWrites(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	a := l6Account(t, env, l6Upsert("u1", "alice", "s1", "r1"))
	b := l6Account(t, env, l6Upsert("u1", "bob", "s2", "r2"))
	c := l6Account(t, env, l6Upsert("u1", "carol", "s3", "r3"))

	patched, err := store.PatchUserProviderAccount(ctx, env.ORM, "u1", a.ID, "Work", &now, &now, "revoked", now)
	if err != nil {
		t.Fatalf("PatchUserProviderAccount: %v", err)
	}
	twin, _ := env.Repo.ListProviderAccounts(ctx, "u1")
	requireSame(t, "PatchUserProviderAccount", patched, twin[0])
	if patched.Label != "Work" || patched.PausedAt == nil || patched.NeedsReauthReason != "revoked" || !patched.UpdatedAt.Equal(now) {
		t.Fatalf("patched = %+v", patched)
	}
	// And the Repository's patch reads back through the store.
	label := "Home"
	if _, err := env.Repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{Label: &label, Paused: ptr(false), NeedsReauth: ptr("")}); err != nil {
		t.Fatalf("Repo.PatchProviderAccount: %v", err)
	}
	meta, _ := store.ListUserProviderAccounts(ctx, env.ORM, "u1")
	if meta[0].Label != "Home" || meta[0].PausedAt != nil || meta[0].NeedsReauthAt != nil || meta[0].NeedsReauthReason != "" {
		t.Fatalf("after Repo.PatchProviderAccount the store reads %+v", meta[0])
	}
	if _, err := store.PatchUserProviderAccount(ctx, env.ORM, "u2", a.ID, "", nil, nil, "", now); !errors.Is(err, core.ErrProviderAccountNotFound) {
		t.Fatalf("patching another user's account: err = %v, want ErrProviderAccountNotFound", err)
	}

	// Priorities: the store's moves, then the Repository's reorder read back by the store.
	l6InTx(t, env, func(ctx context.Context) error {
		for i, id := range []string{c.ID, a.ID, b.ID} {
			if err := store.SetProviderAccountPriority(ctx, env.ORM, id, i, now); err != nil {
				return err
			}
		}
		return nil
	})
	if got := l6IDs(t, env); got != fmt.Sprint([]string{c.ID, a.ID, b.ID}) {
		t.Fatalf("after SetProviderAccountPriority the Repository orders %s", got)
	}
	if err := env.Repo.ReorderProviderAccounts(ctx, "u1", "claude", []string{a.ID, b.ID, c.ID}); err != nil {
		t.Fatalf("Repo.ReorderProviderAccounts: %v", err)
	}
	meta, _ = store.ListUserProviderAccounts(ctx, env.ORM, "u1")
	if meta[0].ID != a.ID || meta[1].ID != b.ID || meta[2].ID != c.ID {
		t.Fatalf("after the Repository's reorder the store orders %v", meta)
	}

	// The merge's statements: standing, assignments, delete, densify.
	seedChatThread(t, env, "chat-l6", "th-1")
	if err := store.CreateThread(ctx, env.ORM, &store.Thread{ID: "th-2", ChatID: "chat-l6", Origin: "main", CreatedAt: now}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for _, as := range []core.Assignment{
		{ThreadID: "th-1", Provider: "claude", AccountID: b.ID, Reason: "initial", AssignedAt: now},
		{ThreadID: "th-2", Provider: "claude", AccountID: c.ID, Reason: "initial", AssignedAt: now},
	} {
		if _, err := env.Repo.SetThreadAssignment(ctx, as, ""); err != nil {
			t.Fatalf("Repo.SetThreadAssignment: %v", err)
		}
	}
	before, _ := store.GetUserProviderAccountSealed(ctx, env.ORM, "u1", b.ID)
	l6InTx(t, env, func(ctx context.Context) error {
		if err := store.MoveThreadAssignments(ctx, env.ORM, c.ID, b.ID); err != nil {
			return err
		}
		if err := store.RemoveProviderAccount(ctx, env.ORM, c.ID); err != nil {
			return err
		}
		if err := store.RemoveProviderAccount(ctx, env.ORM, c.ID); err != nil {
			return fmt.Errorf("removing an absent account must not fail: %w", err)
		}
		if err := store.SetProviderAccountStanding(ctx, env.ORM, b.ID, 5, "Merged", &now); err != nil {
			return err
		}
		return store.DensifyProviderAccounts(ctx, env.ORM, "u1", "claude")
	})
	twin, _ = env.Repo.ListProviderAccounts(ctx, "u1")
	if len(twin) != 2 || twin[0].ID != a.ID || twin[0].Priority != 0 || twin[1].ID != b.ID || twin[1].Priority != 1 ||
		twin[1].Label != "Merged" || twin[1].PausedAt == nil || !twin[1].UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("after the merge statements the Repository reads %+v; want alice 0, then bob densified from 5 to 1 with its standing set and updated_at untouched", twin)
	}
	for _, th := range []string{"th-1", "th-2"} {
		got, err := env.Repo.GetThreadAssignment(ctx, th, "claude")
		if err != nil || got == nil || got.AccountID != b.ID {
			t.Fatalf("thread %s assignment = %+v, %v; want moved to bob", th, got, err)
		}
	}

	// Delete: the Repository's account deleted through the store, scoped to its user.
	if ok, err := store.DeleteUserProviderAccount(ctx, env.ORM, "u2", a.ID); err != nil || ok {
		t.Fatalf("deleting another user's account = %v, %v", ok, err)
	}
	if ok, err := store.DeleteUserProviderAccount(ctx, env.ORM, "u1", a.ID); err != nil || !ok {
		t.Fatalf("DeleteUserProviderAccount = %v, %v", ok, err)
	}
	if _, err := env.Repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID); !errors.Is(err, core.ErrProviderAccountNotFound) {
		t.Fatalf("after the delete the Repository reads err = %v", err)
	}
}

func l6IDs(t *testing.T, env *storetest.Env) string {
	t.Helper()
	all, err := env.Repo.ListProviderAccounts(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Repo.ListProviderAccounts: %v", err)
	}
	ids := make([]string, len(all))
	for i, a := range all {
		ids[i] = a.ID
	}
	return fmt.Sprint(ids)
}

// TestProviderAccountLimitQueries ports provider_account_limits.
func TestProviderAccountLimitQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	a := l6Account(t, env, l6Upsert("u1", "alice", "s1", "r1"))
	b := l6Account(t, env, l6Upsert("u1", "bob", "s2", "r2"))
	now := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	used := 0.5

	// Absent state reads as none through both.
	if l, err := store.GetAccountLimitState(ctx, env.ORM, a.ID); err != nil || l != nil {
		t.Fatalf("GetAccountLimitState(none) = %v, %v", l, err)
	}

	// Snapshot through the store, read through the Repository, and the reverse.
	snap := core.LimitState{AccountID: a.ID, UserID: "u1", Status: "allowed", Source: "headers", ObservedAt: now,
		RepresentativeWindow: "5h", Windows: []core.LimitWindow{{Name: "5h", UsedFraction: &used, ResetsAt: now.Add(time.Hour)}}}
	if err := store.UpsertAccountLimitSnapshot(ctx, env.ORM, snap, now); err != nil {
		t.Fatalf("UpsertAccountLimitSnapshot: %v", err)
	}
	if err := env.Repo.UpsertAccountLimitSnapshot(ctx, core.LimitState{AccountID: b.ID, UserID: "u1", ObservedAt: now, Source: "usage_api"}); err != nil {
		t.Fatalf("Repo.UpsertAccountLimitSnapshot: %v", err)
	}
	// An older snapshot never regresses the state, through either layer.
	stale := snap
	stale.Status, stale.ObservedAt = "rejected", now.Add(-time.Minute)
	if err := store.UpsertAccountLimitSnapshot(ctx, env.ORM, stale, now); err != nil {
		t.Fatalf("UpsertAccountLimitSnapshot(stale): %v", err)
	}
	// A snapshot for another user's account (or a deleted one) inserts nothing.
	if err := store.UpsertAccountLimitSnapshot(ctx, env.ORM, core.LimitState{AccountID: a.ID, UserID: "u2", ObservedAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatalf("UpsertAccountLimitSnapshot(other user): %v", err)
	}
	l6RequireLimitsAgree(t, env, "u1", []string{a.ID, b.ID})
	got, _ := store.GetAccountLimitState(ctx, env.ORM, a.ID)
	if got.Status != "allowed" || len(got.Windows) != 1 || *got.Windows[0].UsedFraction != 0.5 || !got.LastTrafficAt.Equal(now) || got.UserID != "u1" {
		t.Fatalf("limit state = %+v", got)
	}
	gotB, _ := store.GetAccountLimitState(ctx, env.ORM, b.ID)
	if gotB.Status != "unknown" || gotB.Windows == nil || len(gotB.Windows) != 0 || !gotB.LastTrafficAt.IsZero() {
		t.Fatalf("defaults: limit state = %+v; want unknown, no windows, no traffic stamp from a usage-API snapshot", gotB)
	}

	// Bench: GREATEST, probes count only while probing; an "allowed" snapshot lifts it.
	if ok, err := store.BenchProviderAccount(ctx, env.ORM, "u1", a.ID, now.Add(2*time.Hour), "5h", true, now); err != nil || !ok {
		t.Fatalf("BenchProviderAccount = %v, %v", ok, err)
	}
	if err := env.Repo.BenchAccount(ctx, "u1", a.ID, now.Add(time.Hour), "1h", true); err != nil {
		t.Fatalf("Repo.BenchAccount: %v", err)
	}
	got, _ = store.GetAccountLimitState(ctx, env.ORM, a.ID)
	if got.Status != "rejected" || !got.LimitedUntil.Equal(now.Add(2*time.Hour)) || got.LimitedWindow != "5h" || got.LimitedProbes != 2 {
		t.Fatalf("after two benches = %+v; want the later bench and window kept, 2 probes", got)
	}
	if ok, err := store.BenchProviderAccount(ctx, env.ORM, "u1", a.ID, now.Add(3*time.Hour), "7d", false, now); err != nil || !ok {
		t.Fatalf("BenchProviderAccount(no probe) = %v, %v", ok, err)
	}
	l6RequireLimitsAgree(t, env, "u1", []string{a.ID, b.ID})
	got, _ = store.GetAccountLimitState(ctx, env.ORM, a.ID)
	if got.LimitedProbes != 0 || got.LimitedWindow != "7d" {
		t.Fatalf("a known-reset bench resets the probes: %+v", got)
	}
	if ok, err := store.BenchProviderAccount(ctx, env.ORM, "u2", a.ID, now, "", false, now); err != nil || ok {
		t.Fatalf("benching another user's account = %v, %v; want no account", ok, err)
	}
	lift := snap
	lift.ObservedAt = time.Now().UTC().Add(time.Hour)
	if err := env.Repo.UpsertAccountLimitSnapshot(ctx, lift); err != nil {
		t.Fatalf("Repo.UpsertAccountLimitSnapshot(lift): %v", err)
	}
	got, _ = store.GetAccountLimitState(ctx, env.ORM, a.ID)
	if got.LimitedUntil != nil || got.LimitedWindow != "" || got.LimitedProbes != 0 {
		t.Fatalf("an allowed snapshot lifts the bench: %+v", got)
	}

	// Poll schedule: schedule, ensure (never moves one), lease, claim.
	if ok, err := store.ScheduleUsagePoll(ctx, env.ORM, "missing", now, 0, "", now); err != nil || ok {
		t.Fatalf("ScheduleUsagePoll(absent) = %v, %v; want no account", ok, err)
	}
	if ok, err := store.ScheduleUsagePoll(ctx, env.ORM, a.ID, now.Add(-time.Minute), 3, "boom", now); err != nil || !ok {
		t.Fatalf("ScheduleUsagePoll = %v, %v", ok, err)
	}
	if err := store.EnsureUsagePollScheduled(ctx, env.ORM, a.ID, now.Add(time.Hour), []string{"claude"}, now); err != nil {
		t.Fatalf("EnsureUsagePollScheduled(already scheduled): %v", err)
	}
	if err := store.EnsureUsagePollScheduled(ctx, env.ORM, b.ID, now.Add(-2*time.Minute), []string{"codex"}, now); err != nil {
		t.Fatalf("EnsureUsagePollScheduled(other provider): %v", err)
	}
	l6RequireLimitsAgree(t, env, "u1", []string{a.ID, b.ID})
	got, _ = store.GetAccountLimitState(ctx, env.ORM, a.ID)
	if !got.UsageAPINextAt.Equal(now.Add(-time.Minute)) || got.UsageAPIFailures != 3 || got.UsageAPIError != "boom" {
		t.Fatalf("schedule = %+v; want the scheduled poll kept by Ensure", got)
	}
	if gotB, _ := store.GetAccountLimitState(ctx, env.ORM, b.ID); gotB.UsageAPINextAt != nil {
		t.Fatalf("Ensure for a provider not listed scheduled %v", gotB.UsageAPINextAt)
	}
	if err := env.Repo.EnsureUsagePollScheduled(ctx, b.ID, now.Add(-2*time.Minute), []string{"claude"}); err != nil {
		t.Fatalf("Repo.EnsureUsagePollScheduled: %v", err)
	}
	ids, err := store.LeaseUsagePolls(ctx, env.ORM, now, 1, time.Minute)
	if err != nil || len(ids) != 1 || ids[0] != b.ID {
		t.Fatalf("LeaseUsagePolls(limit 1) = %v, %v; want the earliest due (%s)", ids, err, b.ID)
	}
	twinIDs, err := env.Repo.LeaseUsagePolls(ctx, now, 10, time.Minute)
	if err != nil || len(twinIDs) != 1 || twinIDs[0] != a.ID {
		t.Fatalf("Repo.LeaseUsagePolls = %v, %v; want only the account the store did not lease", twinIDs, err)
	}
	if ids, err := store.LeaseUsagePolls(ctx, env.ORM, now, 10, time.Minute); err != nil || len(ids) != 0 {
		t.Fatalf("a second lease = %v, %v; want none due", ids, err)
	}
	if got, _ := store.GetAccountLimitState(ctx, env.ORM, b.ID); !got.UsageAPINextAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("a leased poll moves to now+lease, got %v", got.UsageAPINextAt)
	}
	if ok, err := store.ScheduleUsagePoll(ctx, env.ORM, b.ID, time.Time{}, 0, "", now); err != nil || !ok {
		t.Fatalf("ScheduleUsagePoll(unschedule) = %v, %v", ok, err)
	}
	if got, _ := env.Repo.GetAccountLimits(ctx, b.ID); got.UsageAPINextAt != nil || got.UsageAPIError != "" {
		t.Fatalf("an unscheduled poll reads %+v", got)
	}

	c := l6Account(t, env, l6Upsert("u1", "carol", "s3", "r3"))
	if ok, err := store.ClaimUsageFetch(ctx, env.ORM, c.ID, now, time.Minute); err != nil || !ok {
		t.Fatalf("ClaimUsageFetch(first, no limits row) = %v, %v", ok, err)
	}
	if ok, err := env.Repo.ClaimUsageFetch(ctx, c.ID, now.Add(30*time.Second), time.Minute); err != nil || ok {
		t.Fatalf("Repo.ClaimUsageFetch inside the floor = %v, %v; want refused", ok, err)
	}
	if ok, err := store.ClaimUsageFetch(ctx, env.ORM, c.ID, now.Add(time.Minute), time.Minute); err != nil || !ok {
		t.Fatalf("ClaimUsageFetch at the floor = %v, %v; want allowed", ok, err)
	}
	if ok, err := store.ClaimUsageFetch(ctx, env.ORM, "missing", now, time.Minute); err != nil || ok {
		t.Fatalf("ClaimUsageFetch(absent) = %v, %v", ok, err)
	}
	if got, _ := store.GetAccountLimitState(ctx, env.ORM, c.ID); !got.ObservedAt.IsZero() || got.Source != "usage_api" {
		t.Fatalf("a claim's row is never observed: %+v", got)
	}
	l6RequireLimitsAgree(t, env, "u1", []string{a.ID, b.ID, c.ID})
}

func l6RequireLimitsAgree(t *testing.T, env *storetest.Env, user string, ids []string) {
	t.Helper()
	ctx := context.Background()
	port, err := store.ListUserAccountLimits(ctx, env.ORM, user)
	if err != nil {
		t.Fatalf("ListUserAccountLimits: %v", err)
	}
	twin, err := env.Repo.ListAccountLimits(ctx, user)
	if err != nil {
		t.Fatalf("Repo.ListAccountLimits: %v", err)
	}
	requireSame(t, "ListUserAccountLimits", port, twin)
	for _, id := range ids {
		p, err := store.GetAccountLimitState(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("GetAccountLimitState: %v", err)
		}
		tw, err := env.Repo.GetAccountLimits(ctx, id)
		if err != nil {
			t.Fatalf("Repo.GetAccountLimits: %v", err)
		}
		requireSame(t, "GetAccountLimitState", p, tw)
	}
}

// TestThreadAssignmentQueries ports thread_provider_accounts and the serving-account read.
func TestThreadAssignmentQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	a := l6Account(t, env, l6Upsert("u1", "alice", "s1", "r1"))
	b := l6Account(t, env, l6Upsert("u1", "bob", "s2", "r2"))
	seedChatThread(t, env, "chat-l6", "th-1")
	if err := store.CreateThread(ctx, env.ORM, &store.Thread{ID: "th-2", ChatID: "chat-l6", Origin: "main", CreatedAt: now}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if got, err := store.GetThreadAssignment(ctx, env.ORM, "th-1", "claude"); err != nil || got != nil {
		t.Fatalf("GetThreadAssignment(none) = %v, %v", got, err)
	}
	first := core.Assignment{ThreadID: "th-1", Provider: "claude", AccountID: a.ID, Reason: "initial", AssignedAt: now}
	if ok, err := store.InsertThreadAssignment(ctx, env.ORM, first); err != nil || !ok {
		t.Fatalf("InsertThreadAssignment = %v, %v", ok, err)
	}
	lost := first
	lost.AccountID = b.ID
	if ok, err := store.InsertThreadAssignment(ctx, env.ORM, lost); err != nil || ok {
		t.Fatalf("a second first pick = %v, %v; want it to lose", ok, err)
	}
	if ok, err := env.Repo.SetThreadAssignment(ctx, lost, ""); err != nil || ok {
		t.Fatalf("Repo: a second first pick = %v, %v; want it to lose", ok, err)
	}
	port, err := store.GetThreadAssignment(ctx, env.ORM, "th-1", "claude")
	if err != nil {
		t.Fatalf("GetThreadAssignment: %v", err)
	}
	twin, err := env.Repo.GetThreadAssignment(ctx, "th-1", "claude")
	if err != nil {
		t.Fatalf("Repo.GetThreadAssignment: %v", err)
	}
	requireSame(t, "GetThreadAssignment", port, twin)
	if port.AccountID != a.ID || !port.AssignedAt.Equal(now) {
		t.Fatalf("assignment = %+v, want the first pick", port)
	}

	moved := core.Assignment{ThreadID: "th-1", Provider: "claude", AccountID: b.ID, Reason: "limited", AssignedAt: now.Add(time.Minute)}
	if ok, err := store.ReassignThread(ctx, env.ORM, moved, b.ID); err != nil || ok {
		t.Fatalf("a move from the wrong account = %v, %v; want refused", ok, err)
	}
	if ok, err := store.ReassignThread(ctx, env.ORM, moved, a.ID); err != nil || !ok {
		t.Fatalf("ReassignThread = %v, %v", ok, err)
	}
	if _, err := env.Repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "codex", AccountID: a.ID, Reason: "initial", AssignedAt: now.Add(2 * time.Minute)}, ""); err != nil {
		t.Fatalf("Repo.SetThreadAssignment: %v", err)
	}
	if _, err := env.Repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-2", Provider: "claude", AccountID: b.ID, Reason: "initial", AssignedAt: now.Add(-time.Hour)}, ""); err != nil {
		t.Fatalf("Repo.SetThreadAssignment: %v", err)
	}
	list, err := store.ListThreadAssignments(ctx, env.ORM, "th-1")
	if err != nil {
		t.Fatalf("ListThreadAssignments: %v", err)
	}
	twinList, err := env.Repo.ListThreadAssignments(ctx, "th-1")
	if err != nil {
		t.Fatalf("Repo.ListThreadAssignments: %v", err)
	}
	requireSame(t, "ListThreadAssignments", list, twinList)
	if len(list) != 2 || list[0].Provider != "codex" || list[1].AccountID != b.ID || list[1].Reason != "limited" {
		t.Fatalf("assignments = %+v, want codex (newest) then the moved claude", list)
	}

	counts, err := store.RecentAssignmentCounts(ctx, env.ORM, []string{a.ID, b.ID, "none"}, now)
	if err != nil {
		t.Fatalf("RecentAssignmentCounts: %v", err)
	}
	twinCounts, err := env.Repo.RecentAssignmentCounts(ctx, []string{a.ID, b.ID, "none"}, now)
	if err != nil {
		t.Fatalf("Repo.RecentAssignmentCounts: %v", err)
	}
	requireSame(t, "RecentAssignmentCounts", counts, twinCounts)
	if counts[a.ID] != 1 || counts[b.ID] != 1 || len(counts) != 2 {
		t.Fatalf("counts since now = %v, want one each (th-2's is older)", counts)
	}
	if empty, err := store.RecentAssignmentCounts(ctx, env.ORM, nil, now); err != nil || len(empty) != 0 {
		t.Fatalf("RecentAssignmentCounts(none) = %v, %v", empty, err)
	}

	// The serving account: the newest message that names one.
	if id, err := store.LatestServingProviderAccount(ctx, env.ORM, "th-1"); err != nil || id != "" {
		t.Fatalf("LatestServingProviderAccount(no messages) = %q, %v", id, err)
	}
	mustExec(t, env, `INSERT INTO context_windows (id, thread_id, sequence, created_at) VALUES ('cw-l6', 'th-1', 0, now())`)
	for i, acct := range []*string{&a.ID, &b.ID, nil} {
		if err := store.CreateMessage(ctx, env.ORM, &store.Message{
			ID: fmt.Sprintf("m-%d", i), ChatID: "chat-l6", Ordinal: int64(i), Seq: int64(i + 1), ThreadID: "th-1",
			ContextWindowID: "cw-l6", Role: 2, CreatedAt: now, UpdatedAt: now, ProviderAccountID: acct,
		}); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}
	id, err := store.LatestServingProviderAccount(ctx, env.ORM, "th-1")
	if err != nil {
		t.Fatalf("LatestServingProviderAccount: %v", err)
	}
	twinID, err := env.Repo.LatestServingAccount(ctx, "th-1")
	if err != nil {
		t.Fatalf("Repo.LatestServingAccount: %v", err)
	}
	if id != b.ID || twinID != id {
		t.Fatalf("serving account = %q (Repository %q), want %q: the newest message naming one", id, twinID, b.ID)
	}
}

// TestLegacyCredentialCopyQueries ports the copy's marker, its lock and its per-row savepoint.
func TestLegacyCredentialCopyQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	if done, err := store.LegacyCredentialCopyDone(ctx, env.ORM); err != nil || done {
		t.Fatalf("LegacyCredentialCopyDone(fresh) = %v, %v", done, err)
	}

	errRow := errors.New("row failed")
	l6InTx(t, env, func(ctx context.Context) error {
		if err := store.LockLegacyCredentialCopy(ctx, env.ORM); err != nil {
			return err
		}
		if l6TryInOtherSession(t, env, `SELECT pg_try_advisory_xact_lock(hashtext('provider_accounts_legacy_copy'))`) {
			t.Error("the copy's advisory lock is not the previous release's key")
		}
		rowErr, err := store.InLegacyImportSavepoint(ctx, env.ORM, func() error {
			if _, err := store.InsertProviderAccount(ctx, env.ORM, "rolled-back", "u1", "openai", 0, store.ProviderAccountCredential{
				AuthKind: core.AuthKindAPIKey, IdentityKey: "k", Secret: []byte("x"), UpdatedAt: time.Now()}, time.Now()); err != nil {
				return err
			}
			return errRow
		})
		if err != nil || !errors.Is(rowErr, errRow) {
			t.Errorf("InLegacyImportSavepoint(failing row) = %v, %v", rowErr, err)
		}
		// The failed row's write is gone, and a failed statement does not poison the rest.
		rowErr, err = store.InLegacyImportSavepoint(ctx, env.ORM, func() error {
			_, err := env.ORM.Exec(ctx, `SELECT 1/0`)
			return err
		})
		if err != nil || rowErr == nil {
			t.Errorf("InLegacyImportSavepoint(failing statement) = %v, %v", rowErr, err)
		}
		rowErr, err = store.InLegacyImportSavepoint(ctx, env.ORM, func() error {
			_, err := store.InsertProviderAccount(ctx, env.ORM, "kept", "u1", "openai", 0, store.ProviderAccountCredential{
				AuthKind: core.AuthKindAPIKey, IdentityKey: "k", Secret: []byte("x"), UpdatedAt: time.Now()}, time.Now())
			return err
		})
		if err != nil || rowErr != nil {
			t.Errorf("InLegacyImportSavepoint(row) = %v, %v", rowErr, err)
		}
		return store.RecordLegacyCredentialCopy(ctx, env.ORM, time.Now(), 1, 0, 2)
	})
	meta, err := env.Repo.ListProviderAccounts(ctx, "u1")
	if err != nil || len(meta) != 1 || meta[0].ID != "kept" {
		t.Fatalf("after the copy the Repository reads %v, %v; want only the kept row", meta, err)
	}
	if done, err := store.LegacyCredentialCopyDone(ctx, env.ORM); err != nil || !done {
		t.Fatalf("LegacyCredentialCopyDone = %v, %v", done, err)
	}
	var created, skipped, failed int
	if err := env.SQL.QueryRow(`SELECT created, skipped, failed FROM legacy_credential_copy WHERE id = 1`).Scan(&created, &skipped, &failed); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if created != 1 || skipped != 0 || failed != 2 {
		t.Fatalf("marker = %d/%d/%d", created, skipped, failed)
	}
	// The Repository's copy sees the store's marker and does nothing.
	if _, ran, err := env.Repo.MigrateLegacyCredentials(ctx); err != nil || ran {
		t.Fatalf("Repo.MigrateLegacyCredentials after the marker = ran %v, %v", ran, err)
	}
	if err := store.RecordLegacyCredentialCopy(ctx, env.ORM, time.Now(), 0, 0, 0); err == nil {
		t.Fatal("a second marker was written")
	}
}
