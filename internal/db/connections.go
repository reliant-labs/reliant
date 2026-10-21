// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Connections returns the store for user connections (the credential vault's
// metadata and ciphertext tables, their audit trail, and OAuth flow state).
func (r *Repo) Connections() core.ConnectionStore {
	return connectionStore{db: r.ORM()}
}

// connectionStore is core.ConnectionStore over the connection queries in
// internal/store. A call made with a ctx carrying a transaction joins it.
type connectionStore struct{ db orm.Context }

func (s connectionStore) CreateConnection(ctx context.Context, c *core.Connection, secrets []core.ConnectionSecret, ev core.ConnectionEvent) error {
	return store.InsertUserConnection(ctx, s.db, c, secrets, ev)
}

func (s connectionStore) ReauthorizeConnection(ctx context.Context, userID, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error) {
	return store.ReauthorizeConnection(ctx, s.db, userID, id, upd, secrets, ev)
}

func (s connectionStore) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return store.GetUserConnection(ctx, s.db, userID, id)
}

func (s connectionStore) ListConnections(ctx context.Context, userID, integrationID string) ([]*core.Connection, error) {
	return store.ListUserConnections(ctx, s.db, userID, integrationID)
}

func (s connectionStore) DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error) {
	return store.DefaultUserConnection(ctx, s.db, userID, integrationID)
}

func (s connectionStore) FindByExternalAccount(ctx context.Context, userID, integrationID, externalAccountID string) (*core.Connection, error) {
	return store.FindConnectionByExternalAccount(ctx, s.db, userID, integrationID, externalAccountID)
}

func (s connectionStore) RenameConnection(ctx context.Context, userID, id, name string, ev core.ConnectionEvent) error {
	return store.RenameConnection(ctx, s.db, userID, id, name, ev)
}

func (s connectionStore) SetDefaultConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error {
	return store.SetDefaultConnection(ctx, s.db, userID, id, ev)
}

func (s connectionStore) DeleteConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error {
	return store.SoftDeleteConnection(ctx, s.db, userID, id, ev)
}

func (s connectionStore) RecordTestResult(ctx context.Context, userID, id, accountLabel, senderID string) error {
	return store.RecordConnectionTestResult(ctx, s.db, userID, id, accountLabel, senderID)
}

func (s connectionStore) TouchConnectionUsed(ctx context.Context, userID, id string) error {
	return store.TouchConnectionUsed(ctx, s.db, userID, id)
}

func (s connectionStore) GetSecrets(ctx context.Context, userID, id string) (map[string]core.ConnectionSecret, error) {
	return store.GetConnectionSecrets(ctx, s.db, userID, id)
}

func (s connectionStore) WithSecretsLock(ctx context.Context, userID, id string, fn func(core.SecretsTx) error) error {
	return store.WithConnectionSecretsLock(ctx, s.db, userID, id,
		func(txCtx context.Context, conn *core.Connection, secrets map[string]core.ConnectionSecret) error {
			return fn(&secretsTx{txCtx: txCtx, db: s.db, conn: conn, secrets: secrets})
		})
}

// secretsTx is the locked view WithSecretsLock hands its callback. Its
// statements run in the lock's transaction, which txCtx carries. The ctx each
// method takes is the callback's own, which does not carry it — run there, a
// write would wait on the very row the lock holds — so it is not used; the
// transaction is bounded by the ctx it was opened with.
type secretsTx struct {
	txCtx   context.Context //nolint:containedctx // the lock's transaction, scoped to the callback
	db      orm.Context
	conn    *core.Connection
	secrets map[string]core.ConnectionSecret
}

func (t *secretsTx) Connection() *core.Connection              { return t.conn }
func (t *secretsTx) Secrets() map[string]core.ConnectionSecret { return t.secrets }

// Replace rewrites the given fields and bumps their generation, but only if
// the access_token row is still at expectedGeneration.
func (t *secretsTx) Replace(_ context.Context, expectedGeneration int64, secrets []core.ConnectionSecret, accessExpiresAt *time.Time) error {
	cur, ok := t.secrets[core.SecretFieldAccessToken]
	if !ok || cur.Generation != expectedGeneration {
		return core.ErrGenerationConflict
	}
	return store.ReplaceConnectionSecrets(t.txCtx, t.db, t.conn.ID, secrets, accessExpiresAt)
}

func (t *secretsTx) MarkStatus(_ context.Context, status, reason string) error {
	return store.MarkConnectionStatus(t.txCtx, t.db, t.conn.ID, status, reason)
}

func (t *secretsTx) AppendEvent(_ context.Context, ev core.ConnectionEvent) error {
	ev.ConnectionID = t.conn.ID
	return store.AppendConnectionEvent(t.txCtx, t.db, ev)
}

func (s connectionStore) CreateOAuthFlow(ctx context.Context, f *core.OAuthFlow) error {
	return store.InsertOAuthFlow(ctx, s.db, f)
}

func (s connectionStore) PeekOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	return store.PeekOAuthFlow(ctx, s.db, stateHash, now)
}

func (s connectionStore) ConsumeOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	return store.ConsumeOAuthFlow(ctx, s.db, stateHash, now)
}

func (s connectionStore) AppendConnectionEvent(ctx context.Context, ev core.ConnectionEvent) error {
	return store.AppendConnectionEvent(ctx, s.db, ev)
}

func (s connectionStore) ListConnectionEvents(ctx context.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	return store.ListConnectionEvents(ctx, s.db, userID, id, limit, beforeID)
}
