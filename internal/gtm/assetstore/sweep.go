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
	// ReadyStagingWindow is how far past StalePendingAge the sweep still
	// looks for a READY asset's leftover staging object: a READY asset
	// created between StalePendingAge + ReadyStagingWindow and
	// StalePendingAge ago is checked on every pass. No client can write a
	// staging key once its upload URL has expired (gtm.AssetUploadTTL), so a
	// leftover found then is final; a sweep stopped for longer than the
	// window misses the ones that aged out meanwhile.
	ReadyStagingWindow = 72 * time.Hour
	// DefaultSweepInterval is Run's interval when it is given none.
	DefaultSweepInterval = time.Hour
	// objectDeleteTimeout bounds the object deletes of one swept row.
	objectDeleteTimeout = 30 * time.Second
	// sweepBatch is how many rows one query of a pass reads.
	sweepBatch = 100
	// maxSweepRows caps the rows each half of a pass looks at; the next
	// pass resumes.
	maxSweepRows = 2000
)

// SweeperDeps are a Sweeper's collaborators.
type SweeperDeps struct {
	// DB is the ORM over the gtm_assets table.
	DB orm.Context
	// Store is the gtm Store the sweep's deletes go through. nil is
	// gtm.NewStore(DB); a test wraps it.
	Store gtm.Store
	// Assets holds the bytes (New). nil means assets are not configured,
	// and a sweep does nothing.
	Assets gtm.AssetStore
	// Logger receives what a pass could not finish. nil is slog.Default.
	Logger *slog.Logger
}

// Sweeper deletes the PENDING assets of every org that are older than
// StalePendingAge: their rows, then their objects; and the staging objects
// READY assets left behind. PR-12 schedules it (Run, or SweepStalePending
// from its own loop).
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
	rows := d.Store
	if rows == nil {
		rows = gtm.NewStore(d.DB)
	}
	return &Sweeper{db: d.DB, rows: rows, assets: d.Assets, log: log}
}

// SweepResult is what one pass did.
type SweepResult struct {
	// Deleted counts the PENDING rows deleted.
	Deleted int
	// Orphaned counts the deleted rows whose objects could not all be
	// deleted; each is logged with its keys.
	Orphaned int
	// StagingDeleted counts the leftover staging objects of READY assets
	// deleted.
	StagingDeleted int
}

// SweepStalePending is one pass, across orgs, at now:
//
//  1. The PENDING assets created at or before now - StalePendingAge, at
//     most maxSweepRows of them, oldest first. Each goes as the Service
//     discards one: its row first, and only while it is still PENDING and
//     that old, so a confirm that won a race keeps its READY asset; then its
//     staging object and its own key. A failure to delete a row ends the
//     pass with the error (the next pass retries); a failure to delete an
//     object is logged and counted as Orphaned.
//  2. The READY assets created in the ReadyStagingWindow before that
//     cutoff, at most maxSweepRows of them: a staging object Stat finds for
//     one (a confirm whose staging delete failed) is deleted. The asset and
//     its own key are never touched. A failure is logged, and the next pass
//     retries it while the asset is in the window.
func (s *Sweeper) SweepStalePending(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	if s.assets == nil {
		return res, nil
	}
	cutoff := now.Add(-StalePendingAge)
	if err := s.sweepPending(ctx, cutoff, &res); err != nil {
		return res, err
	}
	return res, s.sweepReadyStaging(ctx, cutoff.Add(-ReadyStagingWindow), cutoff, &res)
}

// sweepPending is step 1 of SweepStalePending.
func (s *Sweeper) sweepPending(ctx context.Context, cutoff time.Time, res *SweepResult) error {
	for seen := 0; seen < maxSweepRows; {
		rows, err := s.listStale(ctx, cutoff, min(sweepBatch, maxSweepRows-seen))
		if err != nil {
			return err
		}
		for _, row := range rows {
			deleted, err := s.deleteRow(ctx, row, cutoff)
			if err != nil {
				return err
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
			return nil
		}
	}
	return nil
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
// even once ctx is done (the row is already gone) but within
// objectDeleteTimeout, and reports whether both went.
func (s *Sweeper) deleteObjects(ctx context.Context, row *store.GtmAsset) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), objectDeleteTimeout)
	defer cancel()
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

// sweepReadyStaging is step 2 of SweepStalePending: the READY assets
// created after from and at or before to, oldest first, paged by
// (created_at, id).
func (s *Sweeper) sweepReadyStaging(ctx context.Context, from, to time.Time, res *SweepResult) error {
	var after *store.GtmAsset
	for seen := 0; seen < maxSweepRows; {
		rows, err := s.listReady(ctx, from, to, after, min(sweepBatch, maxSweepRows-seen))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.deleteLeftoverStaging(ctx, row) {
				res.StagingDeleted++
			}
		}
		seen += len(rows)
		if len(rows) < sweepBatch {
			return nil
		}
		after = rows[len(rows)-1]
	}
	return nil
}

// listReady reads up to limit READY assets of any org created after from
// and at or before to, past after (by created_at, then id), oldest first.
func (s *Sweeper) listReady(ctx context.Context, from, to time.Time, after *store.GtmAsset, limit int) ([]*store.GtmAsset, error) {
	created := bun.Ident(store.GtmAssetFieldCreatedAt)
	rows, err := store.ListGtmAsset(ctx, s.db,
		orm.WhereEq(store.GtmAssetFieldStatus, string(gtm.AssetReady)),
		func(q *bun.SelectQuery) {
			q.Where("? > ?", created, from).Where("? <= ?", created, to)
			if after != nil {
				q.Where("(?, ?) > (?, ?)", created, bun.Ident(store.GtmAssetFieldID), after.CreatedAt, after.ID)
			}
		},
		orm.WithOrderBy("created_at ASC, id ASC", orm.Asc),
		orm.WithLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("assetstore: list ready gtm assets: %w", err)
	}
	return rows, nil
}

// deleteLeftoverStaging deletes a READY asset's staging object when Stat
// finds one, and reports whether it did.
func (s *Sweeper) deleteLeftoverStaging(ctx context.Context, row *store.GtmAsset) bool {
	key := row.ObjectKey + stagingSuffix
	if _, err := s.assets.Stat(ctx, key); err != nil {
		if !errors.Is(err, gtm.ErrObjectNotFound) {
			s.log.WarnContext(ctx, "gtm: checking a ready asset's staging object failed", "asset_id", row.ID, "object_key", key, "error", err)
		}
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), objectDeleteTimeout)
	defer cancel()
	if err := s.assets.Delete(ctx, key); err != nil {
		s.log.WarnContext(ctx, "gtm: deleting a ready asset's leftover staging object failed", "asset_id", row.ID, "object_key", key, "error", err)
		return false
	}
	return true
}

// Run sweeps once at start and then every interval until ctx ends; an
// interval that is not positive is DefaultSweepInterval. A failed pass is
// logged and retried at the next tick.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		res, err := s.SweepStalePending(ctx, time.Now())
		attrs := []any{"deleted", res.Deleted, "orphaned", res.Orphaned, "staging_deleted", res.StagingDeleted}
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.WarnContext(ctx, "gtm: sweeping stale pending assets failed", append(attrs, "error", err)...)
		case res.Deleted > 0 || res.StagingDeleted > 0:
			s.log.InfoContext(ctx, "gtm: swept stale pending assets", attrs...)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
