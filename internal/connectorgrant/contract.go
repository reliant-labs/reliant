// Copyright (c) 2025 Reliant Labs
//
// Package connectorgrant owns connector grants: the stored consent that lets a
// third-party MCP client run tools in a user's cloud workspace.
//
// A grant answers four questions — which daemon, which tools, which paths, and
// whether shell access is included — and it is the only thing standing between
// an outside model and a user's workspace. Everything here therefore fails
// closed: a grant that is incomplete, expired, or revoked resolves to no
// access rather than to default access.
//
// This package stores and resolves decisions. Enforcement happens at the
// daemon's command dispatch; see internal/daemonpolicy.
package connectorgrant

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// Sentinel errors. Callers map these to transport-level codes; the
// distinction between "no such credential" and "revoked" is deliberately NOT
// surfaced to the caller, since both are authentication failures and telling
// them apart helps only an attacker.
var (
	// ErrNotFound means no live grant matched the credential.
	ErrNotFound = errors.New("connector grant not found")

	// ErrRevoked means the grant existed but has been revoked.
	ErrRevoked = errors.New("connector grant revoked")

	// ErrExpired means the grant existed but is past its expiry.
	ErrExpired = errors.New("connector grant expired")
)

// ExecMode mirrors daemonpolicy.ExecMode at the storage boundary. The DB
// spells "deny" explicitly, while the policy's zero value is the empty string;
// keeping a separate type here means that mismatch is converted in exactly one
// place rather than assumed to line up.
type ExecMode string

// ExecMode values, matching the connector_grants CHECK constraint.
const (
	ExecDeny         ExecMode = "deny"
	ExecAllowlist    ExecMode = "allowlist"
	ExecUnrestricted ExecMode = "unrestricted"
)

// Grant is one connector's stored access.
type Grant struct {
	ID       string
	UserID   string
	DaemonID string
	Name     string

	// TokenHash is the SHA-256 hex digest of the credential. The plaintext is
	// shown once at creation and never stored.
	TokenHash   string
	TokenPrefix string

	// AllowedTools holds MCP tool names, the vocabulary the user consented to.
	// The mapping to daemon commands happens in internal/mcpserver.
	AllowedTools []string

	PathRoot      string
	ExecMode      ExecMode
	ExecAllowlist []string

	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// IsLive reports whether the grant may currently be used.
func (g *Grant) IsLive(now time.Time) error {
	if g == nil {
		return ErrNotFound
	}
	if g.RevokedAt != nil {
		return ErrRevoked
	}
	if g.ExpiresAt != nil && now.After(*g.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// Audit lifecycle states. See the 20260802020000 migration for why an audit
// row exists before its outcome is known.
const (
	// AuditStarted marks a call dispatched to the daemon whose outcome is not
	// yet recorded. A row left in this state means the server died mid-call —
	// the command may or may not have run.
	AuditStarted = "started"

	// AuditCompleted marks a call that reached the daemon and returned,
	// successfully or with an error.
	AuditCompleted = "completed"

	// AuditDenied marks a call refused by policy. It never reached the daemon.
	AuditDenied = "denied"
)

// AuditRecord is one connector tool invocation, including refused ones.
type AuditRecord struct {
	ID          string
	GrantID     string
	UserID      string
	DaemonID    string
	ToolName    string
	CommandType string
	Arguments   []byte
	Denied      bool
	ErrorMsg    string
	DurationMS  int
	CreatedAt   time.Time

	// Status is one of AuditStarted / AuditCompleted / AuditDenied.
	Status string
}

// Store persists grants and audit records.
type Store interface {
	CreateGrant(ctx context.Context, g *Grant) error

	// GetGrantByTokenHash resolves a credential. It returns live grants only:
	// revoked and expired rows come back as errors rather than as grants the
	// caller must remember to re-check.
	GetGrantByTokenHash(ctx context.Context, tokenHash string) (*Grant, error)

	ListGrantsByUser(ctx context.Context, userID string) ([]*Grant, error)
	GetGrant(ctx context.Context, userID, id string) (*Grant, error)

	// GetGrantByID returns a LIVE grant by id, without a user scope.
	//
	// It exists for the MCP server's per-call re-resolution, where the caller
	// has already been authenticated by credential and the grant id came from
	// that authentication — there is no user to scope by that would add
	// anything. Unlike GetGrant it filters out revoked and expired rows, so a
	// grant narrowed or revoked mid-session stops working on the next call.
	GetGrantByID(ctx context.Context, id string) (*Grant, error)

	// RevokeGrant is scoped by user so one user can never revoke another's
	// grant by guessing an id.
	RevokeGrant(ctx context.Context, userID, id string) (bool, error)

	// TouchGrant records use for the "last used" column.
	TouchGrant(ctx context.Context, id string) error

	// RecordAudit writes an audit row. Used for the intent row (status
	// AuditStarted) and for terminal outcomes that never dispatched.
	// Client bindings: which connector an OAuth client acts through. See
	// bindings.go for why re-consenting replaces rather than accumulates.
	GetBinding(ctx context.Context, userID, clientID string) (*ClientBinding, error)
	PutBinding(ctx context.Context, b *ClientBinding) error
	DeleteBinding(ctx context.Context, userID, clientID string) (bool, error)
	ListBindingsByUser(ctx context.Context, userID string) ([]*ClientBinding, error)

	RecordAudit(ctx context.Context, rec *AuditRecord) error

	// CompleteAudit resolves an intent row with its outcome. A row that is
	// never completed stays visible as AuditStarted, which is the point.
	CompleteAudit(ctx context.Context, id, status, errMsg string, durationMS int) error
	ListAuditByUser(ctx context.Context, userID string, limit int) ([]*AuditRecord, error)
	ListAuditByGrant(ctx context.Context, userID, grantID string, limit int) ([]*AuditRecord, error)
}

// ToPolicy converts a grant into the enforceable policy sent to the daemon.
//
// commandsForTools translates the grant's MCP tool names into daemon command
// types. It is injected rather than imported so this package does not depend
// on the MCP catalog, keeping the dependency pointing one way.
func (g *Grant) ToPolicy(commandsForTools func([]string) []string) *daemonpolicy.Policy {
	if g == nil {
		return nil
	}

	p := &daemonpolicy.Policy{
		GrantID:  g.ID,
		Tools:    toSet(commandsForTools(g.AllowedTools)),
		PathRoot: g.PathRoot,
	}

	switch g.ExecMode {
	case ExecUnrestricted:
		p.ExecMode = daemonpolicy.ExecUnrestricted
	case ExecAllowlist:
		p.ExecMode = daemonpolicy.ExecAllowlist
		p.ExecAllowlist = toSet(g.ExecAllowlist)
	default:
		// Includes ExecDeny and any value that somehow escaped the CHECK
		// constraint. Denying is the safe reading of an unrecognized mode.
		p.ExecMode = daemonpolicy.ExecDenied
	}

	if g.ExpiresAt != nil {
		p.ExpiresAt = *g.ExpiresAt
	}

	return p
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}
