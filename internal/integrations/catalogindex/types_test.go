// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

func builtin(t *testing.T) *Index {
	t.Helper()
	idx, err := Build(catalog.MustBuiltin().Manifests())
	require.NoError(t, err)
	return idx
}

// A value type is an entry of its own kind, under its qualified ref.
func TestTypeEntriesAreIndexedUnderTheirOwnKind(t *testing.T) {
	idx := builtin(t)
	r := search(t, idx, Query{Kinds: []Kind{KindType}, PageSize: MaxPageSize})
	assert.ElementsMatch(t, []string{"github.repository", "github.label", "github.user", "github.installation"}, refs(r))
	for _, h := range r.Hits {
		e := h.Entry
		assert.Equal(t, KindType, e.Kind)
		assert.Equal(t, "type", e.Kind.String())
		assert.NotNil(t, e.Type)
		assert.NotNil(t, e.ValueSchema(), e.Ref)
		assert.Nil(t, e.PayloadSchema(), e.Ref)
		params, output := e.Schemas()
		assert.Nil(t, params)
		assert.Nil(t, output)
		assert.Empty(t, e.ToolName())
		assert.True(t, e.ConnectionRequired, "%s is listed through a GitHub connection", e.Ref)
	}

	repo, ok := idx.Get("github.repository")
	require.True(t, ok)
	assert.Equal(t, "Repository", repo.DisplayName)
	assert.Equal(t, "string", repo.ValueSchema()["type"])

	// An action or trigger query does not return types.
	for _, k := range []Kind{KindAction, KindTrigger} {
		for _, ref := range refs(search(t, idx, Query{Kinds: []Kind{k}, PageSize: MaxPageSize})) {
			assert.NotContains(t, ref, "github.repository")
		}
	}
}

func TestTypesAreFoundByWhatAUserWouldType(t *testing.T) {
	idx := builtin(t)
	for query, want := range map[string]string{
		"repository":              "github.repository",
		"label":                   "github.label",
		"person":                  "github.user",
		"github app installation": "github.installation",
	} {
		r := search(t, idx, Query{Text: query, Kinds: []Kind{KindType}})
		require.NotEmpty(t, r.Hits, query)
		assert.Equal(t, want, r.Hits[0].Entry.Ref, query)
	}
	// The exact qualified ref is the strongest signal there is.
	r := search(t, idx, Query{Text: "github.repository"})
	require.NotEmpty(t, r.Hits)
	assert.Equal(t, "github.repository", r.Hits[0].Entry.Ref)
}

func TestValueTypesResolveFromTheIndex(t *testing.T) {
	idx := builtin(t)

	schema, ok := idx.ValueSchema("github.repository")
	require.True(t, ok)
	assert.Equal(t, "string", schema["type"])
	assert.Contains(t, schema["pattern"], "/")
	schema["type"] = "mutated"
	again, _ := idx.ValueSchema("github.repository")
	assert.Equal(t, "string", again["type"], "a caller gets its own copy")

	for _, bad := range []string{"github.repo", "repository", "gitlab.repository", "", "github/repository@1", "github.issue.opened"} {
		_, ok := idx.ValueSchema(bad)
		assert.False(t, ok, bad)
	}
	// An action's ref is not a type, whatever it is called.
	_, ok = idx.ValueSchema("github/user.get@1")
	assert.False(t, ok)

	assert.Equal(t, []string{"github.installation", "github.label", "github.repository", "github.user"}, idx.TypeRefs())

	assert.Equal(t, map[string]string{"installation": "github.installation"}, idx.ScopeTypes("github.repository"),
		"a scope is reported by its qualified ref")
	assert.Equal(t, map[string]string{"repository": "github.repository"}, idx.ScopeTypes("github.label"))
	assert.Nil(t, idx.ScopeTypes("github.user"))
	assert.Nil(t, idx.ScopeTypes("github.nope"))
}

// Only the newest manifest of an integration contributes types, so two majors
// do not collide on one qualified ref.
func TestTypesComeFromTheNewestMajorOnly(t *testing.T) {
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	require.NoError(t, err)
	v2 := proto.Clone(m).(*reliantv1.IntegrationManifest)
	v2.Version = 2
	idx, err := Build([]*reliantv1.IntegrationManifest{m, v2})
	require.NoError(t, err)

	e, ok := idx.Get("github.repository")
	require.True(t, ok)
	assert.Equal(t, int32(2), e.Manifest.GetVersion())
	assert.Equal(t, 1, countRef(idx, "github.repository"))
	// Actions are still per major.
	_, ok = idx.Get("github/user.get@1")
	assert.True(t, ok)
	_, ok = idx.Get("github/user.get@2")
	assert.True(t, ok)
}

func countRef(idx *Index, ref string) int {
	n := 0
	for _, e := range idx.entries {
		if e.Ref == ref {
			n++
		}
	}
	return n
}

// A type id and an action or trigger ref can never be the same string.
func TestTypeRefsDoNotCollideWithActionRefs(t *testing.T) {
	idx := builtin(t)
	for _, e := range idx.entries {
		if e.Kind == KindType {
			assert.Equal(t, manifest.TypeRef(e.Manifest, e.Type), e.Ref)
			assert.NotContains(t, e.Ref, "/")
		} else {
			assert.Contains(t, e.Ref, "/")
		}
	}
}
