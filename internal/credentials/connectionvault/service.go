// Copyright (c) 2025 Reliant Labs

// Package connectionvault is the only path to the connections tables and their
// connection_secrets ciphertext: a person's or an org's saved logins to external services, their
// audit events and the OAuth flows that mint them.
//
// The package stores and returns ciphertext only; it never opens a value. internal/connections
// seals a connection's fields under the owner's vault tenant before it stores them and opens them
// after it reads them back, so what leaves here is what a caller already received. Every read and
// write takes the owner (a person or an org, core.ConnectionOwner) it is for, and enforces it in
// SQL, so a connection of another user or org is not found. Only the *Secrets methods return
// ciphertext.
//
// Built by hand, by internal/app's NewConnectionVault provider (providers.go): never a node
// compose.go constructs from Deps.
//
//forge:provided
package connectionvault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Deps holds dependencies for the connectionvault package.
type Deps struct {
	// DB is the forge ORM handle. A call made with a ctx carrying a transaction joins it.
	DB orm.Context
}

//forge:contract
type service struct {
	deps Deps
}

// New creates the connection store.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.DB == nil {
		return nil, errors.New("connectionvault: Deps.DB is required")
	}
	return &service{deps: deps}, nil
}

// CreateConnection inserts the row, its secrets and its created event in
// one transaction. The first connection a user makes for an integration
// becomes its default.
func (s *service) CreateConnection(ctx context.Context, c *core.Connection, secrets []core.ConnectionSecret, ev core.ConnectionEvent) error {
	return store.InsertUserConnection(ctx, s.deps.DB, c, secrets, ev)
}

// ReauthorizeConnection rewrites secrets and identity on an existing row
// and returns it to active.
func (s *service) ReauthorizeConnection(ctx context.Context, userID, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error) {
	return store.ReauthorizeConnection(ctx, s.deps.DB, userID, id, upd, secrets, ev)
}

func (s *service) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return store.GetUserConnection(ctx, s.deps.DB, userID, id)
}

// GetOwnedConnection returns owner's live connection.
func (s *service) GetOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error) {
	return store.GetOwnedConnection(ctx, s.deps.DB, owner, id)
}

// ListOwnedConnections returns owner's live connections.
func (s *service) ListOwnedConnections(ctx context.Context, owner core.ConnectionOwner, integrationID string) ([]*core.Connection, error) {
	return store.ListOwnedConnections(ctx, s.deps.DB, owner, integrationID)
}

func (s *service) ListConnections(ctx context.Context, userID, integrationID string) ([]*core.Connection, error) {
	return store.ListUserConnections(ctx, s.deps.DB, userID, integrationID)
}

func (s *service) DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error) {
	return store.DefaultUserConnection(ctx, s.deps.DB, userID, integrationID)
}

func (s *service) FindByExternalAccount(ctx context.Context, userID, integrationID, externalAccountID string) (*core.Connection, error) {
	return store.FindConnectionByExternalAccount(ctx, s.deps.DB, userID, integrationID, externalAccountID)
}

// FindOwnedByExternalAccount returns owner's oldest live connection to that
// provider account.
func (s *service) FindOwnedByExternalAccount(ctx context.Context, owner core.ConnectionOwner, integrationID, externalAccountID string) (*core.Connection, error) {
	return store.FindOwnedConnectionByExternalAccount(ctx, s.deps.DB, owner, integrationID, externalAccountID)
}

// ReauthorizeOwnedConnection is ReauthorizeConnection for owner's connection.
func (s *service) ReauthorizeOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error) {
	return store.ReauthorizeOwnedConnection(ctx, s.deps.DB, owner, id, upd, secrets, ev)
}

// RenameOwnedConnection renames owner's connection.
func (s *service) RenameOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id, name string, ev core.ConnectionEvent) error {
	return store.RenameOwnedConnection(ctx, s.deps.DB, owner, id, name, ev)
}

// SetDefaultOwnedConnection makes owner's connection its integration's default.
func (s *service) SetDefaultOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string, ev core.ConnectionEvent) error {
	return store.SetDefaultOwnedConnection(ctx, s.deps.DB, owner, id, ev)
}

// SoftDeleteOwnedConnection soft-deletes owner's connection and destroys its
// ciphertext.
func (s *service) SoftDeleteOwnedConnection(ctx context.Context, owner core.ConnectionOwner, id string, ev core.ConnectionEvent) error {
	return store.SoftDeleteOwnedConnection(ctx, s.deps.DB, owner, id, ev)
}

// RecordOwnedTestResult stores what a successful probe of owner's connection read.
func (s *service) RecordOwnedTestResult(ctx context.Context, owner core.ConnectionOwner, id, accountLabel, senderID string) error {
	return store.RecordOwnedConnectionTestResult(ctx, s.deps.DB, owner, id, accountLabel, senderID)
}

// ListAllConnectionEvents pages every user's events of one connection.
func (s *service) ListAllConnectionEvents(ctx context.Context, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	return store.ListAllConnectionEvents(ctx, s.deps.DB, id, limit, beforeID)
}

func (s *service) RenameConnection(ctx context.Context, userID, id, name string, ev core.ConnectionEvent) error {
	return store.RenameConnection(ctx, s.deps.DB, userID, id, name, ev)
}

func (s *service) SetDefaultConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error {
	return store.SetDefaultConnection(ctx, s.deps.DB, userID, id, ev)
}

// DeleteConnection soft-deletes the row and destroys its ciphertext.
func (s *service) DeleteConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error {
	return store.SoftDeleteConnection(ctx, s.deps.DB, userID, id, ev)
}

// RecordTestResult stores what a successful probe read; an empty value
// keeps the stored one.
func (s *service) RecordTestResult(ctx context.Context, userID, id, accountLabel, senderID string) error {
	return store.RecordConnectionTestResult(ctx, s.deps.DB, userID, id, accountLabel, senderID)
}

func (s *service) TouchConnectionUsed(ctx context.Context, userID, id string) error {
	return store.TouchConnectionUsed(ctx, s.deps.DB, userID, id)
}

// TouchOwnedConnectionUsed stamps the last use of owner's connection.
func (s *service) TouchOwnedConnectionUsed(ctx context.Context, owner core.ConnectionOwner, id string) error {
	return store.TouchOwnedConnectionUsed(ctx, s.deps.DB, owner, id)
}

// GetSecrets returns the sealed fields of a personal connection the user owns.
func (s *service) GetSecrets(ctx context.Context, userID, id string) (map[string]core.ConnectionSecret, error) {
	return store.GetConnectionSecrets(ctx, s.deps.DB, userID, id)
}

// GetOwnedSecrets returns the sealed fields of owner's connection.
func (s *service) GetOwnedSecrets(ctx context.Context, owner core.ConnectionOwner, id string) (map[string]core.ConnectionSecret, error) {
	return store.GetOwnedConnectionSecrets(ctx, s.deps.DB, owner, id)
}

// WithSecretsLock runs fn with the user's personal connection's secret rows
// locked (SELECT ... FOR UPDATE) inside one transaction, serializing
// refreshes across worker processes.
func (s *service) WithSecretsLock(ctx context.Context, userID, id string, fn func(*SecretsTx) error) error {
	return s.WithOwnedSecretsLock(ctx, core.UserConnectionOwner(userID), id, fn)
}

// WithOwnedSecretsLock is WithSecretsLock for owner's connection.
func (s *service) WithOwnedSecretsLock(ctx context.Context, owner core.ConnectionOwner, id string, fn func(*SecretsTx) error) error {
	return store.WithOwnedConnectionSecretsLock(ctx, s.deps.DB, owner, id,
		func(txCtx context.Context, conn *core.Connection, secrets map[string]core.ConnectionSecret) error {
			return fn(&SecretsTx{txCtx: txCtx, db: s.deps.DB, conn: conn, secrets: secrets})
		})
}

// SecretsTx is the locked view of one connection's secrets that
// WithSecretsLock hands its callback. Its statements run in the lock's
// transaction, which txCtx carries. The ctx each method takes is the
// callback's own, which does not carry it — run there, a write would wait on
// the very row the lock holds — so it is not used; the transaction is bounded
// by the ctx it was opened with.
type SecretsTx struct {
	txCtx   context.Context //nolint:containedctx // the lock's transaction, scoped to the callback
	db      orm.Context
	conn    *core.Connection
	secrets map[string]core.ConnectionSecret
}

func (t *SecretsTx) Connection() *core.Connection              { return t.conn }
func (t *SecretsTx) Secrets() map[string]core.ConnectionSecret { return t.secrets }

// Replace rewrites the given fields and bumps their generation, but only if
// the access_token row is still at expectedGeneration.
func (t *SecretsTx) Replace(_ context.Context, expectedGeneration int64, secrets []core.ConnectionSecret, accessExpiresAt *time.Time) error {
	cur, ok := t.secrets[core.SecretFieldAccessToken]
	if !ok || cur.Generation != expectedGeneration {
		return core.ErrGenerationConflict
	}
	return store.ReplaceConnectionSecrets(t.txCtx, t.db, t.conn.ID, secrets, accessExpiresAt)
}

func (t *SecretsTx) MarkStatus(_ context.Context, status, reason string) error {
	return store.MarkConnectionStatus(t.txCtx, t.db, t.conn.ID, status, reason)
}

func (t *SecretsTx) AppendEvent(_ context.Context, ev core.ConnectionEvent) error {
	ev.ConnectionID = t.conn.ID
	return store.AppendConnectionEvent(t.txCtx, t.db, ev)
}

func (s *service) CreateOAuthFlow(ctx context.Context, f *core.OAuthFlow) error {
	return store.InsertOAuthFlow(ctx, s.deps.DB, f)
}

// PeekOAuthFlow returns an unexpired, unconsumed flow without consuming
// it; anything else is ErrOAuthFlowInvalid.
func (s *service) PeekOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	return store.PeekOAuthFlow(ctx, s.deps.DB, stateHash, now)
}

// ConsumeOAuthFlow atomically marks an unexpired, unconsumed flow consumed
// and returns it; anything else is ErrOAuthFlowInvalid.
func (s *service) ConsumeOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error) {
	return store.ConsumeOAuthFlow(ctx, s.deps.DB, stateHash, now)
}

func (s *service) AppendConnectionEvent(ctx context.Context, ev core.ConnectionEvent) error {
	return store.AppendConnectionEvent(ctx, s.deps.DB, ev)
}

func (s *service) ListConnectionEvents(ctx context.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	return store.ListConnectionEvents(ctx, s.deps.DB, userID, id, limit, beforeID)
}

var errEmptyUserID = errors.New("user id cannot be empty")

// PurgeUser deletes the person's OAuth flows, audit events and personal connections (their secrets
// cascade) and returns how many rows went, for the account purge. An org's connections, whose
// user_id is only the admin who made them, are the org's and stay. It joins the transaction ctx
// carries, so the purge stays all-or-nothing.
func (s *service) PurgeUser(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return 0, errEmptyUserID
	}
	var total int64
	for _, step := range []struct {
		what string
		del  func(context.Context, orm.Context, string) (int64, error)
	}{
		{"oauth flows", store.DeleteUserOAuthFlows},
		{"connection events", store.DeleteUserConnectionEvents},
		{"connections", store.DeleteUserConnections},
	} {
		n, err := step.del(ctx, s.deps.DB, userID)
		if err != nil {
			return 0, fmt.Errorf("delete the user's %s: %w", step.what, err)
		}
		total += n
	}
	return total, nil
}

// UserHasCredentials reports whether the person has a live personal connection.
func (s *service) UserHasCredentials(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, errEmptyUserID
	}
	return store.UserHasConnection(ctx, s.deps.DB, userID)
}
