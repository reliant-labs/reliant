// Copyright (c) 2025 Reliant Labs

package core

import (
	"context"
	"errors"
	"time"
)

// Connection kinds and states (research/CONNECTIONS_VAULT.md §4.1).
const (
	ConnectionOwnerUser = "user"
	ConnectionOwnerOrg  = "org"

	ConnectionAuthOAuth2 = "oauth2"
	ConnectionAuthAPIKey = "api_key"
	ConnectionAuthBasic  = "basic"
	ConnectionAuthNone   = "none"

	ConnectionStatusActive      = "active"
	ConnectionStatusNeedsReauth = "needs_reauth"
	ConnectionStatusRevoked     = "revoked"

	SecretFieldAccessToken  = "access_token"
	SecretFieldRefreshToken = "refresh_token"
	SecretFieldAPIKey       = "api_key"
	SecretFieldPassword     = "password"
	SecretFieldClientSecret = "client_secret"

	ConnectionEventCreated        = "created"
	ConnectionEventUsed           = "used"
	ConnectionEventRefreshed      = "refreshed"
	ConnectionEventRefreshFailed  = "refresh_failed"
	ConnectionEventNeedsReauth    = "needs_reauth"
	ConnectionEventRevoked        = "revoked"
	ConnectionEventDeleted        = "deleted"
	ConnectionEventDefaultChanged = "default_changed"
	ConnectionEventReconnected    = "reconnected"
	ConnectionEventRenamed        = "renamed"
)

var (
	// ErrConnectionNotFound covers a missing, deleted, or someone else's
	// connection alike: ids must not be an oracle.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrConnectionNameTaken: the user already has a connection with that name
	// for the integration.
	ErrConnectionNameTaken = errors.New("connection name already in use")
	// ErrOAuthFlowInvalid: unknown, expired, or already-consumed OAuth state.
	ErrOAuthFlowInvalid = errors.New("oauth flow is invalid, expired or already used")
	// ErrGenerationConflict: a secret was rewritten since it was read.
	ErrGenerationConflict = errors.New("connection secret changed concurrently")
)

// Connection is a user's saved, authorized login to an external service.
// It never carries ciphertext: that lives in ConnectionSecret.
type Connection struct {
	ID                string
	OwnerKind         string
	UserID            string
	OrgID             *string
	IntegrationID     string
	AuthKind          string
	Name              string
	AccountLabel      *string
	ExternalAccountID *string
	Scopes            []string
	OAuthClient       *string
	AuthHeader        *string
	// Params are the connection's non-secret settings, declared by the
	// integration's connection_params. Never a secret.
	Params          map[string]string
	Status          string
	StatusReason    *string
	IsDefault       bool
	AccessExpiresAt *time.Time
	LastUsedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// ConnectionSecret is one sealed field of a connection.
type ConnectionSecret struct {
	ConnectionID string
	Field        string
	VaultKeyID   string
	Ciphertext   []byte
	Generation   int64
	UpdatedAt    time.Time
}

// ConnectionEvent is one append-only audit row.
type ConnectionEvent struct {
	ID           int64
	ConnectionID string
	UserID       string
	Kind         string
	RunID        string
	NodeID       string
	ToolCallID   string
	Actor        string
	At           time.Time
}

// OAuthFlow is single-use, session-bound state for one authorization.
type OAuthFlow struct {
	StateHash             []byte
	UserID                string
	SessionIDHash         []byte
	IntegrationID         string
	PKCEVerifierSealed    []byte
	RedirectAfter         string
	ReconnectConnectionID string
	ConnectionName        string
	// Params are the connection params the user supplied when starting a new
	// connection; a reconnect keeps the connection's own.
	Params    map[string]string
	ExpiresAt time.Time
}

// ConnectionUpdate is what a successful (re)authorization writes onto an
// existing connection row.
type ConnectionUpdate struct {
	AccountLabel      string
	ExternalAccountID string
	Scopes            []string
	OAuthClient       string
	AccessExpiresAt   *time.Time
}

// ConnectionStore persists connections. Every method that takes a userID
// enforces ownership in SQL; none returns ciphertext except the *Secrets
// methods.
type ConnectionStore interface {
	// CreateConnection inserts the row, its secrets and its created event in
	// one transaction. The first connection a user makes for an integration
	// becomes its default.
	CreateConnection(ctx context.Context, c *Connection, secrets []ConnectionSecret, ev ConnectionEvent) error
	// ReauthorizeConnection rewrites secrets and identity on an existing row
	// and returns it to active.
	ReauthorizeConnection(ctx context.Context, userID, id string, upd ConnectionUpdate, secrets []ConnectionSecret, ev ConnectionEvent) (*Connection, error)

	GetConnection(ctx context.Context, userID, id string) (*Connection, error)
	ListConnections(ctx context.Context, userID, integrationID string) ([]*Connection, error)
	DefaultConnection(ctx context.Context, userID, integrationID string) (*Connection, error)
	FindByExternalAccount(ctx context.Context, userID, integrationID, externalAccountID string) (*Connection, error)

	RenameConnection(ctx context.Context, userID, id, name string, ev ConnectionEvent) error
	SetDefaultConnection(ctx context.Context, userID, id string, ev ConnectionEvent) error
	// DeleteConnection soft-deletes the row and destroys its ciphertext.
	DeleteConnection(ctx context.Context, userID, id string, ev ConnectionEvent) error
	RecordTestResult(ctx context.Context, userID, id, accountLabel string) error
	TouchConnectionUsed(ctx context.Context, userID, id string) error

	// GetSecrets returns the sealed fields of a connection the user owns.
	GetSecrets(ctx context.Context, userID, id string) (map[string]ConnectionSecret, error)
	// WithSecretsLock runs fn with the connection's secret rows locked
	// (SELECT ... FOR UPDATE) inside one transaction, serializing refreshes
	// across worker processes.
	WithSecretsLock(ctx context.Context, userID, id string, fn func(SecretsTx) error) error

	CreateOAuthFlow(ctx context.Context, f *OAuthFlow) error
	// ConsumeOAuthFlow atomically marks an unexpired, unconsumed flow consumed
	// and returns it; anything else is ErrOAuthFlowInvalid.
	ConsumeOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*OAuthFlow, error)

	AppendConnectionEvent(ctx context.Context, ev ConnectionEvent) error
	ListConnectionEvents(ctx context.Context, userID, id string, limit int, beforeID int64) ([]ConnectionEvent, error)
}

// SecretsTx is the locked view of one connection's secrets.
type SecretsTx interface {
	Connection() *Connection
	Secrets() map[string]ConnectionSecret
	// Replace rewrites the given fields and bumps their generation, but only
	// if the access_token row is still at expectedGeneration.
	Replace(ctx context.Context, expectedGeneration int64, secrets []ConnectionSecret, accessExpiresAt *time.Time) error
	MarkStatus(ctx context.Context, status, reason string) error
	AppendEvent(ctx context.Context, ev ConnectionEvent) error
}
