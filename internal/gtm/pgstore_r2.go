// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/store"
)

// The r2/r3 tables: strategies, channels, assets, the strategy library.

var libraryUpdateCols = []string{
	store.GtmStrategyLibraryFieldCategory, store.GtmStrategyLibraryFieldDepth, store.GtmStrategyLibraryFieldName,
	store.GtmStrategyLibraryFieldSummaryMd, store.GtmStrategyLibraryFieldIcon, store.GtmStrategyLibraryFieldStage,
	store.GtmStrategyLibraryFieldCost, store.GtmStrategyLibraryFieldCostNote, store.GtmStrategyLibraryFieldDifficulty,
	store.GtmStrategyLibraryFieldLevel, store.GtmStrategyLibraryFieldObjective, store.GtmStrategyLibraryFieldNorthStar,
	store.GtmStrategyLibraryFieldHypothesisMd, store.GtmStrategyLibraryFieldHours, store.GtmStrategyLibraryFieldSignalDays,
	store.GtmStrategyLibraryFieldWindowDays, store.GtmStrategyLibraryFieldLaunchDays, store.GtmStrategyLibraryFieldExpectedDelta,
	store.GtmStrategyLibraryFieldLifecycle, store.GtmStrategyLibraryFieldChannelTypes, store.GtmStrategyLibraryFieldIntegrations,
	store.GtmStrategyLibraryFieldFits, store.GtmStrategyLibraryFieldPrerequisites, store.GtmStrategyLibraryFieldKpis,
	store.GtmStrategyLibraryFieldTactics, store.GtmStrategyLibraryFieldAutomates, store.GtmStrategyLibraryFieldExperiments,
	store.GtmStrategyLibraryFieldStopWhen, store.GtmStrategyLibraryFieldSteps, store.GtmStrategyLibraryFieldExtra,
	store.GtmStrategyLibraryFieldSourceRevision, store.GtmStrategyLibraryFieldImportedAt, store.GtmStrategyLibraryFieldImportedBy,
}

// --- strategies ---

func (p pgStore) ListStrategies(ctx context.Context, f StrategyFilter) ([]*store.GtmStrategy, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmStrategyFieldOrgID, f.OrgID)}
	if f.Status != "" {
		opts = append(opts, orm.WhereEq(store.GtmStrategyFieldStatus, string(f.Status)))
	}
	opts = append(opts, newestFirst, pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmStrategy(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm strategies: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetStrategy(ctx context.Context, orgID, id string) (*store.GtmStrategy, error) {
	rows, err := store.ListGtmStrategy(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm strategy")
}

func (p pgStore) CreateStrategy(ctx context.Context, row *store.GtmStrategy) error {
	return classify(store.CreateGtmStrategy(ctx, p.db, row), "create gtm strategy")
}

func (p pgStore) UpdateStrategy(ctx context.Context, row *store.GtmStrategy, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmStrategyWhere(ctx, p.db, &v, strategyUpdateCols, atRevision(row.OrgID, row.ID, expected)...)
	return updated(n, err, "update gtm strategy")
}

// --- channels ---

func (p pgStore) ListChannels(ctx context.Context, f ChannelFilter) ([]*store.GtmChannel, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmChannelFieldOrgID, f.OrgID)}
	if f.Status != "" {
		opts = append(opts, orm.WhereEq(store.GtmChannelFieldStatus, string(f.Status)))
	}
	if f.Mode != "" {
		opts = append(opts, orm.WhereEq(store.GtmChannelFieldMode, string(f.Mode)))
	}
	opts = append(opts, orm.WithOrderBy("key ASC, id ASC", orm.Asc), pageOpts(f.Limit, 0))
	rows, err := store.ListGtmChannel(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm channels: %w", err)
	}
	return rows, nil
}

func (p pgStore) CountChannels(ctx context.Context, orgID string) (int64, error) {
	n, err := store.CountGtmChannel(ctx, p.db, orm.WhereEq(store.GtmChannelFieldOrgID, orgID))
	if err != nil {
		return 0, fmt.Errorf("count gtm channels: %w", err)
	}
	return n, nil
}

func (p pgStore) GetChannel(ctx context.Context, orgID, id string) (*store.GtmChannel, error) {
	rows, err := store.ListGtmChannel(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm channel")
}

func (p pgStore) SeedChannels(ctx context.Context, rows []*store.GtmChannel) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := p.db.Bun().NewInsert().Model(&rows).On("CONFLICT (org_id, key) DO NOTHING").Exec(ctx)
	return classify(err, "seed gtm channels")
}

func (p pgStore) CreateChannel(ctx context.Context, row *store.GtmChannel) error {
	return classify(store.CreateGtmChannel(ctx, p.db, row), "create gtm channel")
}

func (p pgStore) UpdateChannel(ctx context.Context, row *store.GtmChannel, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmChannelWhere(ctx, p.db, &v, channelUpdateCols, atRevision(row.OrgID, row.ID, expected)...)
	return updated(n, err, "update gtm channel")
}

// --- coverage ---

func (p pgStore) CampaignItemCounts(ctx context.Context, orgID, campaignID string) ([]ItemCount, error) {
	rows, err := p.db.Query(ctx, `SELECT COALESCE(channel_id, ''), format, status, count(*)
		FROM gtm_items WHERE org_id = $1 AND campaign_id = $2
		GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, orgID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("count gtm campaign items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ItemCount
	for rows.Next() {
		var c ItemCount
		var format, status string
		if err := rows.Scan(&c.ChannelID, &format, &status, &c.Count); err != nil {
			return nil, fmt.Errorf("count gtm campaign items: %w", err)
		}
		c.Format, c.Status = ItemFormat(format), ItemStatus(status)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count gtm campaign items: %w", err)
	}
	return out, nil
}

// --- assets ---

func (p pgStore) ListAssets(ctx context.Context, f AssetFilter) ([]*store.GtmAsset, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmAssetFieldOrgID, f.OrgID)}
	if f.ItemID != "" {
		opts = append(opts, orm.WhereEq(store.GtmAssetFieldItemID, f.ItemID))
	}
	if f.CampaignID != "" {
		opts = append(opts, orm.WhereEq(store.GtmAssetFieldCampaignID, f.CampaignID))
	}
	opts = append(opts, newestFirst, pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmAsset(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm assets: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetAsset(ctx context.Context, orgID, id string) (*store.GtmAsset, error) {
	rows, err := store.ListGtmAsset(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm asset")
}

func (p pgStore) SumAssetBytes(ctx context.Context, orgID string, pendingAfter time.Time) (int64, error) {
	var n int64
	err := p.db.QueryRow(ctx, `SELECT COALESCE(SUM(size_bytes), 0) FROM gtm_assets
		WHERE org_id = $1 AND (status <> $2 OR created_at > $3)`, orgID, string(AssetPending), pendingAfter).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sum gtm asset bytes: %w", err)
	}
	return n, nil
}

func (p pgStore) ListExpiredAssets(ctx context.Context, orgID string, createdBefore time.Time, limit int) ([]*store.GtmAsset, error) {
	rows, err := store.ListGtmAsset(ctx, p.db,
		orm.WhereEq(store.GtmAssetFieldOrgID, orgID),
		orm.WhereEq(store.GtmAssetFieldStatus, string(AssetPending)),
		func(q *bun.SelectQuery) { q.Where("? <= ?", bun.Ident(store.GtmAssetFieldCreatedAt), createdBefore) },
		orm.WithOrderBy("created_at ASC, id ASC", orm.Asc),
		pageOpts(limit, 0))
	if err != nil {
		return nil, fmt.Errorf("list expired gtm assets: %w", err)
	}
	return rows, nil
}

func (p pgStore) CreateAsset(ctx context.Context, row *store.GtmAsset) error {
	return classify(store.CreateGtmAsset(ctx, p.db, row), "create gtm asset")
}

func (p pgStore) MarkAssetReady(ctx context.Context, orgID, id, contentType string, size int64, sha256 string, createdAfter time.Time) error {
	res, err := p.db.Exec(ctx, `UPDATE gtm_assets SET status = $1, content_type = $2, size_bytes = $3, sha256 = $4
		WHERE org_id = $5 AND id = $6 AND status = $7 AND created_at > $8`,
		string(AssetReady), contentType, size, sha256, orgID, id, string(AssetPending), createdAfter)
	if err := execUpdated(res, err, "mark gtm asset ready"); err != nil {
		if err == ErrRevisionConflict {
			return ErrAssetNotPending
		}
		return err
	}
	return nil
}

func (p pgStore) DeletePendingAsset(ctx context.Context, orgID, id string, createdBefore time.Time) (bool, error) {
	q := p.db.Bun().NewDelete().Model((*store.GtmAsset)(nil)).
		Where("? = ?", bun.Ident(store.GtmAssetFieldOrgID), orgID).
		Where("? = ?", bun.Ident(store.GtmAssetFieldID), id).
		Where("? = ?", bun.Ident(store.GtmAssetFieldStatus), string(AssetPending))
	if !createdBefore.IsZero() {
		q = q.Where("? <= ?", bun.Ident(store.GtmAssetFieldCreatedAt), createdBefore)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return false, classify(err, "delete pending gtm asset")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete pending gtm asset: %w", err)
	}
	return n > 0, nil
}

func (p pgStore) DeleteAsset(ctx context.Context, orgID, id string) error {
	_, err := p.db.Bun().NewDelete().Model((*store.GtmAsset)(nil)).
		Where("? = ?", bun.Ident(store.GtmAssetFieldOrgID), orgID).
		Where("? = ?", bun.Ident(store.GtmAssetFieldID), id).
		Exec(ctx)
	return classify(err, "delete gtm asset")
}

// --- library ---

func (p pgStore) ListLibrary(ctx context.Context, f LibraryFilter) ([]*store.GtmStrategyLibrary, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmStrategyLibraryFieldOrgID, f.OrgID)}
	for col, v := range map[string]string{
		store.GtmStrategyLibraryFieldCategory:   string(f.Category),
		store.GtmStrategyLibraryFieldDepth:      string(f.Depth),
		store.GtmStrategyLibraryFieldStage:      f.Stage,
		store.GtmStrategyLibraryFieldCost:       f.Cost,
		store.GtmStrategyLibraryFieldDifficulty: f.Difficulty,
	} {
		if v != "" {
			opts = append(opts, orm.WhereEq(col, v))
		}
	}
	if f.Fit != "" {
		fit := f.Fit
		opts = append(opts, func(q *bun.SelectQuery) { q.Where("? = ANY(?)", fit, bun.Ident(store.GtmStrategyLibraryFieldFits)) })
	}
	if f.Query != "" {
		opts = append(opts, orm.WhereILikeAny(
			[]string{store.GtmStrategyLibraryFieldName, store.GtmStrategyLibraryFieldSummaryMd},
			"%"+orm.EscapeLike(f.Query)+"%"))
	}
	if f.Keys != nil {
		if len(f.Keys) == 0 {
			return nil, nil
		}
		opts = append(opts, orm.WhereIn(store.GtmStrategyLibraryFieldKey, f.Keys))
	}
	opts = append(opts, orm.WithOrderBy("category ASC, name ASC, key ASC", orm.Asc), pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmStrategyLibrary(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm strategy library: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetLibraryEntry(ctx context.Context, orgID, key string) (*store.GtmStrategyLibrary, error) {
	rows, err := store.ListGtmStrategyLibrary(ctx, p.db,
		orm.WhereEq(store.GtmStrategyLibraryFieldOrgID, orgID), orm.WhereEq(store.GtmStrategyLibraryFieldKey, key), orm.WithLimit(1))
	return one(rows, err, "gtm strategy library entry")
}

func (p pgStore) CreateLibraryEntry(ctx context.Context, row *store.GtmStrategyLibrary) error {
	return classify(store.CreateGtmStrategyLibrary(ctx, p.db, row), "create gtm strategy library entry")
}

func (p pgStore) UpdateLibraryEntry(ctx context.Context, row *store.GtmStrategyLibrary) error {
	n, err := store.UpdateGtmStrategyLibraryWhere(ctx, p.db, row, libraryUpdateCols,
		orm.WhereEq(store.GtmStrategyLibraryFieldOrgID, row.OrgID), orm.WhereEq(store.GtmStrategyLibraryFieldID, row.ID))
	if err := updated(n, err, "update gtm strategy library entry"); err != nil {
		if err == ErrRevisionConflict {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (p pgStore) PruneLibrary(ctx context.Context, orgID string, keep []string) (int64, error) {
	q := p.db.Bun().NewDelete().Model((*store.GtmStrategyLibrary)(nil)).
		Where("? = ?", bun.Ident(store.GtmStrategyLibraryFieldOrgID), orgID)
	if len(keep) > 0 {
		q = q.Where("? NOT IN (?)", bun.Ident(store.GtmStrategyLibraryFieldKey), bun.In(keep))
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return 0, classify(err, "prune gtm strategy library")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune gtm strategy library: %w", err)
	}
	return n, nil
}
