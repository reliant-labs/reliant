// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

// OpenReadOnlyRepo opens a Postgres connection and wraps it in a *Repo WITHOUT
// running migrations. It is intended for read-only tooling (run analysis /
// node inspection) that must not mutate the target database: the normal
// NewRepoFromConfig path applies migrations on connect, which is a write.
//
// The caller owns the returned Repo and must Close() it. The pgx stdlib driver
// is registered by this package's connect.go import, so callers need not import
// it themselves.
func OpenReadOnlyRepo(url string) (*Repo, error) {
	if url == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	sqlDB, err := dbtrace.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect to postgres database: %w", err)
	}
	repo, err := NewRepoWithDriver(sqlDB, DriverPostgres)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return repo, nil
}
