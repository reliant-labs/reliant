// Copyright (c) 2025 Reliant Labs

package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
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
	ErrConnectionNotFound = svcerr.NotFound("connection")
	// ErrConnectionNameTaken: the user already has a connection with that name
	// for the integration.
	ErrConnectionNameTaken = svcerr.AlreadyExists("connection name already in use")
	// ErrOAuthFlowInvalid: unknown, expired, or already-consumed OAuth state.
	ErrOAuthFlowInvalid = svcerr.FailedPrecondition("oauth flow is invalid, expired or already used")
	// ErrGenerationConflict: a secret was rewritten since it was read.
	ErrGenerationConflict = svcerr.Aborted("connection secret changed concurrently")
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
	// SenderID is the provider's id for the person who made the connection,
	// as that provider's events name them in trigger.sender.id (a Slack user
	// id, a GitHub user id, a Gmail address). Nil when the integration does
	// not say. Unlike ExternalAccountID it never routes anything.
	SenderID    *string
	Scopes      []string
	OAuthClient *string
	AuthHeader  *string
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
	Params map[string]string
	// ReturnTo is the absolute URL the provider's redirect is relayed to, so
	// the client that started the flow can finish it.
	ReturnTo  string
	ExpiresAt time.Time
}

// ConnectionUpdate is what a successful (re)authorization writes onto an
// existing connection row.
type ConnectionUpdate struct {
	AccountLabel      string
	ExternalAccountID string
	// SenderID replaces the stored one; empty clears it, because a new
	// authorization may have been made by a different person.
	SenderID        string
	Scopes          []string
	OAuthClient     string
	AccessExpiresAt *time.Time
}
