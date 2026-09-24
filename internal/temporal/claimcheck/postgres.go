// Copyright (c) 2025 Reliant Labs
package claimcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// DefaultGCHorizon is how long a blob survives after it was last written.
//
// A blob must outlive every workflow history that references it. A history
// lives at most WorkflowExecutionTimeout (14d) while running plus namespace
// retention (7d prod, 3d elsewhere) after close: 21d. Every reference is
// created by an Encode, and every Encode upserts the row (refreshing
// last_referenced_at), so no history can reference a blob older than its own
// start. 30d = 21d + 9d margin.
//
// Get deliberately does NOT refresh last_referenced_at. Decode runs on the
// workflow-task path; an UPDATE per decode is a write on the hot path, and it
// is unnecessary given the bound above. If WorkflowExecutionTimeout or
// retention grows, raise this with it — the invariant is
// horizon > max run + max retention.
const DefaultGCHorizon = 30 * 24 * time.Hour

// GCHorizonEnv overrides DefaultGCHorizon (Go duration syntax, e.g. "720h").
const GCHorizonEnv = "RELIANT_PAYLOAD_BLOB_GC_HORIZON"

const (
	gcInterval  = time.Hour
	gcBatchSize = 1000
)

// PostgresStore is a Store over the temporal_payload_blobs table. It takes a
// bare *sql.DB rather than the repo: internal/temporal must stay importable
// from below internal/db.
type PostgresStore struct {
	db *sql.DB
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by db.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Put upserts the blob. Content-addressed, so a conflict means identical
// bytes: only the retention timestamp is refreshed.
func (s *PostgresStore) Put(ctx context.Context, key string, data []byte, rawSize int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO temporal_payload_blobs (key, data, size_bytes)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET last_referenced_at = NOW()`,
		key, data, rawSize)
	return err
}

// Get returns the stored blob. See DefaultGCHorizon for why it does not
// refresh last_referenced_at.
func (s *PostgresStore) Get(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM temporal_payload_blobs WHERE key = $1`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return data, err
}

// DeleteExpired deletes blobs not referenced within horizon, in batches so a
// large backlog never holds one long lock. Returns the number deleted.
func (s *PostgresStore) DeleteExpired(ctx context.Context, horizon time.Duration) (int64, error) {
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM temporal_payload_blobs
			WHERE key IN (
				SELECT key FROM temporal_payload_blobs
				WHERE last_referenced_at < NOW() - make_interval(secs => $1)
				LIMIT $2
			)`, horizon.Seconds(), gcBatchSize)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < gcBatchSize {
			return total, nil
		}
	}
}

// GCHorizonFromEnv returns DefaultGCHorizon, or the GCHorizonEnv override
// when it parses to a positive duration.
func GCHorizonFromEnv() time.Duration {
	v := os.Getenv(GCHorizonEnv)
	if v == "" {
		return DefaultGCHorizon
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logging.Warn("Ignoring invalid payload blob GC horizon; using default",
			"env", GCHorizonEnv, "value", v, "default", DefaultGCHorizon)
		return DefaultGCHorizon
	}
	return d
}

// RunGC deletes expired blobs once immediately and then every hour until ctx
// is done. Run it from exactly one kind of process (the api-server, which
// owns the schema); concurrent runs are safe but wasteful.
func (s *PostgresStore) RunGC(ctx context.Context, horizon time.Duration) {
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for {
		deleted, err := s.DeleteExpired(ctx, horizon)
		switch {
		case err != nil && ctx.Err() == nil:
			logging.Warn("Payload blob GC failed", "error", err, "deleted_before_error", deleted)
		case deleted > 0:
			logging.Info("Payload blob GC deleted expired blobs", "deleted", deleted, "horizon", horizon)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
