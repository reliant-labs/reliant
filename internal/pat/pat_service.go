package pat

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// Service manages Personal Access Token lifecycle.
type Service struct {
	repo db.Repository
}

// NewService creates a new PAT service.
func NewService(repo db.Repository) *Service {
	return &Service{repo: repo}
}

// CreatePAT generates a new PAT for a user. Returns the raw token (show once).
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

	now := time.Now().UTC()
	p = &db.DaemonPAT{
		ID:          uuid.New().String(),
		UserID:      userID,
		TokenHash:   hash,
		TokenPrefix: prefix,
		Name:        name,
		Ephemeral:   ephemeral,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}

	if err := s.repo.CreateDaemonPAT(ctx, p); err != nil {
		return "", nil, fmt.Errorf("failed to create PAT: %w", err)
	}

	return raw, p, nil
}

// CreatePATForDaemon generates a new PAT bound to an authoritative daemonID.
// It follows the same SHA-256-hash / store-hash / show-raw-once flow as
// CreatePAT, but additionally stamps DaemonPAT.DaemonID so the resulting token
// authenticates as a specific managed daemon. The PAT-bound daemon ID is later
// surfaced by patauth.ValidatePAT and injected into request context, which lets
// the gateway skip its hostname/UUID guessing (resolveUnboundDaemonID) for
// managed daemons.
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

	now := time.Now().UTC()
	p := &db.DaemonPAT{
		ID:          uuid.New().String(),
		UserID:      userID,
		DaemonID:    daemonID,
		TokenHash:   hash,
		TokenPrefix: prefix,
		Name:        name,
		Ephemeral:   false,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}

	if err := s.repo.CreateDaemonPAT(ctx, p); err != nil {
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
	return s.repo.RevokeDaemonPATsByDaemonID(ctx, daemonID)
}

// RevokePAT revokes a specific PAT by ID.
func (s *Service) RevokePAT(ctx context.Context, id string) error {
	return s.repo.RevokeDaemonPAT(ctx, id)
}

// RevokeEphemeralPATs revokes all ephemeral PATs for a user (called on shutdown).
func (s *Service) RevokeEphemeralPATs(ctx context.Context, userID string) error {
	return s.repo.RevokeDaemonPATsByUserID(ctx, userID, true)
}

// ListPATs returns all PATs for a user.
func (s *Service) ListPATs(ctx context.Context, userID string) ([]*db.DaemonPAT, error) {
	return s.repo.ListDaemonPATsByUserID(ctx, userID)
}
