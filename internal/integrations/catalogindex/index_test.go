// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// fixtureCatalog is a small catalog built so that each ranking tier has an
// entry that wins it and an entry that only reaches a lower tier.
func fixtureCatalog(t *testing.T) []*reliantv1.IntegrationManifest {
	t.Helper()
	docs := []string{`
id: github
version: 1
display_name: GitHub
description: Act on repositories, issues and pull requests.
icon: github
category: engineering
keywords: [git, code]
connection:
  base_url: https://api.github.com
  auth:
    - delegated: { broker: controlplane-github }
    - api_key: { in: header, name: Authorization, prefix: "Bearer " }
actions:
  - id: issue.create
    display_name: Create issue
    summary: Open a new issue in a repository.
    keywords: [ticket, bug]
    placement: server
    mutates: true
    params: { type: object, properties: { title: { type: string } } }
    request: { method: POST, path: /issues }
    output: { schema: { type: object, properties: { number: { type: integer } } } }
  - id: issue.comment
    display_name: Comment on issue
    summary: Add a comment to an issue or pull request.
    placement: server
    mutates: true
    request: { method: POST, path: /comments }
  - id: pr.get
    display_name: Get pull request
    summary: Read one pull request.
    keywords: [review]
    placement: server
    request: { method: GET, path: /pulls }
`, `
id: slack
version: 1
display_name: Slack
description: Post and read messages in Slack workspaces.
icon: slack
category: communication
keywords: [chat, messaging]
connection:
  base_url: https://slack.com/api
  auth:
    - api_key: { in: header, name: Authorization, prefix: "Bearer " }
actions:
  - id: message.post
    display_name: Post message
    summary: Send a message to a channel.
    keywords: [send, notify]
    placement: server
    mutates: true
    request: { method: POST, path: /chat.postMessage }
  - id: issue.report
    display_name: Report a problem
    summary: File feedback with Slack support.
    placement: server
    mutates: true
    request: { method: POST, path: /feedback }
`, `
id: http
version: 1
display_name: HTTP
description: Make an HTTP request to a public URL.
icon: globe
category: core
keywords: [api, webhook]
connection:
  allow_any_public_host: true
  auth_optional: true
  auth:
    - api_key: { label: API key }
actions:
  - id: request
    display_name: HTTP request
    description: Send an HTTP request and return the status, headers and body. Useful for creating issues in trackers without a manifest.
    placement: server
    mutates: true
    request: { method: "{{ params.method }}", url: "{{ params.url }}" }
`}
	var ms []*reliantv1.IntegrationManifest
	for _, d := range docs {
		m, err := manifest.Parse([]byte(d), manifest.TrustCurated)
		require.NoError(t, err)
		ms = append(ms, m)
	}
	return ms
}

func fixtureIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := Build(fixtureCatalog(t))
	require.NoError(t, err)
	return idx
}

func refs(r *Result) []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, h.Entry.Ref)
	}
	return out
}

func search(t *testing.T, idx *Index, q Query) *Result {
	t.Helper()
	r, err := idx.Search(q)
	require.NoError(t, err)
	return r
}

func TestBuildIndexesEveryActionWithARef(t *testing.T) {
	idx := fixtureIndex(t)
	assert.Equal(t, 6, idx.Len())
	e, ok := idx.Get("github/issue.create@1")
	require.True(t, ok)
	assert.Equal(t, KindAction, e.Kind)
	assert.Equal(t, "Create issue", e.DisplayName)
	assert.Equal(t, "Open a new issue in a repository.", e.Summary)
	assert.Equal(t, []string{manifest.AuthDelegated, manifest.AuthAPIKey}, e.AuthKinds)
	assert.True(t, e.ConnectionRequired)

	h, ok := idx.Get("http/request@1")
	require.True(t, ok)
	assert.False(t, h.ConnectionRequired, "auth_optional means no connection is required")
	assert.Equal(t, "Send an HTTP request and return the status, headers and body.", h.Summary,
		"with no summary, the first sentence of the description stands in")

	_, ok = idx.Get("github/issue.create@2")
	assert.False(t, ok)
}

func TestBuildRefusesDuplicateRefs(t *testing.T) {
	ms := fixtureCatalog(t)
	_, err := Build(append(ms, ms[0]))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate ref")
}

// Today no manifest can declare a trigger (the loader refuses them and
// TriggerSpec is a stub). This pins that the embedded catalog's index is
// actions-only until stream B lands; when it does, this test changes to
// assert the trigger entries instead.
func TestEmbeddedCatalogIsActionsOnlyUntilTriggersLand(t *testing.T) {
	idx, err := Build(catalog.MustBuiltin().Manifests())
	require.NoError(t, err)
	require.Positive(t, idx.Len())
	r := search(t, idx, Query{Kinds: []Kind{KindTrigger}})
	assert.Zero(t, r.Total, "no manifest declares triggers yet")
	all := search(t, idx, Query{PageSize: MaxPageSize})
	for _, h := range all.Hits {
		assert.Equal(t, KindAction, h.Entry.Kind, h.Entry.Ref)
	}
}

// A trigger declared in a manifest gets its own entry and kind, so the
// trigger slot is real even though the embedded catalog has none yet.
func TestTriggerEntriesAreIndexedUnderTheirOwnKind(t *testing.T) {
	ms := fixtureCatalog(t)
	ms[0].Triggers = []*reliantv1.TriggerSpec{{Id: "issue.opened"}}
	idx, err := Build(ms)
	require.NoError(t, err)
	e, ok := idx.Get("github/issue.opened@1")
	require.True(t, ok)
	assert.Equal(t, KindTrigger, e.Kind)

	r := search(t, idx, Query{Text: "issue", Kinds: []Kind{KindTrigger}})
	assert.Equal(t, []string{"github/issue.opened@1"}, refs(r))
	r = search(t, idx, Query{Text: "issue", Kinds: []Kind{KindAction}})
	assert.NotContains(t, refs(r), "github/issue.opened@1")
}

func TestRankingTiers(t *testing.T) {
	idx := fixtureIndex(t)

	t.Run("exact ref beats everything", func(t *testing.T) {
		r := search(t, idx, Query{Text: "github/issue.create@1"})
		require.NotEmpty(t, r.Hits)
		assert.Equal(t, "github/issue.create@1", r.Hits[0].Entry.Ref)
		assert.Equal(t, 1, r.Total, "an exact ref is one word that only that entry contains")
	})

	t.Run("exact id and whole display name are exact matches", func(t *testing.T) {
		r := search(t, idx, Query{Text: "issue.create"})
		assert.Equal(t, "github/issue.create@1", r.Hits[0].Entry.Ref)
		r = search(t, idx, Query{Text: "Post message"})
		assert.Equal(t, "slack/message.post@1", r.Hits[0].Entry.Ref)
	})

	t.Run("prefix beats keyword beats text", func(t *testing.T) {
		// "issue" is:
		//   a display-name word of Create issue / Comment on issue  (prefix tier)
		//   a prefix of slack/issue.report's id                     (prefix tier)
		//   absent from http/request's name and keywords, but in its
		//   description ("creating issues")                          (text tier)
		r := search(t, idx, Query{Text: "issue"})
		got := refs(r)
		require.Len(t, got, 4)
		assert.Equal(t, "http/request@1", got[3], "a description-only hit ranks last")
		assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1", "slack/issue.report@1"}, got[:3])
	})

	t.Run("keyword beats text", func(t *testing.T) {
		// "bug" is a keyword of github/issue.create only.
		r := search(t, idx, Query{Text: "bug"})
		assert.Equal(t, []string{"github/issue.create@1"}, refs(r))
		// "notify" is a keyword of slack/message.post; "send" is a keyword of
		// it too but also a description word of http/request ("Send an HTTP
		// request"), which must rank below the keyword hit.
		r = search(t, idx, Query{Text: "send"})
		assert.Equal(t, []string{"slack/message.post@1", "http/request@1"}, refs(r))
	})

	t.Run("prefix of a word matches", func(t *testing.T) {
		r := search(t, idx, Query{Text: "comm"})
		assert.Equal(t, "github/issue.comment@1", r.Hits[0].Entry.Ref)
	})

	t.Run("every word must match", func(t *testing.T) {
		r := search(t, idx, Query{Text: "github issue"})
		assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1"}, refs(r))
		r = search(t, idx, Query{Text: "slack bug"})
		assert.Empty(t, r.Hits)
	})

	t.Run("integration keyword reaches its actions", func(t *testing.T) {
		r := search(t, idx, Query{Text: "messaging"})
		assert.ElementsMatch(t, []string{"slack/issue.report@1", "slack/message.post@1"}, refs(r))
	})

	t.Run("case and punctuation do not matter", func(t *testing.T) {
		a := refs(search(t, idx, Query{Text: "  CREATE   Issue! "}))
		b := refs(search(t, idx, Query{Text: "create issue"}))
		assert.Equal(t, b, a)
		assert.Equal(t, "github/issue.create@1", a[0])
	})

	t.Run("no match is empty, not an error", func(t *testing.T) {
		r := search(t, idx, Query{Text: "zzzz"})
		assert.Empty(t, r.Hits)
		assert.Zero(t, r.Total)
		assert.Empty(t, r.NextPageToken)
	})
}

func TestConnectedBoostReordersOnlyEqualRelevance(t *testing.T) {
	idx := fixtureIndex(t)
	// Both slack/issue.report and the two github issue actions reach the
	// prefix tier for "issue". Connected to slack, its entry moves first;
	// http/request (text tier, and always connected because it needs no
	// connection) must still rank last.
	r := search(t, idx, Query{Text: "issue", Usable: map[string]bool{"slack": true}})
	got := refs(r)
	assert.Equal(t, "slack/issue.report@1", got[0])
	assert.Equal(t, "http/request@1", got[len(got)-1], "connected must not lift a weaker match")

	r = search(t, idx, Query{Text: "issue", Usable: map[string]bool{"github": true}})
	got = refs(r)
	assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1"}, got[:2])
	for _, h := range r.Hits {
		switch h.Entry.IntegrationID() {
		case "github", "http":
			assert.True(t, h.Connected, h.Entry.Ref)
		case "slack":
			assert.False(t, h.Connected, h.Entry.Ref)
		}
	}
}

func TestEmptyQueryBrowsesByConnectedThenRef(t *testing.T) {
	idx := fixtureIndex(t)
	r := search(t, idx, Query{Usable: map[string]bool{"slack": true}})
	assert.Equal(t, []string{
		// connected: http needs no connection, slack is usable
		"http/request@1", "slack/issue.report@1", "slack/message.post@1",
		// not connected
		"github/issue.comment@1", "github/issue.create@1", "github/pr.get@1",
	}, refs(r))
	assert.Equal(t, 6, r.Total)
}

func TestTieBreakIsStableAndByRef(t *testing.T) {
	idx := fixtureIndex(t)
	first := refs(search(t, idx, Query{Text: "github"}))
	for i := 0; i < 20; i++ {
		assert.Equal(t, first, refs(search(t, idx, Query{Text: "github"})))
	}
	assert.Equal(t, []string{"github/issue.comment@1", "github/issue.create@1", "github/pr.get@1"}, first)
}

func TestFilters(t *testing.T) {
	idx := fixtureIndex(t)

	r := search(t, idx, Query{Category: "communication"})
	assert.ElementsMatch(t, []string{"slack/issue.report@1", "slack/message.post@1"}, refs(r))

	r = search(t, idx, Query{Integration: "github", Text: "issue"})
	assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1"}, refs(r))

	r = search(t, idx, Query{Kinds: []Kind{KindAction}})
	assert.Equal(t, 6, r.Total)
}

// connected_only is per caller: the same query returns different entries for
// a user with a Slack connection and a user with none.
func TestConnectedOnlyFiltersPerCaller(t *testing.T) {
	idx := fixtureIndex(t)

	none := search(t, idx, Query{ConnectedOnly: true})
	assert.Equal(t, []string{"http/request@1"}, refs(none), "only what needs no connection")

	withSlack := search(t, idx, Query{ConnectedOnly: true, Usable: map[string]bool{"slack": true}})
	assert.ElementsMatch(t, []string{"http/request@1", "slack/issue.report@1", "slack/message.post@1"}, refs(withSlack))
	for _, h := range withSlack.Hits {
		assert.True(t, h.Connected)
	}

	issue := search(t, idx, Query{Text: "issue", ConnectedOnly: true, Usable: map[string]bool{"github": true}})
	assert.ElementsMatch(t, []string{"github/issue.comment@1", "github/issue.create@1", "http/request@1"}, refs(issue))
}

func TestCategoryFacetsIgnoreTheCategoryFilter(t *testing.T) {
	idx := fixtureIndex(t)
	r := search(t, idx, Query{Category: "communication"})
	assert.Equal(t, []Facet{
		{Value: "engineering", Count: 3},
		{Value: "communication", Count: 2},
		{Value: "core", Count: 1},
	}, r.CategoryFacets)
	assert.Equal(t, 2, r.Total)

	r = search(t, idx, Query{Text: "issue"})
	assert.Equal(t, []Facet{
		{Value: "engineering", Count: 2},
		{Value: "communication", Count: 1},
		{Value: "core", Count: 1},
	}, r.CategoryFacets)

	r = search(t, idx, Query{ConnectedOnly: true})
	assert.Equal(t, []Facet{{Value: "core", Count: 1}}, r.CategoryFacets, "facets respect the other filters")
}

func TestPaging(t *testing.T) {
	idx := fixtureIndex(t)
	all := refs(search(t, idx, Query{PageSize: MaxPageSize}))
	require.Len(t, all, 6)

	var got []string
	q := Query{PageSize: 4}
	page := search(t, idx, q)
	assert.Len(t, page.Hits, 4)
	assert.Equal(t, 6, page.Total)
	require.NotEmpty(t, page.NextPageToken)
	got = append(got, refs(page)...)

	q.PageToken = page.NextPageToken
	page = search(t, idx, q)
	assert.Len(t, page.Hits, 2)
	assert.Empty(t, page.NextPageToken, "the last page has no token")
	got = append(got, refs(page)...)
	assert.Equal(t, all, got, "pages concatenate to the full ranking with no gap or repeat")

	t.Run("page size defaults and caps", func(t *testing.T) {
		assert.Equal(t, DefaultPageSize, Query{}.pageSize())
		assert.Equal(t, MaxPageSize, Query{PageSize: 10_000}.pageSize())
		assert.Equal(t, DefaultPageSize, Query{PageSize: -3}.pageSize())
	})

	t.Run("a token is bound to its query", func(t *testing.T) {
		first := search(t, idx, Query{PageSize: 2})
		require.NotEmpty(t, first.NextPageToken)
		_, err := idx.Search(Query{PageSize: 2, Text: "issue", PageToken: first.NextPageToken})
		assert.ErrorIs(t, err, ErrInvalidPageToken)
		_, err = idx.Search(Query{PageSize: 2, ConnectedOnly: true, PageToken: first.NextPageToken})
		assert.ErrorIs(t, err, ErrInvalidPageToken)
	})

	t.Run("garbage tokens are refused", func(t *testing.T) {
		for _, tok := range []string{"x", "!!!", "MTA", strings.Repeat("A", 64)} {
			_, err := idx.Search(Query{PageToken: tok})
			assert.ErrorIs(t, err, ErrInvalidPageToken, tok)
		}
	})

	t.Run("an offset past the end is an empty last page", func(t *testing.T) {
		q := Query{PageSize: 3}
		p1 := search(t, idx, q)
		q.PageToken = p1.NextPageToken
		p2 := search(t, idx, q)
		assert.Empty(t, p2.NextPageToken)
		assert.Len(t, p2.Hits, 3)
	})
}

func TestQueryTooLong(t *testing.T) {
	idx := fixtureIndex(t)
	_, err := idx.Search(Query{Text: strings.Repeat("a", MaxQueryLen+1)})
	assert.ErrorIs(t, err, ErrQueryTooLong)
}
