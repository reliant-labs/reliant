// Copyright (c) 2025 Reliant Labs
package claimcheck

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
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

const (
	gcInterval  = time.Hour
	gcBatchSize = 1000
)

// PostgresStore is a Store over the temporal_payload_blobs table, through
// the forge ORM (internal/store's temporal_payload_blob_repo_ext.go). It takes
// an orm.Context rather than the repo: internal/temporal must stay importable
// from below internal/db, and internal/store is.
type PostgresStore struct {
	db orm.Context
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by db.
func NewPostgresStore(db orm.Context) *PostgresStore {
	return &PostgresStore{db: db}
}

// Put upserts the blob. Content-addressed, so a conflict means identical
// bytes: only the retention timestamp is refreshed.
func (s *PostgresStore) Put(ctx context.Context, key string, data []byte, rawSize int) error {
	return store.PutTemporalPayloadBlob(ctx, s.db, key, data, rawSize)
}

// Get returns the stored blob. See DefaultGCHorizon for why it does not
// refresh last_referenced_at.
func (s *PostgresStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := store.TemporalPayloadBlobData(ctx, s.db, key)
	if svcerr.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return data, err
}

// DeleteExpired deletes blobs not referenced within horizon, in batches so a
// large backlog never holds one long lock. Returns the number deleted.
func (s *PostgresStore) DeleteExpired(ctx context.Context, horizon time.Duration) (int64, error) {
	var total int64
	for {
		n, err := store.DeleteExpiredTemporalPayloadBlobs(ctx, s.db, horizon, gcBatchSize)
		if err != nil {
			return total, err
		}
		total += n
		if n < gcBatchSize {
			return total, nil
		}
	}
}

// RunGC deletes expired blobs once immediately and then every hour until ctx
// is done. Run it from exactly one kind of process (the api-server, which
// owns the schema); concurrent runs are safe but wasteful.
func (s *PostgresStore) RunGC(ctx context.Context, horizon time.Duration) {
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for {
		var deleted int64
		err := observe.RunRoot(ctx, "claimcheck.gc", func(ctx context.Context) error {
			var err error
			deleted, err = s.DeleteExpired(ctx, horizon)
			return err
		})
		switch {
		case err != nil && ctx.Err() == nil:
			logging.WarnContext(ctx, "Payload blob GC failed", "error", err, "deleted_before_error", deleted)
		case deleted > 0:
			logging.InfoContext(ctx, "Payload blob GC deleted expired blobs", "deleted", deleted, "horizon", horizon)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
