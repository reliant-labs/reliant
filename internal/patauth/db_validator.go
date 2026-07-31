// Package patauth provides the DB-backed PAT validator implementation for the
// daemon/gateway side. It lives in its own package to avoid an import cycle
// between internal/auth and internal/db, and is a thin adapter over
// internal/pat — the single hashing/minting/validation implementation.
package patauth

import (
	"context"

	"github.com/reliant-labs/reliant/internal/pat"
)

// DBPATValidator validates daemon-kind PATs against the database. It accepts
// kind='daemon' tokens ONLY: api-kind tokens (user API credentials) are
// rejected here exactly like unknown tokens, so an API token can never
// authenticate a daemon <-> gateway stream.
type DBPATValidator struct {
	svc *pat.Service
}

// NewDBPATValidator creates a new DB-backed PAT validator. store is satisfied
// by db.Repository.
func NewDBPATValidator(store pat.Store) *DBPATValidator {
	return &DBPATValidator{svc: pat.NewService(store)}
}

// ValidatePAT implements auth.PATValidator.
func (v *DBPATValidator) ValidatePAT(ctx context.Context, rawToken string) (userID string, patID string, daemonID string, err error) {
	p, err := v.svc.ValidateDaemonPAT(ctx, rawToken)
	if err != nil {
		return "", "", "", err
	}
	return p.UserID, p.ID, p.DaemonID, nil
}
