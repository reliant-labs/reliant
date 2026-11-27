// Copyright (c) 2026 Reliant Labs

package assetstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/store"
)

const (
	// StalePendingAge is how old a PENDING asset is before the sweep deletes
	// it. The Service expires one after gtm.AssetUploadTTL plus a few
	// minutes, and deletes it on that org's next upload or confirm; the
	// sweep catches the ones whose org never comes back.
	StalePendingAge = 24 * time.Hour
	// sweepBatch is how many rows one query of a pass reads.
	sweepBatch = 100
	// maxSweepRows caps the rows one pass looks at; the next pass resumes.
	maxSweepRows = 2000
)

// SweeperDeps are a Sweeper's collaborators.
type SweeperDeps struct {
	// DB is the ORM over the gtm_assets table.
	DB orm.Context
	// Assets holds the bytes (New). nil means assets are not configured,
	// and a sweep does nothing.
	Assets gtm.AssetStore
	// Logger receives what a pass could not finish. nil is slog.Default.
	Logger *slog.Logger
}

// Sweeper deletes the PENDING assets of every org that are older than
// StalePendingAge: their rows, then their objects. PR-12 schedules it
// (Run, or SweepStalePending from its own loop).
type Sweeper struct {
	db     orm.Context
	rows   gtm.Store
	assets gtm.AssetStore
	log    *slog.Logger
}

// NewSweeper returns the sweep over d.
func NewSweeper(d SweeperDeps) *Sweeper {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Sweeper{db: d.DB, rows: gtm.NewStore(d.DB), assets: d.Assets, log: log}
}

// SweepResult is what one pass did.
type SweepResult struct {
	// Deleted counts the rows deleted.
	Deleted int
	// Orphaned counts the deleted rows whose objects could not all be
	// deleted; each is logged with its keys.
	Orphaned int
}

// SweepStalePending deletes, across orgs, the PENDING assets created at or
// before now - StalePendingAge, at most maxSweepRows of them, oldest first.
// Each goes as the Service discards one: its row first, and only while it
// is still PENDING and that old, so a confirm that won a race keeps its
// READY asset; then its staging object and its own key. A failure to
// delete a row ends the pass with the error (the next pass retries); a
// failure to delete an object is logged and counted as Orphaned.
func (s *Sweeper) SweepStalePending(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	if s.assets == nil {
		return res, nil
	}
	cutoff := now.Add(-StalePendingAge)
	for seen := 0; seen < maxSweepRows; {
		rows, err := s.listStale(ctx, cutoff, min(sweepBatch, maxSweepRows-seen))
		if err != nil {
			return res, err
		}
		for _, row := range rows {
			deleted, err := s.deleteRow(ctx, row, cutoff)
			if err != nil {
				return res, err
			}
			if !deleted {
				continue
			}
			res.Deleted++
			if !s.deleteObjects(ctx, row) {
				res.Orphaned++
			}
		}
		seen += len(rows)
		if len(rows) < sweepBatch {
			break
		}
	}
	return res, nil
}

// listStale reads up to limit PENDING assets of any org created at or
// before cutoff, oldest first.
func (s *Sweeper) listStale(ctx context.Context, cutoff time.Time, limit int) ([]*store.GtmAsset, error) {
	rows, err := store.ListGtmAsset(ctx, s.db,
		orm.WhereEq(store.GtmAssetFieldStatus, string(gtm.AssetPending)),
		func(q *bun.SelectQuery) { q.Where("? <= ?", bun.Ident(store.GtmAssetFieldCreatedAt), cutoff) },
		orm.WithOrderBy("created_at ASC, id ASC", orm.Asc),
		orm.WithLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("assetstore: list stale pending gtm assets: %w", err)
	}
	return rows, nil
}

// deleteRow deletes row while it is still PENDING and created at or before
// cutoff, and reports whether it did.
func (s *Sweeper) deleteRow(ctx context.Context, row *store.GtmAsset, cutoff time.Time) (bool, error) {
	var deleted bool
	err := s.rows.InTx(ctx, func(ctx context.Context) error {
		var err error
		deleted, err = s.rows.DeletePendingAsset(ctx, row.OrgID, row.ID, cutoff)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("assetstore: delete stale pending gtm asset %s: %w", row.ID, err)
	}
	return deleted, nil
}

// deleteObjects deletes a deleted row's staging object and its own key,
// even once ctx is done (the row is already gone), and reports whether both
// went.
func (s *Sweeper) deleteObjects(ctx context.Context, row *store.GtmAsset) bool {
	ctx = context.WithoutCancel(ctx)
	var errs []error
	for _, key := range []string{row.ObjectKey + stagingSuffix, row.ObjectKey} {
		if err := s.assets.Delete(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a stale pending asset's objects failed; they are orphaned",
			"asset_id", row.ID, "org_id", row.OrgID, "object_key", row.ObjectKey, "error", err)
		return false
	}
	return true
}

// Run sweeps once at start and then every interval until ctx ends. A
// failed pass is logged and retried at the next tick.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		res, err := s.SweepStalePending(ctx, time.Now())
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.WarnContext(ctx, "gtm: sweeping stale pending assets failed", "deleted", res.Deleted, "orphaned", res.Orphaned, "error", err)
		case res.Deleted > 0:
			s.log.InfoContext(ctx, "gtm: swept stale pending assets", "deleted", res.Deleted, "orphaned", res.Orphaned)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
