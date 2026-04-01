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
