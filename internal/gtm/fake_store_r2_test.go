// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// The r2/r3 half of fakeStore.

type fakeR2 struct {
	imports    map[string]*store.GtmLibraryImport
	prunes     map[string]*store.GtmLibraryPruneRequest
	strategies map[string]*store.GtmStrategy
	channels   map[string]*store.GtmChannel
	assets     map[string]*store.GtmAsset
	library    map[string]*store.GtmStrategyLibrary
	// seeds counts SeedChannels calls.
	seeds int
}

func (f *fakeStore) r2() *fakeR2 {
	if f.r2s == nil {
		f.r2s = &fakeR2{
			imports:    map[string]*store.GtmLibraryImport{},
			prunes:     map[string]*store.GtmLibraryPruneRequest{},
			strategies: map[string]*store.GtmStrategy{},
			channels:   map[string]*store.GtmChannel{},
			assets:     map[string]*store.GtmAsset{},
			library:    map[string]*store.GtmStrategyLibrary{},
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

func (f *fakeStore) ListLibrary(_ context.Context, flt LibraryFilter) ([]*store.GtmStrategyLibrary, error) {
	var out []*store.GtmStrategyLibrary
	for _, r := range f.r2().library {
		if r.OrgID != flt.OrgID ||
			(flt.Category != "" && r.Category != string(flt.Category)) ||
			(flt.Depth != "" && r.Depth != string(flt.Depth)) ||
			(flt.Stage != "" && r.Stage != flt.Stage) ||
			(flt.Cost != "" && r.Cost != flt.Cost) ||
			(flt.Difficulty != "" && r.Difficulty != flt.Difficulty) ||
			(flt.Fit != "" && !slices.Contains(r.Fits, flt.Fit)) ||
			(flt.Keys != nil && !slices.Contains(flt.Keys, r.Key)) {
			continue
		}
		if q := strings.ToLower(flt.Query); q != "" &&
			!strings.Contains(strings.ToLower(r.Name), q) && !strings.Contains(strings.ToLower(r.SummaryMd), q) {
			continue
		}
		c := *r
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Key < b.Key
	})
	if flt.Offset > len(out) {
		return nil, nil
	}
	out = out[flt.Offset:]
	if flt.Limit > 0 && len(out) > flt.Limit {
		out = out[:flt.Limit]
	}
	return out, nil
}

func (f *fakeStore) GetLibraryEntry(_ context.Context, orgID, key string) (*store.GtmStrategyLibrary, error) {
	for _, r := range f.r2().library {
		if r.OrgID == orgID && r.Key == key {
			c := *r
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) CreateLibraryEntry(_ context.Context, row *store.GtmStrategyLibrary) error {
	if row.ID == "" {
		row.ID = f.nextID()
	}
	c := *row
	f.r2().library[row.ID] = &c
	return nil
}

func (f *fakeStore) UpdateLibraryEntry(_ context.Context, row *store.GtmStrategyLibrary) error {
	cur, ok := f.r2().library[row.ID]
	if !ok || cur.OrgID != row.OrgID {
		return ErrNotFound
	}
	c := *row
	c.Key = cur.Key
	f.r2().library[row.ID] = &c
	return nil
}

func (f *fakeStore) PruneLibrary(_ context.Context, orgID string, keep []string) (int64, error) {
	var n int64
	for id, r := range f.r2().library {
		if r.OrgID == orgID && !slices.Contains(keep, r.Key) {
			delete(f.r2().library, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) TouchLibraryEntries(_ context.Context, orgID string, keys []string, rev, by string, at time.Time) error {
	for _, r := range f.r2().library {
		if r.OrgID == orgID && slices.Contains(keys, r.Key) {
			r.SourceRevision, r.ImportedBy, r.ImportedAt = rev, by, at
		}
	}
	return nil
}

func (f *fakeStore) LibraryKeysNotIn(_ context.Context, orgID string, keep []string) ([]string, error) {
	var out []string
	for _, r := range f.r2().library {
		if r.OrgID == orgID && !slices.Contains(keep, r.Key) {
			out = append(out, r.Key)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeStore) PruneLibraryKeys(_ context.Context, orgID string, keys []string, before time.Time) (int64, error) {
	var n int64
	for id, r := range f.r2().library {
		if r.OrgID == orgID && slices.Contains(keys, r.Key) && !r.ImportedAt.After(before) {
			delete(f.r2().library, id)
			n++
		}
	}
	return n, nil
}

// newestPage is sortedPage by created_at, newest first.
func newestPage[T any](m map[string]*T, keep func(*T) bool, created func(*T) time.Time, limit, offset int) []*T {
	var out []*T
	for _, v := range m {
		if keep(v) {
			c := *v
			out = append(out, &c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return created(out[i]).After(created(out[j])) })
	if offset > len(out) {
		return nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (f *fakeStore) CreateLibraryImport(_ context.Context, row *store.GtmLibraryImport) error {
	if row.CredentialKind == string(CredentialToken) && row.Pruned != 0 {
		return &ValidationError{Field: store.GtmLibraryImportConstraintTokenNeverPrunes, Reason: "violates a constraint"}
	}
	if row.PruneRequestID != nil {
		if r, ok := f.r2().prunes[*row.PruneRequestID]; !ok || r.OrgID != row.OrgID {
			return &LinkError{Field: "prune_request_id"}
		}
	}
	c := *row
	f.r2().imports[row.ID] = &c
	return nil
}

func (f *fakeStore) ListLibraryImports(_ context.Context, flt LibraryImportFilter) ([]*store.GtmLibraryImport, error) {
	return newestPage(f.r2().imports, func(r *store.GtmLibraryImport) bool { return r.OrgID == flt.OrgID },
		func(r *store.GtmLibraryImport) time.Time { return r.CreatedAt }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) CreatePruneRequest(_ context.Context, row *store.GtmLibraryPruneRequest) error {
	if len(row.RemoveKeys) == 0 {
		return &ValidationError{Field: "remove_keys", Reason: "violates a constraint"}
	}
	c := *row
	c.RemoveKeys = slices.Clone(row.RemoveKeys)
	f.r2().prunes[row.ID] = &c
	return nil
}

func (f *fakeStore) GetPruneRequest(_ context.Context, orgID, id string) (*store.GtmLibraryPruneRequest, error) {
	r, ok := f.r2().prunes[id]
	if !ok || r.OrgID != orgID {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeStore) ListPruneRequests(_ context.Context, flt PruneRequestFilter) ([]*store.GtmLibraryPruneRequest, error) {
	return newestPage(f.r2().prunes, func(r *store.GtmLibraryPruneRequest) bool {
		if r.OrgID != flt.OrgID {
			return false
		}
		stale := r.Status == string(PruneRequestPending) && !r.CreatedAt.After(flt.ExpiredAtOrBefore)
		switch flt.Status {
		case "":
			return true
		case PruneRequestPending:
			return r.Status == string(PruneRequestPending) && !stale
		case PruneRequestExpired:
			return r.Status == string(PruneRequestExpired) || stale
		}
		return r.Status == string(flt.Status)
	}, func(r *store.GtmLibraryPruneRequest) time.Time { return r.CreatedAt }, flt.Limit, flt.Offset), nil
}

func (f *fakeStore) DecidePruneRequest(_ context.Context, orgID, id string, status PruneRequestStatus, by string, at time.Time) error {
	r, ok := f.r2().prunes[id]
	if !ok || r.OrgID != orgID || r.Status != string(PruneRequestPending) {
		return ErrPruneRequestNotPending
	}
	r.Status = string(status)
	if status != PruneRequestExpired {
		r.DecidedBy, r.DecidedAt = &by, &at
	}
	return nil
}
