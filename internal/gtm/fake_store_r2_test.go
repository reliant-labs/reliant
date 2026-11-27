// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"sort"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// The r2/r3 half of fakeStore.

type fakeR2 struct {
	strategies map[string]*store.GtmStrategy
	channels   map[string]*store.GtmChannel
	assets     map[string]*store.GtmAsset
	// seeds counts SeedChannels calls.
	seeds int
}

func (f *fakeStore) r2() *fakeR2 {
	if f.r2s == nil {
		f.r2s = &fakeR2{
			strategies: map[string]*store.GtmStrategy{},
			channels:   map[string]*store.GtmChannel{},
			assets:     map[string]*store.GtmAsset{},
		}
	}
	return f.r2s
}

func (f *fakeStore) ListStrategies(_ context.Context, flt StrategyFilter) ([]*store.GtmStrategy, error) {
	return sortedPage(f.r2().strategies, func(r *store.GtmStrategy) bool {
		return r.OrgID == flt.OrgID && (flt.Status == "" || r.Status == string(flt.Status))
	}, func(r *store.GtmStrategy) string { return r.ID }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) GetStrategy(_ context.Context, orgID, id string) (*store.GtmStrategy, error) {
	r, ok := f.r2().strategies[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) CreateStrategy(_ context.Context, row *store.GtmStrategy) error {
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.r2().strategies[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateStrategy(_ context.Context, row *store.GtmStrategy, expected int32) error {
	cur, ok := f.r2().strategies[row.ID]
	if !ok || cur.OrgID != row.OrgID || cur.Revision != expected {
		return ErrRevisionConflict
	}
	c := *row
	c.Revision = expected + 1
	f.r2().strategies[row.ID] = &c
	return nil
}

func (f *fakeStore) ListChannels(_ context.Context, flt ChannelFilter) ([]*store.GtmChannel, error) {
	var out []*store.GtmChannel
	for _, r := range f.r2().channels {
		if r.OrgID == flt.OrgID && (flt.Status == "" || r.Status == string(flt.Status)) && (flt.Mode == "" || r.Mode == string(flt.Mode)) {
			c := *r
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if flt.Limit > 0 && len(out) > flt.Limit {
		out = out[:flt.Limit]
	}
	return out, nil
}

func (f *fakeStore) CountChannels(_ context.Context, orgID string) (int64, error) {
	var n int64
	for _, r := range f.r2().channels {
		if r.OrgID == orgID {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) GetChannel(_ context.Context, orgID, id string) (*store.GtmChannel, error) {
	r, ok := f.r2().channels[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) channelKeyTaken(orgID, key, except string) bool {
	for _, r := range f.r2().channels {
		if r.OrgID == orgID && r.Key == key && r.ID != except {
			return true
		}
	}
	return false
}

func (f *fakeStore) SeedChannels(_ context.Context, rows []*store.GtmChannel) error {
	f.r2().seeds++
	for _, row := range rows {
		if f.channelKeyTaken(row.OrgID, row.Key, "") {
			continue
		}
		c := *row
		f.r2().channels[row.ID] = &c
	}
	return nil
}

func (f *fakeStore) CreateChannel(_ context.Context, row *store.GtmChannel) error {
	if f.channelKeyTaken(row.OrgID, row.Key, "") {
		return ErrChannelExists
	}
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.r2().channels[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateChannel(_ context.Context, row *store.GtmChannel, expected int32) error {
	cur, ok := f.r2().channels[row.ID]
	if !ok || cur.OrgID != row.OrgID || cur.Revision != expected {
		return ErrRevisionConflict
	}
	if f.channelKeyTaken(row.OrgID, row.Key, row.ID) {
		return ErrChannelExists
	}
	c := *row
	c.Revision = expected + 1
	f.r2().channels[row.ID] = &c
	return nil
}

func (f *fakeStore) CampaignItemCounts(_ context.Context, orgID, campaignID string) ([]ItemCount, error) {
	agg := map[ItemCount]int{}
	for _, r := range f.items {
		if r.OrgID != orgID || deref(r.CampaignID) != campaignID {
			continue
		}
		agg[ItemCount{ChannelID: deref(r.ChannelID), Format: ItemFormat(r.Format), Status: ItemStatus(r.Status)}]++
	}
	var out []ItemCount
	for k, n := range agg {
		k.Count = n
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.ChannelID+string(a.Format)+string(a.Status) < b.ChannelID+string(b.Format)+string(b.Status)
	})
	return out, nil
}

func (f *fakeStore) ListAssets(_ context.Context, flt AssetFilter) ([]*store.GtmAsset, error) {
	return sortedPage(f.r2().assets, func(r *store.GtmAsset) bool {
		return r.OrgID == flt.OrgID && (flt.ItemID == "" || deref(r.ItemID) == flt.ItemID) &&
			(flt.CampaignID == "" || deref(r.CampaignID) == flt.CampaignID)
	}, func(r *store.GtmAsset) string { return r.ID }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) GetAsset(_ context.Context, orgID, id string) (*store.GtmAsset, error) {
	r, ok := f.r2().assets[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) SumAssetBytes(_ context.Context, orgID string, pendingAfter time.Time) (int64, error) {
	var n int64
	for _, r := range f.r2().assets {
		live := r.Status != string(AssetPending) || r.CreatedAt.After(pendingAfter)
		if r.OrgID == orgID && r.SizeBytes != nil && live {
			n += *r.SizeBytes
		}
	}
	return n, nil
}

func (f *fakeStore) CreateAsset(_ context.Context, row *store.GtmAsset) error {
	if row.ObjectKey != assetKey(row.OrgID, row.ID) {
		return &ValidationError{Field: store.GtmAssetConstraintObjectKeyIsItsOwn, Reason: "violates a constraint"}
	}
	c := *row
	f.r2().assets[row.ID] = &c
	return nil
}

func (f *fakeStore) ListExpiredAssets(_ context.Context, orgID string, createdBefore time.Time, limit int) ([]*store.GtmAsset, error) {
	var out []*store.GtmAsset
	for _, r := range f.r2().assets {
		if r.OrgID == orgID && r.Status == string(AssetPending) && !r.CreatedAt.After(createdBefore) {
			c := *r
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) MarkAssetReady(_ context.Context, orgID, id, contentType string, size int64, sum string, createdAfter time.Time) error {
	r, ok := f.r2().assets[id]
	if !ok || r.OrgID != orgID || r.Status != string(AssetPending) || !r.CreatedAt.After(createdAfter) {
		return ErrAssetNotPending
	}
	r.Status, r.ContentType, r.SizeBytes, r.Sha256 = string(AssetReady), contentType, &size, &sum
	return nil
}

func (f *fakeStore) DeletePendingAsset(_ context.Context, orgID, id string, createdBefore time.Time) (bool, error) {
	r, ok := f.r2().assets[id]
	if !ok || r.OrgID != orgID || r.Status != string(AssetPending) || (!createdBefore.IsZero() && r.CreatedAt.After(createdBefore)) {
		return false, nil
	}
	delete(f.r2().assets, id)
	return true, nil
}

func (f *fakeStore) DeleteAsset(_ context.Context, orgID, id string) error {
	if r, ok := f.r2().assets[id]; ok && r.OrgID == orgID {
		delete(f.r2().assets, id)
	}
	return nil
}
