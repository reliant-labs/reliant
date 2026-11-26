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

	"github.com/lib/pq"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Tests for the accounts lane's credential tables (P5b L6, part 2): vault_keys, api_keys,
// access_tokens and provider_backoff. Each
// ported statement is checked against the previous release's hand SQL (the twin), run
// verbatim on the same rows: the store writes and the previous statement reads, or the reverse.

// TestVaultKeyQueries ports internal/vault's statements.
func TestVaultKeyQueries(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()

	if _, found, err := store.GetTenantVaultKey(ctx, env.ORM, "user", "alice", "primary"); err != nil || found {
		t.Fatalf("GetTenantVaultKey(none) found=%v, %v", found, err)
	}
	key := store.WrappedVaultKey{ID: "vk_1", TenantKind: "user", TenantID: "alice", State: "primary", WrappedDEK: []byte("wrapped-1")}
	if err := store.InsertVaultKeyIfAbsent(ctx, env.ORM, key, "v1"); err != nil {
		t.Fatalf("InsertVaultKeyIfAbsent: %v", err)
	}
	// A second first use of the tenant converges on the first key, as before.
	if err := store.InsertVaultKeyIfAbsent(ctx, env.ORM, store.WrappedVaultKey{ID: "vk_2", TenantKind: "user", TenantID: "alice",
		State: "primary", WrappedDEK: []byte("wrapped-2")}, "v1"); err != nil {
		t.Fatalf("InsertVaultKeyIfAbsent(second): %v", err)
	}
	var (
		id      string
		wrapped []byte
		version int
		kek     string
	)
	if err := env.SQL.QueryRow(`SELECT id, wrapped_dek FROM vault_keys WHERE tenant_kind = $1 AND tenant_id = $2 AND state = $3`,
		"user", "alice", "primary").Scan(&id, &wrapped); err != nil {
		t.Fatalf("previous release's primary-key read: %v", err)
	}
	got, found, err := store.GetTenantVaultKey(ctx, env.ORM, "user", "alice", "primary")
	if err != nil || !found || got.ID != id || !bytes.Equal(got.WrappedDEK, wrapped) || id != "vk_1" {
		t.Fatalf("GetTenantVaultKey = %+v, %v, %v; the previous release reads %s", got, found, err, id)
	}
	if err := env.SQL.QueryRow(`SELECT version, kek_id FROM vault_keys WHERE id = 'vk_1'`).Scan(&version, &kek); err != nil || version != 1 || kek != "v1" {
		t.Fatalf("stored key version=%d kek=%q, %v; want 1, v1", version, kek, err)
	}

	// By id, as Open resolves the key a ciphertext names.
	mustExec(t, env, `INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state) VALUES ('vk_old', 'user', 'bob', 1, 'v1', 'x', 'destroyed')`)
	byID, found, err := store.GetWrappedVaultKey(ctx, env.ORM, "vk_old")
	if err != nil || !found || byID.TenantID != "bob" || byID.State != "destroyed" || string(byID.WrappedDEK) != "x" {
		t.Fatalf("GetWrappedVaultKey = %+v, %v, %v", byID, found, err)
	}
	if _, found, err := store.GetWrappedVaultKey(ctx, env.ORM, "vk_none"); err != nil || found {
		t.Fatalf("GetWrappedVaultKey(none) found=%v, %v", found, err)
	}
}

// TestSealedCredentialsExist ports vault boot's "would a new key orphan anything" probe.
func TestSealedCredentialsExist(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	previous := func() bool {
		var exists bool
		if err := env.SQL.QueryRow(`SELECT EXISTS (SELECT 1 FROM vault_keys) OR EXISTS (SELECT 1 FROM api_keys WHERE api_key_sealed IS NOT NULL)`).Scan(&exists); err != nil {
			t.Fatalf("previous release's probe: %v", err)
		}
		return exists
	}
	check := func(want bool) {
		t.Helper()
		got, err := store.SealedCredentialsExist(ctx, env.ORM)
		if err != nil || got != want || previous() != want {
			t.Fatalf("SealedCredentialsExist = %v, %v (previous release %v); want %v", got, err, previous(), want)
		}
	}
	// storetest's vault has sealed nothing yet.
	check(false)
	mustExec(t, env, `INSERT INTO api_keys (id, user_id, provider, api_key_sealed) VALUES ('k1', 'u1', 'reliant-automation:d1', 'x')`)
	check(true)
	mustExec(t, env, `DELETE FROM api_keys`)
	mustExec(t, env, `INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state) VALUES ('vk', 'user', 'u', 1, 'v1', 'x', 'primary')`)
	check(true)
}

// TestAPIKeyQueries ports settingStore's api_keys statements.
func TestAPIKeyQueries(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	provider := core.AutomationProviderPrefix + "d1"

	if _, err := store.GetAPIKeySealed(ctx, env.ORM, "u1", provider); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetAPIKeySealed(none) err = %v, want sql.ErrNoRows (automationcred branches on it)", err)
	}
	if err := store.UpsertSealedAPIKey(ctx, env.ORM, "k1", "u1", provider, []byte("sealed-1"), now); err != nil {
		t.Fatalf("UpsertSealedAPIKey: %v", err)
	}
	sealed, err := store.GetAPIKeySealed(ctx, env.ORM, "u1", provider)
	if err != nil || string(sealed) != "sealed-1" {
		t.Fatalf("GetAPIKeySealed = %q, %v; want the upsert's write", sealed, err)
	}
	if err := store.UpsertSealedAPIKey(ctx, env.ORM, "k3", "u1", provider, []byte("sealed-3"), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpsertSealedAPIKey(again): %v", err)
	}
	var (
		id           string
		value        []byte
		created, upd time.Time
	)
	if err := env.SQL.QueryRow(`SELECT id, api_key_sealed, created_at, updated_at FROM api_keys WHERE user_id = $1 AND provider = $2`,
		"u1", provider).Scan(&id, &value, &created, &upd); err != nil {
		t.Fatalf("read api key: %v", err)
	}
	if id != "k1" || string(value) != "sealed-3" || !created.Equal(now) || !upd.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("after two upserts the row is %s %q %v %v; want k1 kept in place, sealed-3, created kept, updated stamped", id, value, created, upd)
	}

	if err := store.DeleteAPIKey(ctx, env.ORM, "u1", provider); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	if err := store.DeleteAPIKey(ctx, env.ORM, "u1", provider); err != nil {
		t.Fatalf("DeleteAPIKey(absent): %v", err)
	}
}

// TestAccessTokenQueries ports the self-hosted token store's statements.
func TestAccessTokenQueries(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	past, future := time.Now().Add(-time.Hour).UTC(), time.Now().Add(time.Hour).UTC()

	insert := func(id, name, hash string, scopes []string, expires *time.Time, kind string) {
		t.Helper()
		tok := store.NewAccessToken{ID: id, OrgID: "u1", Name: name, TokenHash: hash, TokenPrefix: "rlat_" + id,
			Scopes: scopes, ActingUserID: "u1", ExpiresAt: expires}
		if kind != "" {
			tok.ResourceKind, tok.ResourceID = kind, "res-1"
		}
		if err := store.InsertAccessToken(ctx, env.ORM, tok); err != nil {
			t.Fatalf("InsertAccessToken(%s): %v", id, err)
		}
	}
	insert("t-live", "laptop", "h-live", []string{"daemon:connect", "reliant:api"}, &future, "daemon")
	insert("t-expired", "laptop", "h-expired", []string{"daemon:connect"}, &future, "")
	mustExec(t, env, `UPDATE access_tokens SET created_at = $1, expires_at = $2 WHERE id = 't-expired'`, past.Add(-time.Hour), past)
	insert("t-forever", "ci", "h-forever", []string{"reliant:api"}, nil, "")
	// The previous release's insert, alongside.
	if _, err := env.SQL.Exec(`
		INSERT INTO access_tokens (id, org_id, name, token_hash, token_prefix, scopes,
			created_by_user_id, acting_user_id, resource_kind, resource_id, ephemeral, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9, $10, $11)`,
		"t-eph", "u1", "eph", "h-eph", "rlat_e", pq.Array([]string{"daemon:connect"}), "u1", "daemon", "res-eph", true, future); err != nil {
		t.Fatalf("previous release's insert: %v", err)
	}

	// The hash lookup: live only, expiry on the database clock.
	rec, found, err := store.FindLiveAccessToken(ctx, env.ORM, "h-live")
	if err != nil || !found || rec.ID != "t-live" || rec.TokenHash != "h-live" || rec.ActingUserID != "u1" || rec.OrgID != "u1" ||
		rec.ResourceKind != "daemon" || rec.ResourceID != "res-1" || rec.ExpiresAt == nil || fmt.Sprint(rec.Scopes) != "[daemon:connect reliant:api]" {
		t.Fatalf("FindLiveAccessToken = %+v, %v, %v", rec, found, err)
	}
	var createdBy string
	if err := env.SQL.QueryRow(`SELECT created_by_user_id FROM access_tokens WHERE id = 't-live'`).Scan(&createdBy); err != nil || createdBy != "u1" {
		t.Fatalf("created_by_user_id = %q, %v; want the acting user", createdBy, err)
	}
	for _, hash := range []string{"h-expired", "h-none"} {
		if _, found, err := store.FindLiveAccessToken(ctx, env.ORM, hash); err != nil || found {
			t.Fatalf("FindLiveAccessToken(%s) found=%v, %v; want inactive", hash, found, err)
		}
	}
	if rec, found, _ := store.FindLiveAccessToken(ctx, env.ORM, "h-eph"); !found || !rec.Ephemeral || rec.ResourceID != "res-eph" {
		t.Fatalf("the previous release's row reads %+v", rec)
	}

	// Last use: stamped once, then throttled for a minute.
	if err := store.TouchAccessToken(ctx, env.ORM, "t-live"); err != nil {
		t.Fatalf("TouchAccessToken: %v", err)
	}
	mustExec(t, env, `UPDATE access_tokens SET last_used_at = now() - interval '30 seconds' WHERE id = 't-live'`)
	var before, after time.Time
	_ = env.SQL.QueryRow(`SELECT last_used_at FROM access_tokens WHERE id = 't-live'`).Scan(&before)
	_ = store.TouchAccessToken(ctx, env.ORM, "t-live")
	_ = env.SQL.QueryRow(`SELECT last_used_at FROM access_tokens WHERE id = 't-live'`).Scan(&after)
	if !after.Equal(before) {
		t.Fatalf("a touch within a minute moved last_used_at from %v to %v", before, after)
	}

	// Listing matches the previous release's query, with and without a scope.
	for _, scope := range []string{"", "reliant:api"} {
		got, err := store.ListUserAccessTokens(ctx, env.ORM, "u1", scope)
		if err != nil {
			t.Fatalf("ListUserAccessTokens(%q): %v", scope, err)
		}
		q := `SELECT id FROM access_tokens WHERE acting_user_id = $1 AND revoked_at IS NULL`
		args := []any{"u1"}
		if scope != "" {
			q += ` AND $2 = ANY(scopes)`
			args = append(args, scope)
		}
		rows, err := env.SQL.Query(q+` ORDER BY created_at DESC`, args...)
		if err != nil {
			t.Fatalf("previous release's list: %v", err)
		}
		var twin []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			twin = append(twin, id)
		}
		_ = rows.Close()
		ids := make([]string, len(got))
		for i, r := range got {
			ids[i] = r.ID
		}
		requireSame(t, "ListUserAccessTokens("+scope+")", ids, twin)
		if scope == "" && len(ids) != 4 || scope != "" && len(ids) != 2 {
			t.Fatalf("ListUserAccessTokens(%q) = %v", scope, ids)
		}
		if got[0].TokenHash != "" {
			t.Fatal("a listing must not read the token hash")
		}
	}

	// Rotation: revoke by name and exact scope set, under the user's lock; count the live ones.
	l6InTx(t, env, func(ctx context.Context) error {
		if err := store.LockUserAccessTokens(ctx, env.ORM, "u1"); err != nil {
			return err
		}
		if l6TryInOtherSession(t, env, `SELECT pg_try_advisory_xact_lock(hashtext('accesstoken:user:' || $1))`, "u1") {
			t.Error("the rotation lock is not the previous release's key")
		}
		if n, err := store.RevokeUserAccessTokensNamed(ctx, env.ORM, "u1", "laptop", []string{"daemon:connect"}); err != nil || n != 1 {
			t.Errorf("RevokeUserAccessTokensNamed(exact set) = %d, %v; want only t-expired", n, err)
		}
		live, err := store.CountLiveUserAccessTokens(ctx, env.ORM, "u1", []string{"daemon:connect"})
		if err != nil || live != 1 {
			t.Errorf("CountLiveUserAccessTokens = %d, %v; want t-eph only", live, err)
		}
		return nil
	})

	// Update: rename and re-scope, the user scoped in; a revoked or foreign token is not found.
	name := "work laptop"
	upd, found, err := store.UpdateUserAccessToken(ctx, env.ORM, "u1", "t-live", &name, []string{"reliant:api"})
	if err != nil || !found || upd.Name != name || fmt.Sprint(upd.Scopes) != "[reliant:api]" || upd.ResourceKind != "daemon" {
		t.Fatalf("UpdateUserAccessToken = %+v, %v, %v", upd, found, err)
	}
	if again, found, _ := store.FindLiveAccessToken(ctx, env.ORM, "h-live"); !found || fmt.Sprint(again.Scopes) != "[reliant:api]" {
		t.Fatalf("the re-scoped token must keep its hash and authenticate with its new scopes: %+v", again)
	}
	if _, found, err := store.UpdateUserAccessToken(ctx, env.ORM, "u2", "t-live", &name, nil); err != nil || found {
		t.Fatalf("updating another user's token found=%v, %v", found, err)
	}
	if _, found, err := store.UpdateUserAccessToken(ctx, env.ORM, "u1", "t-expired", nil, []string{"llm:invoke"}); err != nil || found {
		t.Fatalf("updating a revoked token found=%v, %v", found, err)
	}

	// Revocations.
	if n, err := store.RevokeUserAccessToken(ctx, env.ORM, "u2", "t-forever"); err != nil || n != 0 {
		t.Fatalf("revoking another user's token = %d, %v", n, err)
	}
	if ok, err := store.UserAccessTokenExists(ctx, env.ORM, "u2", "t-forever"); err != nil || ok {
		t.Fatalf("UserAccessTokenExists(another user) = %v, %v", ok, err)
	}
	if n, err := store.RevokeUserAccessToken(ctx, env.ORM, "u1", "t-forever"); err != nil || n != 1 {
		t.Fatalf("RevokeUserAccessToken = %d, %v", n, err)
	}
	if n, err := store.RevokeUserAccessToken(ctx, env.ORM, "u1", "t-forever"); err != nil || n != 0 {
		t.Fatalf("revoking a revoked token = %d, %v", n, err)
	}
	if ok, err := store.UserAccessTokenExists(ctx, env.ORM, "u1", "t-forever"); err != nil || !ok {
		t.Fatalf("UserAccessTokenExists(revoked) = %v, %v; want true", ok, err)
	}
	if n, err := store.RevokeResourceAccessTokens(ctx, env.ORM, "daemon", "res-1"); err != nil || n != 1 {
		t.Fatalf("RevokeResourceAccessTokens = %d, %v", n, err)
	}
	if n, err := store.RevokeEphemeralAccessTokens(ctx, env.ORM, "u1"); err != nil || n != 1 {
		t.Fatalf("RevokeEphemeralAccessTokens = %d, %v", n, err)
	}
	var live int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM access_tokens WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("live tokens left = %d, %v", live, err)
	}
	var revokedFuture int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM access_tokens WHERE revoked_at > now()`).Scan(&revokedFuture); err != nil || revokedFuture != 0 {
		t.Fatalf("revoked_at must be the database clock: %d rows in the future, %v", revokedFuture, err)
	}
}

// TestProviderBackoffQueries runs the same backoff sequence through the store and, on another
// thread, through the previous release's statements, and requires identical rows.
func TestProviderBackoffQueries(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-bo", "th-port")
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if err := store.CreateThread(ctx, env.ORM, &store.Thread{ID: "th-twin", ChatID: "chat-bo", Origin: "main", CreatedAt: t0}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	const prevRecord = `
INSERT INTO provider_backoff (
    thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
    status_code, reason, retries, waited_ms, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 0, $9)
ON CONFLICT (thread_id) DO UPDATE SET
    chat_id = EXCLUDED.chat_id,
    waiting_since = EXCLUDED.waiting_since,
    resume_at = EXCLUDED.resume_at,
    attempt = EXCLUDED.attempt,
    max_attempts = EXCLUDED.max_attempts,
    status_code = EXCLUDED.status_code,
    reason = EXCLUDED.reason,
    provider = '',
    retries = provider_backoff.retries + 1,
    waited_ms = provider_backoff.waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(provider_backoff.resume_at, EXCLUDED.waiting_since), EXCLUDED.waiting_since)
        - provider_backoff.waiting_since)) * 1000)::BIGINT),
    updated_at = EXCLUDED.updated_at`
	const prevClear = `
UPDATE provider_backoff SET
    waited_ms = waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(resume_at, $1), $2) - waiting_since)) * 1000)::BIGINT),
    waiting_since = NULL,
    resume_at = NULL,
    attempt = 0,
    updated_at = $3
WHERE thread_id = $4 AND waiting_since IS NOT NULL`
	const prevUsageWait = `
INSERT INTO provider_backoff (
    thread_id, chat_id, waiting_since, resume_at, attempt, max_attempts,
    status_code, reason, provider, retries, waited_ms, updated_at
) VALUES ($1, $2, $3, $4, 1, 0, 429, $5, $6, 1, 0, $7)
ON CONFLICT (thread_id) DO UPDATE SET
    chat_id = EXCLUDED.chat_id,
    waiting_since = EXCLUDED.waiting_since,
    resume_at = EXCLUDED.resume_at,
    attempt = provider_backoff.attempt + 1,
    max_attempts = 0,
    status_code = 429,
    reason = EXCLUDED.reason,
    provider = EXCLUDED.provider,
    retries = provider_backoff.retries + 1,
    waited_ms = provider_backoff.waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(provider_backoff.resume_at, EXCLUDED.waiting_since), EXCLUDED.waiting_since)
        - provider_backoff.waiting_since)) * 1000)::BIGINT),
    updated_at = EXCLUDED.updated_at`
	const prevClearUsage = `
UPDATE provider_backoff SET
    waited_ms = waited_ms + GREATEST(0, (EXTRACT(EPOCH FROM (
        LEAST(COALESCE(resume_at, $1), $2) - waiting_since)) * 1000)::BIGINT),
    waiting_since = NULL,
    resume_at = NULL,
    attempt = 0,
    updated_at = $3
WHERE chat_id = $4 AND waiting_since IS NOT NULL
  AND reason IN ('usage_limit', 'usage_limit_probe') AND thread_id = $5`

	step := func(what string, port func() error, twin string, args ...any) {
		t.Helper()
		if err := port(); err != nil {
			t.Fatalf("%s (store): %v", what, err)
		}
		mustExec(t, env, twin, args...)
	}
	// A retry ladder: wait 2s, re-wait after 1s (only 1s charged), clear after 3s more.
	t1, t2, t3, t4 := t0.Add(time.Second), t0.Add(4*time.Second), t0.Add(time.Hour), t0.Add(2*time.Hour)
	step("record", func() error {
		return store.RecordProviderBackoff(ctx, env.ORM, "chat-bo", "th-port", 1, 5, 429, "http_429", 2*time.Second, t0)
	}, prevRecord, "th-twin", "chat-bo", t0, t0.Add(2*time.Second), 1, 5, 429, "http_429", t0)
	step("re-record", func() error {
		return store.RecordProviderBackoff(ctx, env.ORM, "chat-bo", "th-port", 2, 5, 503, "http_503", 10*time.Second, t1)
	}, prevRecord, "th-twin", "chat-bo", t1, t1.Add(10*time.Second), 2, 5, 503, "http_503", t1)
	step("clear", func() error { return store.ClearProviderBackoff(ctx, env.ORM, "th-port", t2) },
		prevClear, t2, t2, t2, "th-twin")
	step("clear again (no-op)", func() error { return store.ClearProviderBackoff(ctx, env.ORM, "th-port", t3) },
		prevClear, t3, t3, t3, "th-twin")
	step("usage wait", func() error {
		return store.RecordUsageLimitWait(ctx, env.ORM, "chat-bo", "th-port", "Anthropic", core.ReasonUsageLimit, t4, t3)
	}, prevUsageWait, "th-twin", "chat-bo", t3, t4, core.ReasonUsageLimit, "Anthropic", t3)
	n, err := store.ClearUsageLimitWait(ctx, env.ORM, "chat-bo", "th-port", t3.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("ClearUsageLimitWait(one thread) = %d, %v", n, err)
	}
	mustExec(t, env, prevClearUsage, t3.Add(time.Minute), t3.Add(time.Minute), t3.Add(time.Minute), "chat-bo", "th-twin")

	all, err := store.ListChatProviderBackoff(ctx, env.ORM, "chat-bo")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListChatProviderBackoff = %v, %v", all, err)
	}
	port, twin := all["th-port"], all["th-twin"]
	twin.ThreadID = port.ThreadID
	requireSame(t, "provider backoff after the same sequence", port, twin)
	if port.WaitedMs != 1000+3000+60000 || port.Retries != 3 || port.Provider != "Anthropic" || port.Waiting() || port.Attempt != 0 {
		t.Fatalf("backoff = %+v; want 64s waited over 3 retries, released", port)
	}
	viaRepo, err := store.ListChatProviderBackoff(ctx, env.ORM, "chat-bo")
	if err != nil {
		t.Fatalf("Repo.ProviderBackoffByChat: %v", err)
	}
	requireSame(t, "Repo.ProviderBackoffByChat", viaRepo, all)

	// A chat-wide release (no thread) frees every usage-limit wait, and only those.
	step("usage wait 2", func() error {
		return store.RecordUsageLimitWait(ctx, env.ORM, "chat-bo", "th-port", "Anthropic", core.ReasonUsageLimitProbe, t4.Add(time.Hour), t4)
	}, prevRecord, "th-twin", "chat-bo", t4, t4.Add(time.Second), 1, 5, 429, "http_429", t4)
	if n, err := store.ClearUsageLimitWait(ctx, env.ORM, "chat-bo", "", t4.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("ClearUsageLimitWait(chat) = %d, %v; want only the usage-limit wait released", n, err)
	}
	all, _ = store.ListChatProviderBackoff(ctx, env.ORM, "chat-bo")
	if all["th-port"].Waiting() || !all["th-twin"].Waiting() {
		t.Fatalf("after a chat-wide usage release: %+v", all)
	}
}
