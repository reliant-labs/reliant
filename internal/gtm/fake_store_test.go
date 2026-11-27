// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// fakeStore is an in-memory Store with the real one's org scoping and
// revision checks, so the service's rules can be exercised end to end
// without a database. InTx runs fn once and never rolls back.
type fakeStore struct {
	ideas       map[string]*store.GtmIdea
	campaigns   map[string]*store.GtmCampaign
	items       map[string]*store.GtmItem
	positioning map[string]*store.GtmPositioning
	claims      map[string]*store.GtmClaim
	seq         int
	// r2s holds the r2/r3 tables (fake_store_r2_test.go).
	r2s *fakeR2
	// failUpdateItem, when set, is returned by the next UpdateItem.
	failUpdateItem error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		ideas:       map[string]*store.GtmIdea{},
		campaigns:   map[string]*store.GtmCampaign{},
		items:       map[string]*store.GtmItem{},
		positioning: map[string]*store.GtmPositioning{},
		claims:      map[string]*store.GtmClaim{},
	}
}

var _ Store = (*fakeStore)(nil)

func (f *fakeStore) nextID() string {
	f.seq++
	return fmt.Sprintf("id-%03d", f.seq)
}

// InTx refuses a done ctx, as a real transaction cannot begin on one.
func (f *fakeStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

func sortedPage[T any](m map[string]*T, keep func(*T) bool, id func(*T) string, limit, offset int) []*T {
	var out []*T
	for _, v := range m {
		if keep(v) {
			c := *v
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return id(out[i]) < id(out[j]) })
	if offset > len(out) {
		return nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func linkMatches(want *string, got *string) bool {
	switch {
	case want == nil:
		return true
	case *want == "":
		return got == nil
	}
	return got != nil && *got == *want
}

func (f *fakeStore) ListIdeas(_ context.Context, flt IdeaFilter) ([]*store.GtmIdea, error) {
	return sortedPage(f.ideas, func(r *store.GtmIdea) bool {
		return r.OrgID == flt.OrgID && (flt.Kind == "" || r.Kind == string(flt.Kind)) &&
			(flt.Status == "" || r.Status == string(flt.Status)) && linkMatches(flt.CampaignID, r.CampaignID)
	}, func(r *store.GtmIdea) string { return r.ID }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) GetIdea(_ context.Context, orgID, id string) (*store.GtmIdea, error) {
	r, ok := f.ideas[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) CreateIdea(_ context.Context, row *store.GtmIdea) error {
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.ideas[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateIdea(_ context.Context, row *store.GtmIdea, expected int32) error {
	cur, ok := f.ideas[row.ID]
	if !ok || cur.OrgID != row.OrgID || cur.Revision != expected {
		return ErrRevisionConflict
	}
	c := *row
	c.Revision = expected + 1
	f.ideas[row.ID] = &c
	return nil
}

func (f *fakeStore) ListCampaigns(_ context.Context, flt CampaignFilter) ([]*store.GtmCampaign, error) {
	return sortedPage(f.campaigns, func(r *store.GtmCampaign) bool {
		return r.OrgID == flt.OrgID && (flt.Status == "" || r.Status == string(flt.Status)) &&
			(flt.StrategyID == "" || r.StrategyID == flt.StrategyID)
	}, func(r *store.GtmCampaign) string { return r.ID }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) GetCampaign(_ context.Context, orgID, id string) (*store.GtmCampaign, error) {
	r, ok := f.campaigns[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) CreateCampaign(_ context.Context, row *store.GtmCampaign) error {
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.campaigns[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateCampaign(_ context.Context, row *store.GtmCampaign, expected int32) error {
	cur, ok := f.campaigns[row.ID]
	if !ok || cur.OrgID != row.OrgID || cur.Revision != expected {
		return ErrRevisionConflict
	}
	c := *row
	c.Revision = expected + 1
	f.campaigns[row.ID] = &c
	return nil
}

func (f *fakeStore) ListItems(_ context.Context, flt ItemFilter) ([]*store.GtmItem, error) {
	return sortedPage(f.items, func(r *store.GtmItem) bool {
		if r.OrgID != flt.OrgID || !linkMatches(flt.CampaignID, r.CampaignID) {
			return false
		}
		if !linkMatches(flt.ChannelID, r.ChannelID) {
			return false
		}
		if (flt.Format != "" && r.Format != string(flt.Format)) || (flt.Status != "" && r.Status != string(flt.Status)) {
			return false
		}
		if flt.ScheduledFrom != nil && (r.ScheduledAt == nil || r.ScheduledAt.Before(*flt.ScheduledFrom)) {
			return false
		}
		if flt.ScheduledTo != nil && (r.ScheduledAt == nil || !r.ScheduledAt.Before(*flt.ScheduledTo)) {
			return false
		}
		return true
	}, func(r *store.GtmItem) string { return r.ID }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) GetItem(_ context.Context, orgID, id string) (*store.GtmItem, error) {
	r, ok := f.items[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	c.EmailTo = slices.Clone(r.EmailTo)
	return &c, nil
}

func (f *fakeStore) HoldItemSend(_ context.Context, orgID, id string, revision int32, hash string, at time.Time) error {
	r, ok := f.items[id]
	if !ok || r.OrgID != orgID || r.Revision != revision || r.Status != string(ItemApproved) || r.ApprovalHash != "" {
		return ErrRevisionConflict
	}
	r.ApprovalHash, r.UpdatedAt = hash, at
	return nil
}

func (f *fakeStore) ReleaseItemSend(_ context.Context, orgID, id string, revision int32, hash string, at time.Time, invalidate bool) error {
	r, ok := f.items[id]
	if !ok || r.OrgID != orgID || r.Revision != revision || r.Status != string(ItemApproved) || r.ApprovalHash != hash {
		return ErrRevisionConflict
	}
	r.ApprovalHash, r.UpdatedAt = "", at
	if invalidate {
		r.Revision++
	}
	return nil
}

func (f *fakeStore) CreateItem(_ context.Context, row *store.GtmItem) error {
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.items[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateItem(_ context.Context, row *store.GtmItem, expected int32) error {
	if err := f.failUpdateItem; err != nil {
		f.failUpdateItem = nil
		return err
	}
	cur, ok := f.items[row.ID]
	if !ok || cur.OrgID != row.OrgID || cur.Revision != expected {
		return ErrRevisionConflict
	}
	c := *row
	c.Revision = expected + 1
	f.items[row.ID] = &c
	return nil
}

func (f *fakeStore) GetPositioning(_ context.Context, orgID string) (*store.GtmPositioning, error) {
	r, ok := f.positioning[orgID]
	if !ok {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) CreatePositioning(_ context.Context, row *store.GtmPositioning) error {
	if _, ok := f.positioning[row.OrgID]; ok {
		return ErrRevisionConflict
	}
	c := *row
	f.positioning[row.OrgID] = &c
	return nil
}

func (f *fakeStore) UpdatePositioning(_ context.Context, row *store.GtmPositioning, expected int32) error {
	cur, ok := f.positioning[row.OrgID]
	if !ok || cur.Revision != expected {
		return ErrRevisionConflict
	}
	c := *row
	c.Revision = expected + 1
	f.positioning[row.OrgID] = &c
	return nil
}

func (f *fakeStore) ListClaims(_ context.Context, flt ClaimFilter) ([]*store.GtmClaim, error) {
	return sortedPage(f.claims, func(r *store.GtmClaim) bool {
		return r.OrgID == flt.OrgID && (flt.Status == "" || r.Status == string(flt.Status)) &&
			(flt.Category == "" || r.Category == string(flt.Category))
	}, func(r *store.GtmClaim) string { return r.ID }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) GetClaim(_ context.Context, orgID, id string) (*store.GtmClaim, error) {
	r, ok := f.claims[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) CreateClaim(_ context.Context, row *store.GtmClaim) error {
	for _, c := range f.claims {
		if c.OrgID == row.OrgID && c.Text == row.Text {
			return ErrClaimExists
		}
	}
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.claims[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateClaim(_ context.Context, row *store.GtmClaim, expected int32) error {
	cur, ok := f.claims[row.ID]
	if !ok || cur.OrgID != row.OrgID || cur.Revision != expected {
		return ErrRevisionConflict
	}
	c := *row
	c.Text, c.Category = cur.Text, cur.Category
	c.Revision = expected + 1
	f.claims[row.ID] = &c
	return nil
}
