// Package pat is the single implementation of Personal Access Token
// minting, listing, revocation, and validation. One token format (rlnt_pat_,
// see internal/auth/pat.go) and one table (daemon_pats) back every PAT; a
// kind column discriminates what a token may authenticate:
//
//   - kind='daemon' — daemon <-> gateway streams (validated via
//     internal/patauth, which delegates here and accepts daemon-kind only)
//   - kind='api'    — regular user API requests through the same
//     interceptor/middleware path as JWTs (ValidateAPIToken accepts api-kind
//     only)
//
// Kind separation is strict and enforced at validation time: a daemon token
// never authenticates user APIs and an api token never authenticates a
// daemon stream. Wrong-kind tokens fail with the same error as unknown
// tokens so the two cases are indistinguishable to a caller.
package pat

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// lastUsedThrottle is how fresh last_used_at may be before we skip stamping
// it again. Spec allows throttled updates; this avoids a write per request.
const lastUsedThrottle = 60 * time.Second

// maxNameLen bounds token names.
const maxNameLen = 128

// ErrTokenNotFound is returned by the owner-scoped revoke paths when no live
// token matches the (user, id) pair — either it never existed, belongs to
// another user, or is already revoked. Deliberately indistinguishable to the
// caller.
var ErrTokenNotFound = errors.New("no active token with that ID")

// Store is the narrow persistence surface the service needs.
// db.Repository satisfies it.
type Store interface {
	CreateDaemonPAT(ctx context.Context, pat *db.DaemonPAT) error
	GetDaemonPATByTokenHash(ctx context.Context, tokenHash string) (*db.DaemonPAT, error)
	ListDaemonPATsByUserIDAndKind(ctx context.Context, userID, kind string) ([]*db.DaemonPAT, error)
	RevokeDaemonPATByUserID(ctx context.Context, userID, id, kind string) (bool, error)
	RevokeDaemonPATsByUserID(ctx context.Context, userID string, ephemeralOnly bool) error
	RevokeDaemonPATsByDaemonID(ctx context.Context, daemonID string) (int, error)
	UpdateDaemonPATLastUsed(ctx context.Context, id string) error
}

// Service manages Personal Access Token lifecycle and validation for both
// kinds.
type Service struct {
	store Store

	// now is injectable for expiry/throttle tests.
	now func() time.Time
	// syncLastUsed makes the last_used_at stamp synchronous (tests only).
	syncLastUsed bool
}

// NewService creates a new PAT service.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// ---------------------------------------------------------------------------
// Daemon-kind lifecycle (daemon <-> gateway stream credentials)
// ---------------------------------------------------------------------------

// CreatePAT generates a new daemon-kind PAT for a user. Returns the raw token
// (show once).
func (s *Service) CreatePAT(ctx context.Context, userID, name string, ephemeral bool, expiresAt *time.Time) (rawToken string, p *db.DaemonPAT, err error) {
	if userID == "" {
		return "", nil, fmt.Errorf("user ID is required")
	}
	if name == "" {
		return "", nil, fmt.Errorf("name is required")
	}

	raw, hash, prefix, err := auth.GeneratePAT()
	if err != nil {
		return "", nil, err
	}

	now := s.now().UTC()
	p = &db.DaemonPAT{
		ID:          uuid.New().String(),
		UserID:      userID,
		Kind:        db.DaemonPATKindDaemon,
		TokenHash:   hash,
		TokenPrefix: prefix,
		Name:        name,
		Ephemeral:   ephemeral,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}

	if err := s.store.CreateDaemonPAT(ctx, p); err != nil {
		return "", nil, fmt.Errorf("failed to create PAT: %w", err)
	}

	return raw, p, nil
}

// CreatePATForDaemon generates a new daemon-kind PAT bound to an
// authoritative daemonID. It follows the same SHA-256-hash / store-hash /
// show-raw-once flow as CreatePAT, but additionally stamps DaemonPAT.DaemonID
// so the resulting token authenticates as a specific managed daemon. The
// PAT-bound daemon ID is later surfaced by patauth.ValidatePAT and injected
// into request context, which lets the gateway skip its hostname/UUID
// guessing (resolveUnboundDaemonID) for managed daemons.
//
// Managed-daemon PATs are non-ephemeral and (by default) non-expiring: the
// control-plane operator owns their lifecycle explicitly via
// RevokeManagedDaemonToken, not via desktop-session shutdown. Callers may still
// pass a non-nil expiresAt to bound the token.
func (s *Service) CreatePATForDaemon(ctx context.Context, userID, daemonID, name string, expiresAt *time.Time) (rawToken string, patID string, err error) {
	if userID == "" {
		return "", "", fmt.Errorf("user ID is required")
	}
	if daemonID == "" {
		return "", "", fmt.Errorf("daemon ID is required")
	}
	if name == "" {
		return "", "", fmt.Errorf("name is required")
	}

	raw, hash, prefix, err := auth.GeneratePAT()
	if err != nil {
		return "", "", err
	}

	now := s.now().UTC()
	p := &db.DaemonPAT{
		ID:          uuid.New().String(),
		UserID:      userID,
		DaemonID:    daemonID,
		Kind:        db.DaemonPATKindDaemon,
		TokenHash:   hash,
		TokenPrefix: prefix,
		Name:        name,
		Ephemeral:   false,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}

	if err := s.store.CreateDaemonPAT(ctx, p); err != nil {
		return "", "", fmt.Errorf("failed to create managed daemon PAT: %w", err)
	}

	return raw, p.ID, nil
}

// RevokeManagedDaemonPATs revokes every live PAT bound to daemonID and returns
// the number revoked. Used by the managed-daemon teardown / re-provision path.
func (s *Service) RevokeManagedDaemonPATs(ctx context.Context, daemonID string) (int, error) {
	if daemonID == "" {
		return 0, fmt.Errorf("daemon ID is required")
	}
	return s.store.RevokeDaemonPATsByDaemonID(ctx, daemonID)
}

// RevokePAT revokes the user's daemon-kind PAT by ID. Owner- and kind-scoped:
// a caller can never revoke another user's token, and api-kind tokens are not
// reachable through this surface (use RevokeAPIToken). Returns
// ErrTokenNotFound when no live daemon-kind token matches.
func (s *Service) RevokePAT(ctx context.Context, userID, id string) error {
	if userID == "" {
		return fmt.Errorf("user ID is required")
	}
	if id == "" {
		return fmt.Errorf("token ID is required")
	}
	revoked, err := s.store.RevokeDaemonPATByUserID(ctx, userID, id, db.DaemonPATKindDaemon)
	if err != nil {
		return err
	}
	if !revoked {
		return fmt.Errorf("%w: %s", ErrTokenNotFound, id)
	}
	return nil
}

// RevokeEphemeralPATs revokes all ephemeral PATs for a user (called on shutdown).
func (s *Service) RevokeEphemeralPATs(ctx context.Context, userID string) error {
	return s.store.RevokeDaemonPATsByUserID(ctx, userID, true)
}

// ListPATs returns all daemon-kind PATs for a user (api-kind tokens are
// listed via ListAPITokens).
func (s *Service) ListPATs(ctx context.Context, userID string) ([]*db.DaemonPAT, error) {
	return s.store.ListDaemonPATsByUserIDAndKind(ctx, userID, db.DaemonPATKindDaemon)
}

// ValidateDaemonPAT resolves a raw bearer to its PAT record, accepting
// daemon-kind tokens ONLY. Unknown, malformed, revoked, expired, and api-kind
// tokens are all rejected. This is the single validation path behind
// internal/patauth (the gateway-side auth.PATValidator).
func (s *Service) ValidateDaemonPAT(ctx context.Context, rawToken string) (*db.DaemonPAT, error) {
	return s.validateToken(ctx, rawToken, db.DaemonPATKindDaemon)
}

// ---------------------------------------------------------------------------
// API-kind lifecycle (user API credentials, JWT-equivalent claims)
// ---------------------------------------------------------------------------

// CreateAPIToken mints a new api-kind PAT for the user. Returns the raw token
// — it is shown exactly once and never retrievable again. ttl == 0 means no
// expiry.
func (s *Service) CreateAPIToken(ctx context.Context, userID, userEmail, name string, ttl time.Duration) (rawToken string, p *db.DaemonPAT, err error) {
	if userID == "" {
		return "", nil, fmt.Errorf("user ID is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, fmt.Errorf("name is required")
	}
	if len(name) > maxNameLen {
		return "", nil, fmt.Errorf("name must be at most %d characters", maxNameLen)
	}
	if ttl < 0 {
		return "", nil, fmt.Errorf("ttl must not be negative")
	}

	// Reject duplicate active names so `auth token revoke <name>` stays
	// unambiguous. Enforced at the service level (no DB constraint) — a
	// concurrent-create race producing a duplicate degrades to revoke-by-id.
	existing, err := s.store.ListDaemonPATsByUserIDAndKind(ctx, userID, db.DaemonPATKindAPI)
	if err != nil {
		return "", nil, fmt.Errorf("failed to check existing tokens: %w", err)
	}
	for _, t := range existing {
		if t.Name == name && t.RevokedAt == nil {
			return "", nil, fmt.Errorf("an active token named %q already exists — revoke it first or pick another name", name)
		}
	}

	raw, hash, prefix, err := auth.GeneratePAT()
	if err != nil {
		return "", nil, err
	}

	now := s.now().UTC()
	p = &db.DaemonPAT{
		ID:          uuid.New().String(),
		UserID:      userID,
		UserEmail:   userEmail,
		Kind:        db.DaemonPATKindAPI,
		TokenHash:   hash,
		TokenPrefix: prefix,
		Name:        name,
		Ephemeral:   false,
		CreatedAt:   now,
	}
	if ttl > 0 {
		exp := now.Add(ttl)
		p.ExpiresAt = &exp
	}

	if err := s.store.CreateDaemonPAT(ctx, p); err != nil {
		return "", nil, fmt.Errorf("failed to create api token: %w", err)
	}
	return raw, p, nil
}

// ListAPITokens returns all api-kind tokens owned by the user (hashes are
// included in the db rows; callers rendering responses must never expose
// them).
func (s *Service) ListAPITokens(ctx context.Context, userID string) ([]*db.DaemonPAT, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID is required")
	}
	return s.store.ListDaemonPATsByUserIDAndKind(ctx, userID, db.DaemonPATKindAPI)
}

// RevokeAPIToken marks the user's api-kind token revoked. Returns
// ErrTokenNotFound when the token does not exist, belongs to someone else, is
// not api-kind, or is already revoked.
func (s *Service) RevokeAPIToken(ctx context.Context, userID, id string) error {
	if userID == "" {
		return fmt.Errorf("user ID is required")
	}
	if id == "" {
		return fmt.Errorf("token ID is required")
	}
	revoked, err := s.store.RevokeDaemonPATByUserID(ctx, userID, id, db.DaemonPATKindAPI)
	if err != nil {
		return err
	}
	if !revoked {
		return fmt.Errorf("%w: %s", ErrTokenNotFound, id)
	}
	return nil
}

// ValidateAPIToken implements auth.APITokenValidator: it resolves a raw
// rlnt_pat_ bearer to the same claims object JWT validation produces,
// accepting api-kind tokens ONLY. Unknown, malformed, revoked, expired, and
// daemon-kind tokens are all rejected, and last_used_at is stamped
// (throttled).
func (s *Service) ValidateAPIToken(ctx context.Context, rawToken string) (*auth.JWTClaims, error) {
	p, err := s.validateToken(ctx, rawToken, db.DaemonPATKindAPI)
	if err != nil {
		return nil, err
	}
	return &auth.JWTClaims{
		Sub:   p.UserID,
		Email: p.UserEmail,
		Role:  "authenticated",
	}, nil
}

// ---------------------------------------------------------------------------
// Shared validation core
// ---------------------------------------------------------------------------

// validateToken is the single validation path for every PAT kind: format
// check, SHA-256 hash lookup, constant-time hash re-compare, strict kind
// gate, revoked/expired rejection, throttled last_used_at stamp.
func (s *Service) validateToken(ctx context.Context, rawToken, kind string) (*db.DaemonPAT, error) {
	if !auth.IsPATFormat(rawToken) {
		return nil, auth.ErrInvalidToken
	}

	hash := auth.HashPAT(rawToken)
	p, err := s.store.GetDaemonPATByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, auth.ErrInvalidToken
		}
		return nil, fmt.Errorf("token lookup failed: %w", err)
	}
	if p == nil {
		return nil, auth.ErrInvalidToken
	}
	// The DB lookup already matched on the hash; re-compare constant-time as
	// defense in depth (e.g. against a collation-lenient index match).
	if subtle.ConstantTimeCompare([]byte(p.TokenHash), []byte(hash)) != 1 {
		return nil, auth.ErrInvalidToken
	}
	// Strict kind separation. A wrong-kind token fails exactly like an
	// unknown one so the cases are indistinguishable to a caller.
	if p.Kind != kind {
		return nil, auth.ErrInvalidToken
	}
	// The DB lookup already filters revoked/expired rows; re-check here as
	// defense in depth (and for stores that return rows regardless of state).
	if p.RevokedAt != nil {
		return nil, fmt.Errorf("token has been revoked")
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(s.now()) {
		return nil, fmt.Errorf("token has expired")
	}

	s.stampLastUsed(p)
	return p, nil
}

// stampLastUsed updates last_used_at, throttled to once per lastUsedThrottle
// and (by default) asynchronously so auth never blocks on the write.
func (s *Service) stampLastUsed(p *db.DaemonPAT) {
	if p.LastUsedAt != nil && s.now().Sub(*p.LastUsedAt) < lastUsedThrottle {
		return
	}
	update := func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.UpdateDaemonPATLastUsed(bgCtx, p.ID); err != nil {
			logging.Warn("Failed to update PAT last_used_at", "patID", p.ID, "error", err)
		}
	}
	if s.syncLastUsed {
		update()
		return
	}
	go update()
}
