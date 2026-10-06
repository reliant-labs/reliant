// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// IntegrationQuery browses the catalog by integration rather than by entry:
// what a picker shows before the user has typed anything.
type IntegrationQuery struct {
	// Kinds keeps only integrations with an entry of these kinds, and counts
	// only those entries; empty keeps every kind.
	Kinds []Kind
	// Category keeps only integrations in this category.
	Category string
	// PageSize defaults to DefaultPageSize and is capped at MaxPageSize.
	PageSize int
	// PageToken continues a previous IntegrationResult.
	PageToken string
	// Usable is the set of integration ids the caller can use now, as in
	// Query.Usable.
	Usable map[string]bool
}

// IntegrationListing is one integration in a browse.
type IntegrationListing struct {
	// Manifest is the integration's newest indexed version.
	Manifest *reliantv1.IntegrationManifest
	// EntryCount is how many of its entries are of the requested kinds.
	EntryCount int
	// Connected reports whether the caller can use the integration now: one
	// of those entries is connected (see Entry.Connected).
	Connected bool
}

// IntegrationResult is one page of a browse.
type IntegrationResult struct {
	Integrations []IntegrationListing
	// NextPageToken continues the browse; empty on the last page.
	NextPageToken string
	// Total is how many integrations match every filter.
	Total int
	// CategoryFacets count matching integrations per category, applying every
	// filter except Category. Largest first, then by value.
	CategoryFacets []Facet
}

// ListIntegrations groups the index's entries by integration: every
// integration with an entry of q's kinds, those the caller can use now first,
// then by display name. Like Search it is one pass over the entries, so it
// stays exact at catalog scale with no second structure to keep in step.
func (idx *Index) ListIntegrations(q IntegrationQuery) (*IntegrationResult, error) {
	size := clampPageSize(q.PageSize)
	offset, err := decodePageToken(q.PageToken, q.fingerprint())
	if err != nil {
		return nil, err
	}
	kinds := map[Kind]bool{}
	for _, k := range q.Kinds {
		kinds[k] = true
	}

	byID := map[string]*IntegrationListing{}
	for _, e := range idx.entries {
		if len(kinds) > 0 && !kinds[e.Kind] {
			continue
		}
		l, ok := byID[e.IntegrationID()]
		if !ok {
			l = &IntegrationListing{Manifest: e.Manifest}
			byID[e.IntegrationID()] = l
		}
		if e.Manifest.GetVersion() > l.Manifest.GetVersion() {
			l.Manifest = e.Manifest
		}
		l.EntryCount++
		if e.Connected(q.Usable) {
			l.Connected = true
		}
	}

	facetCounts := map[string]int{}
	matches := make([]IntegrationListing, 0, len(byID))
	for _, l := range byID {
		category := l.Manifest.GetCategory()
		facetCounts[category]++
		if q.Category != "" && category != q.Category {
			continue
		}
		matches = append(matches, *l)
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Connected != b.Connected {
			return a.Connected
		}
		an, bn := strings.ToLower(a.Manifest.GetDisplayName()), strings.ToLower(b.Manifest.GetDisplayName())
		if an != bn {
			return an < bn
		}
		return a.Manifest.GetId() < b.Manifest.GetId()
	})

	res := &IntegrationResult{Total: len(matches), CategoryFacets: facets(facetCounts)}
	if offset > len(matches) {
		offset = len(matches)
	}
	end := min(offset+size, len(matches))
	res.Integrations = matches[offset:end]
	if end < len(matches) {
		res.NextPageToken = encodePageToken(end, q.fingerprint())
	}
	return res, nil
}

// fingerprint binds a page token to the browse it was issued for. The leading
// tag keeps a SearchCatalog token from ever decoding as a browse token.
func (q IntegrationQuery) fingerprint() uint32 {
	h := fnv.New32a()
	kinds := make([]string, 0, len(q.Kinds))
	for _, k := range q.Kinds {
		kinds = append(kinds, strconv.Itoa(int(k)))
	}
	sort.Strings(kinds)
	for _, part := range []string{"integrations", strings.Join(kinds, ","), q.Category, strconv.Itoa(clampPageSize(q.PageSize))} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum32()
}
