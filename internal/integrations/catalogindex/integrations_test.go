// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

func integrationIDs(r *IntegrationResult) []string {
	out := make([]string, 0, len(r.Integrations))
	for _, l := range r.Integrations {
		out = append(out, l.Manifest.GetId())
	}
	return out
}

func listIntegrations(t *testing.T, idx *Index, q IntegrationQuery) *IntegrationResult {
	t.Helper()
	r, err := idx.ListIntegrations(q)
	require.NoError(t, err)
	return r
}

// Browsing lists integrations, not entries: one row each, the ones the
// caller can use first, then alphabetical by display name.
func TestListIntegrationsConnectedFirstThenByName(t *testing.T) {
	idx := fixtureIndex(t)

	r := listIntegrations(t, idx, IntegrationQuery{})
	// http takes no connection, so it is usable by everyone.
	assert.Equal(t, []string{"http", "github", "slack"}, integrationIDs(r))
	assert.Equal(t, 3, r.Total)
	assert.Empty(t, r.NextPageToken)

	r = listIntegrations(t, idx, IntegrationQuery{Usable: map[string]bool{"slack": true}})
	assert.Equal(t, []string{"http", "slack", "github"}, integrationIDs(r))
	connected := map[string]bool{}
	for _, l := range r.Integrations {
		connected[l.Manifest.GetId()] = l.Connected
	}
	assert.Equal(t, map[string]bool{"http": true, "slack": true, "github": false}, connected)
}

func TestListIntegrationsCountsEntriesOfTheRequestedKinds(t *testing.T) {
	idx := fixtureIndex(t)
	counts := func(r *IntegrationResult) map[string]int {
		out := map[string]int{}
		for _, l := range r.Integrations {
			out[l.Manifest.GetId()] = l.EntryCount
		}
		return out
	}

	all := listIntegrations(t, idx, IntegrationQuery{Kinds: []Kind{KindAction}})
	assert.Equal(t, map[string]int{"github": 3, "slack": 2, "http": 1}, counts(all))

	// The fixture declares no trigger types, so a trigger browse is empty
	// rather than listing integrations that have nothing to offer.
	triggers := listIntegrations(t, idx, IntegrationQuery{Kinds: []Kind{KindTrigger}})
	assert.Empty(t, triggers.Integrations)
	assert.Zero(t, triggers.Total)
}

func TestListIntegrationsWithTriggersOnlyListsThoseIntegrations(t *testing.T) {
	m, err := manifest.Parse([]byte(triggerFixture), manifest.TrustCurated)
	require.NoError(t, err)
	idx, err := Build(append(fixtureCatalog(t), m))
	require.NoError(t, err)

	r := listIntegrations(t, idx, IntegrationQuery{Kinds: []Kind{KindTrigger}})
	require.Equal(t, []string{"pager"}, integrationIDs(r))
	assert.Equal(t, 1, r.Integrations[0].EntryCount)
	assert.False(t, r.Integrations[0].Connected, "pager needs a connection the caller does not have")

	// With no kind filter it is listed beside the action-only integrations.
	assert.Contains(t, integrationIDs(listIntegrations(t, idx, IntegrationQuery{})), "pager")
}

func TestListIntegrationsCategoryFacetsIgnoreTheCategoryFilter(t *testing.T) {
	idx := fixtureIndex(t)
	r := listIntegrations(t, idx, IntegrationQuery{Category: "communication"})
	assert.Equal(t, []string{"slack"}, integrationIDs(r))
	assert.Equal(t, 1, r.Total)
	// Facets count integrations, not entries: GitHub is one, not three.
	assert.Equal(t, []Facet{
		{Value: "communication", Count: 1},
		{Value: "core", Count: 1},
		{Value: "engineering", Count: 1},
	}, r.CategoryFacets)
}

func TestListIntegrationsPaging(t *testing.T) {
	idx := fixtureIndex(t)
	q := IntegrationQuery{PageSize: 2}
	p1 := listIntegrations(t, idx, q)
	assert.Equal(t, []string{"http", "github"}, integrationIDs(p1))
	assert.Equal(t, 3, p1.Total)
	require.NotEmpty(t, p1.NextPageToken)

	q.PageToken = p1.NextPageToken
	p2 := listIntegrations(t, idx, q)
	assert.Equal(t, []string{"slack"}, integrationIDs(p2))
	assert.Empty(t, p2.NextPageToken)

	t.Run("a token is bound to its browse", func(t *testing.T) {
		_, err := idx.ListIntegrations(IntegrationQuery{PageSize: 2, Category: "core", PageToken: p1.NextPageToken})
		assert.ErrorIs(t, err, ErrInvalidPageToken)
	})

	t.Run("a search token is not a browse token", func(t *testing.T) {
		search := search(t, idx, Query{PageSize: 2})
		require.NotEmpty(t, search.NextPageToken)
		_, err := idx.ListIntegrations(IntegrationQuery{PageSize: 2, PageToken: search.NextPageToken})
		assert.ErrorIs(t, err, ErrInvalidPageToken)
	})
}

// The newest indexed version is what a browse shows: its name, icon and
// category, with every version's entries counted under one row.
func TestListIntegrationsShowsTheNewestVersion(t *testing.T) {
	parse := func(doc string) *reliantv1.IntegrationManifest {
		m, err := manifest.Parse([]byte(doc), manifest.TrustCurated)
		require.NoError(t, err)
		return m
	}
	v1 := parse(`
id: acme
version: 1
display_name: Acme (old)
icon: acme-old
category: crm
connection: { base_url: https://api.acme.example }
actions:
  - { id: contact.get, display_name: Get contact, placement: server, request: { method: GET, path: /c } }
`)
	v2 := parse(`
id: acme
version: 2
display_name: Acme
icon: acme
category: crm
connection: { base_url: https://api.acme.example }
actions:
  - { id: contact.get, display_name: Get contact, placement: server, request: { method: GET, path: /c } }
  - { id: deal.list, display_name: List deals, placement: server, request: { method: GET, path: /d } }
`)
	idx, err := Build([]*reliantv1.IntegrationManifest{v2, v1})
	require.NoError(t, err)

	r := listIntegrations(t, idx, IntegrationQuery{})
	require.Len(t, r.Integrations, 1)
	assert.Equal(t, "Acme", r.Integrations[0].Manifest.GetDisplayName())
	assert.Equal(t, 3, r.Integrations[0].EntryCount)
}

// The embedded catalog is what the builder browses: every integration in it
// is listed, and nothing the index does not hold.
func TestEmbeddedCatalogListsEveryIntegration(t *testing.T) {
	idx := MustBuiltin()
	r := listIntegrations(t, idx, IntegrationQuery{Kinds: []Kind{KindAction}, PageSize: MaxPageSize})
	ids := integrationIDs(r)
	for _, want := range []string{"github", "gmail", "http", "slack", "twilio"} {
		assert.Contains(t, ids, want)
	}
	assert.Equal(t, len(idx.integrations), r.Total, "every embedded integration has an action")
}
