// Package patauth provides the DB-backed PAT validator implementation.
// It lives in its own package to avoid an import cycle between internal/auth and internal/db.
package patauth

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DBPATValidator validates PATs against the database.
type DBPATValidator struct {
	repo db.Repository
}

// NewDBPATValidator creates a new DB-backed PAT validator.
func NewDBPATValidator(repo db.Repository) *DBPATValidator {
	return &DBPATValidator{repo: repo}
}

func (v *DBPATValidator) ValidatePAT(ctx context.Context, rawToken string) (userID string, patID string, daemonID string, err error) {
	if !auth.IsPATFormat(rawToken) {
		return "", "", "", fmt.Errorf("invalid token format")
	}

	hash := auth.HashPAT(rawToken)
	pat, err := v.repo.GetDaemonPATByTokenHash(ctx, hash)
	if err != nil {
		return "", "", "", fmt.Errorf("token lookup failed: %w", err)
	}
	if pat == nil {
		return "", "", "", fmt.Errorf("invalid or expired token")
	}

	// Update last_used_at asynchronously (don't block auth on this)
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := v.repo.UpdateDaemonPATLastUsed(bgCtx, pat.ID); err != nil {
			logging.Warn("Failed to update PAT last_used_at", "patID", pat.ID, "error", err)
		}
	}()

	return pat.UserID, pat.ID, pat.DaemonID, nil
}
