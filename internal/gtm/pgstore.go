// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/store"
)

// NewStore is Store over the ORM's gtm_* tables (the generated internal/store
// delegates). Called with the ctx Store.InTx hands its fn, every method joins
// that transaction.
func NewStore(db orm.Context) Store { return pgStore{db: db} }

type pgStore struct{ db orm.Context }

var _ Store = pgStore{}

// The columns an update writes: everything but the key, the org, the
// creation stamp (updated_at is stamped by the write) and, for a claim, its
// immutable text and category.
var (
	ideaUpdateCols = []string{
		store.GtmIdeaFieldKind, store.GtmIdeaFieldTitle, store.GtmIdeaFieldBodyMd, store.GtmIdeaFieldEvidence,
		store.GtmIdeaFieldStatus, store.GtmIdeaFieldSource, store.GtmIdeaFieldSourceChatID,
		store.GtmIdeaFieldCampaignID, store.GtmIdeaFieldRevision,
		store.GtmIdeaFieldLibraryKey, store.GtmIdeaFieldTailorAnswers, store.GtmIdeaFieldSuggestedWorkflowMd,
	}
	campaignUpdateCols = []string{
		store.GtmCampaignFieldName, store.GtmCampaignFieldGoal, store.GtmCampaignFieldPeriodStart,
		store.GtmCampaignFieldPeriodEnd, store.GtmCampaignFieldStatus, store.GtmCampaignFieldRevision,
		store.GtmCampaignFieldStrategyID, store.GtmCampaignFieldPlannedChannelIds, store.GtmCampaignFieldPlannedFormats,
	}
	itemUpdateCols = []string{
		store.GtmItemFieldCampaignID, store.GtmItemFieldIdeaID, store.GtmItemFieldFormat, store.GtmItemFieldChannelID,
		store.GtmItemFieldTitle, store.GtmItemFieldBodyMd, store.GtmItemFieldStatus, store.GtmItemFieldScheduledAt,
		store.GtmItemFieldEmailTo, store.GtmItemFieldEmailSubject, store.GtmItemFieldGmailMessageID,
		store.GtmItemFieldSentAt, store.GtmItemFieldSentBy, store.GtmItemFieldApprovalHash, store.GtmItemFieldRevision,
		store.GtmItemFieldPublishedURL,
	}
	strategyUpdateCols = []string{
		store.GtmStrategyFieldName, store.GtmStrategyFieldGoalMd, store.GtmStrategyFieldTargetMetric,
		store.GtmStrategyFieldPeriodStart, store.GtmStrategyFieldPeriodEnd, store.GtmStrategyFieldStatus,
		store.GtmStrategyFieldCategory, store.GtmStrategyFieldUpdatedBy, store.GtmStrategyFieldRevision,
	}
	channelUpdateCols = []string{
		store.GtmChannelFieldKey, store.GtmChannelFieldName, store.GtmChannelFieldMode, store.GtmChannelFieldIntegrationKind,
		store.GtmChannelFieldConnectionID, store.GtmChannelFieldManualInstructionsMd, store.GtmChannelFieldStatus,
		store.GtmChannelFieldChannelTypes,
		store.GtmChannelFieldRevision,
	}
	positioningUpdateCols = []string{
		store.GtmPositioningFieldPositioningMd, store.GtmPositioningFieldAudiences,
		store.GtmPositioningFieldUpdatedBy, store.GtmPositioningFieldRevision,
		store.GtmPositioningFieldBusinessType, store.GtmPositioningFieldBusinessModels,
	}
	claimUpdateCols = []string{
		store.GtmClaimFieldStatus, store.GtmClaimFieldSourceURL, store.GtmClaimFieldSourceKind,
		store.GtmClaimFieldVerifiedAt, store.GtmClaimFieldVerifiedBy, store.GtmClaimFieldExpiresOrRecheckAt,
		store.GtmClaimFieldNotes, store.GtmClaimFieldRevision,
	}
)

func (p pgStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return p.db.RunTx(ctx, orm.Serializable, fn)
}

// --- ideas ---

func (p pgStore) ListIdeas(ctx context.Context, f IdeaFilter) ([]*store.GtmIdea, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmIdeaFieldOrgID, f.OrgID)}
	if f.Kind != "" {
		opts = append(opts, orm.WhereEq(store.GtmIdeaFieldKind, string(f.Kind)))
	}
	if f.Status != "" {
		opts = append(opts, orm.WhereEq(store.GtmIdeaFieldStatus, string(f.Status)))
	}
	opts = append(opts, linkFilter(store.GtmIdeaFieldCampaignID, f.CampaignID)...)
	opts = append(opts, newestFirst, pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmIdea(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm ideas: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetIdea(ctx context.Context, orgID, id string) (*store.GtmIdea, error) {
	rows, err := store.ListGtmIdea(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm idea")
}

func (p pgStore) CreateIdea(ctx context.Context, row *store.GtmIdea) error {
	return classify(store.CreateGtmIdea(ctx, p.db, row), "create gtm idea")
}

func (p pgStore) UpdateIdea(ctx context.Context, row *store.GtmIdea, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmIdeaWhere(ctx, p.db, &v, ideaUpdateCols, atRevision(row.OrgID, row.ID, expected)...)
	return updated(n, err, "update gtm idea")
}

// --- campaigns ---

func (p pgStore) ListCampaigns(ctx context.Context, f CampaignFilter) ([]*store.GtmCampaign, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmCampaignFieldOrgID, f.OrgID)}
	if f.Status != "" {
		opts = append(opts, orm.WhereEq(store.GtmCampaignFieldStatus, string(f.Status)))
	}
	if f.StrategyID != "" {
		opts = append(opts, orm.WhereEq(store.GtmCampaignFieldStrategyID, f.StrategyID))
	}
	opts = append(opts, newestFirst, pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmCampaign(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm campaigns: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetCampaign(ctx context.Context, orgID, id string) (*store.GtmCampaign, error) {
	rows, err := store.ListGtmCampaign(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm campaign")
}

func (p pgStore) CreateCampaign(ctx context.Context, row *store.GtmCampaign) error {
	return classify(store.CreateGtmCampaign(ctx, p.db, row), "create gtm campaign")
}

func (p pgStore) UpdateCampaign(ctx context.Context, row *store.GtmCampaign, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmCampaignWhere(ctx, p.db, &v, campaignUpdateCols, atRevision(row.OrgID, row.ID, expected)...)
	return updated(n, err, "update gtm campaign")
}

// --- items ---

func (p pgStore) ListItems(ctx context.Context, f ItemFilter) ([]*store.GtmItem, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmItemFieldOrgID, f.OrgID)}
	opts = append(opts, linkFilter(store.GtmItemFieldCampaignID, f.CampaignID)...)
	if f.Format != "" {
		opts = append(opts, orm.WhereEq(store.GtmItemFieldFormat, string(f.Format)))
	}
	opts = append(opts, linkFilter(store.GtmItemFieldChannelID, f.ChannelID)...)
	if f.Status != "" {
		opts = append(opts, orm.WhereEq(store.GtmItemFieldStatus, string(f.Status)))
	}
	if f.ScheduledFrom != nil {
		opts = append(opts, orm.WhereGte(store.GtmItemFieldScheduledAt, *f.ScheduledFrom))
	}
	if f.ScheduledTo != nil {
		opts = append(opts, orm.WhereLt(store.GtmItemFieldScheduledAt, *f.ScheduledTo))
	}
	opts = append(opts, calendarOrder, pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmItem(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm items: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetItem(ctx context.Context, orgID, id string) (*store.GtmItem, error) {
	rows, err := store.ListGtmItem(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm item")
}

// The hold writes are raw statements: they set updated_at to the hold's own
// time (the generated UpdateWhere stamps the wall clock over it) and, for an
// invalidating release, advance the revision in the same statement.

func (p pgStore) HoldItemSend(ctx context.Context, orgID, id string, revision int32, hash string, at time.Time) error {
	res, err := p.db.Exec(ctx, `UPDATE gtm_items SET approval_hash = $1, updated_at = $2
		WHERE org_id = $3 AND id = $4 AND revision = $5 AND status = $6 AND approval_hash = ''`,
		hash, at, orgID, id, revision, string(ItemApproved))
	return execUpdated(res, err, "hold gtm item send")
}

func (p pgStore) ReleaseItemSend(ctx context.Context, orgID, id string, revision int32, hash string, at time.Time, invalidate bool) error {
	bump := 0
	if invalidate {
		bump = 1
	}
	res, err := p.db.Exec(ctx, `UPDATE gtm_items SET approval_hash = '', revision = revision + $1, updated_at = $2
		WHERE org_id = $3 AND id = $4 AND revision = $5 AND status = $6 AND approval_hash = $7`,
		bump, at, orgID, id, revision, string(ItemApproved), hash)
	return execUpdated(res, err, "release gtm item send")
}

func execUpdated(res sql.Result, err error, op string) error {
	if err != nil {
		return classify(err, op)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return updated(n, nil, op)
}

func (p pgStore) CreateItem(ctx context.Context, row *store.GtmItem) error {
	return classify(store.CreateGtmItem(ctx, p.db, row), "create gtm item")
}

func (p pgStore) UpdateItem(ctx context.Context, row *store.GtmItem, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmItemWhere(ctx, p.db, &v, itemUpdateCols, atRevision(row.OrgID, row.ID, expected)...)
	return updated(n, err, "update gtm item")
}

// --- positioning ---

func (p pgStore) GetPositioning(ctx context.Context, orgID string) (*store.GtmPositioning, error) {
	rows, err := store.ListGtmPositioning(ctx, p.db, orm.WhereEq(store.GtmPositioningFieldOrgID, orgID), orm.WithLimit(1))
	return one(rows, err, "gtm positioning")
}

func (p pgStore) CreatePositioning(ctx context.Context, row *store.GtmPositioning) error {
	err := store.CreateGtmPositioning(ctx, p.db, row)
	if orm.IsUniqueViolation(err) {
		// The org's row appeared since the read: someone else wrote first.
		return ErrRevisionConflict
	}
	return classify(err, "create gtm positioning")
}

func (p pgStore) UpdatePositioning(ctx context.Context, row *store.GtmPositioning, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmPositioningWhere(ctx, p.db, &v, positioningUpdateCols,
		orm.WhereEq(store.GtmPositioningFieldOrgID, row.OrgID),
		orm.WhereEq(store.GtmPositioningFieldRevision, expected))
	return updated(n, err, "update gtm positioning")
}

// --- claims ---

func (p pgStore) ListClaims(ctx context.Context, f ClaimFilter) ([]*store.GtmClaim, error) {
	opts := []orm.QueryOption{orm.WhereEq(store.GtmClaimFieldOrgID, f.OrgID)}
	if f.Status != "" {
		opts = append(opts, orm.WhereEq(store.GtmClaimFieldStatus, string(f.Status)))
	}
	if f.Category != "" {
		opts = append(opts, orm.WhereEq(store.GtmClaimFieldCategory, string(f.Category)))
	}
	if f.Search != "" {
		opts = append(opts, orm.WhereILike(store.GtmClaimFieldText, "%"+orm.EscapeLike(f.Search)+"%"))
	}
	opts = append(opts, newestFirst, pageOpts(f.Limit, f.Offset))
	rows, err := store.ListGtmClaim(ctx, p.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list gtm claims: %w", err)
	}
	return rows, nil
}

func (p pgStore) GetClaim(ctx context.Context, orgID, id string) (*store.GtmClaim, error) {
	rows, err := store.ListGtmClaim(ctx, p.db, byOrgID(orgID, id)...)
	return one(rows, err, "gtm claim")
}

func (p pgStore) CreateClaim(ctx context.Context, row *store.GtmClaim) error {
	return classify(store.CreateGtmClaim(ctx, p.db, row), "create gtm claim")
}

func (p pgStore) UpdateClaim(ctx context.Context, row *store.GtmClaim, expected int32) error {
	v := *row
	v.Revision = expected + 1
	n, err := store.UpdateGtmClaimWhere(ctx, p.db, &v, claimUpdateCols, atRevision(row.OrgID, row.ID, expected)...)
	return updated(n, err, "update gtm claim")
}

// --- query pieces ---

// Every gtm_* table but the positioning has id, org_id and created_at.
var (
	newestFirst   = orm.WithOrderBy("created_at DESC, id DESC", orm.Desc)
	calendarOrder = orm.QueryOption(func(q *bun.SelectQuery) {
		q.OrderExpr("? ASC NULLS LAST, ? DESC, ? DESC",
			bun.Ident(store.GtmItemFieldScheduledAt), bun.Ident(store.GtmItemFieldCreatedAt), bun.Ident(store.GtmItemFieldID))
	})
)

func byOrgID(orgID, id string) []orm.QueryOption {
	return []orm.QueryOption{orm.WhereEq("org_id", orgID), orm.WhereEq("id", id), orm.WithLimit(1)}
}

func atRevision(orgID, id string, expected int32) []orm.QueryOption {
	return []orm.QueryOption{orm.WhereEq("org_id", orgID), orm.WhereEq("id", id), orm.WhereEq("revision", expected)}
}

// linkFilter is a nil link (any), a pointer to "" (unlinked) or an id.
func linkFilter(col string, id *string) []orm.QueryOption {
	switch {
	case id == nil:
		return nil
	case *id == "":
		return []orm.QueryOption{orm.WhereIsNull(col)}
	}
	return []orm.QueryOption{orm.WhereEq(col, *id)}
}

func pageOpts(limit, offset int) orm.QueryOption {
	return func(q *bun.SelectQuery) {
		if limit > 0 {
			q.Limit(int64(limit))
		}
		if offset > 0 {
			q.Offset(int64(offset))
		}
	}
}

func one[T any](rows []*T, err error, what string) (*T, error) {
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", what, err)
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}

func updated(n int64, err error, op string) error {
	if err != nil {
		return classify(err, op)
	}
	if n == 0 {
		return ErrRevisionConflict
	}
	return nil
}

// classify converts a write's driver error to the package's errors at the
// storage edge: a same-org link refused by its composite foreign key is a
// *LinkError, a claim text the org holds is ErrClaimExists, and any other
// CHECK is a *ValidationError naming the constraint.
func classify(err error, op string) error {
	if err == nil {
		return nil
	}
	switch name := orm.ConstraintName(err); name {
	case store.GtmIdeaConstraintCampaignSameOrgFkey, store.GtmItemConstraintCampaignSameOrgFkey, store.GtmAssetConstraintCampaignSameOrgFkey:
		return &LinkError{Field: "campaign_id"}
	case store.GtmItemConstraintIdeaSameOrgFkey:
		return &LinkError{Field: "idea_id"}
	case store.GtmItemConstraintChannelSameOrgFkey:
		return &LinkError{Field: "channel_id"}
	case store.GtmCampaignConstraintStrategySameOrgFkey:
		return &LinkError{Field: "strategy_id"}
	case store.GtmAssetConstraintItemSameOrgFkey:
		return &LinkError{Field: "item_id"}
	case store.GtmChannelConstraintOrgIDKeyKey:
		return ErrChannelExists
	case store.GtmClaimConstraintOrgIDTextNormKey:
		return ErrClaimExists
	default:
		if orm.IsCheckViolation(err) {
			return &ValidationError{Field: name, Reason: "violates a constraint"}
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
