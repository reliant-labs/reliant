// Copyright (c) 2025 Reliant Labs
package serveraction

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

func TestParseName(t *testing.T) {
	for _, tc := range []struct {
		uses string
		want Name
	}{
		{"reliant/pr.next@1", Name{"pr", "next", 1}},
		{"reliant/pr.needs_person@1", Name{"pr", "needs_person", 1}},
		{"reliant/pr_record.get@1", Name{"pr_record", "get", 1}},
		{"reliant/recurring.output@2", Name{"recurring", "output", 2}},
		{"  reliant/queue.enqueue@1 ", Name{"queue", "enqueue", 1}},
	} {
		got, err := ParseName(tc.uses)
		require.NoError(t, err, tc.uses)
		assert.Equal(t, tc.want, got, tc.uses)
	}
	for _, uses := range []string{
		"", "pr.next@1", "github/pr.next@1", "reliant/", "reliant/pr.next", "reliant/pr@1", "reliant/.next@1",
		"reliant/pr.@1", "reliant/PR.next@1", "reliant/pr.Next@1", "reliant/pr.next@0", "reliant/pr.next@01",
		"reliant/pr.next@-1", "reliant/pr.next@1.0", "reliant/pr.next.now@1", "reliant/1pr.next@1", "reliant/pr-x.next@1",
	} {
		_, err := ParseName(uses)
		assert.Error(t, err, "%q must not parse", uses)
	}
}

func TestIsRefIsTheNamespaceAlone(t *testing.T) {
	assert.True(t, IsRef("reliant/pr.next@1"))
	assert.True(t, IsRef(" reliant/whatever"), "a malformed reliant/ name is still a server action, refused as one")
	assert.False(t, IsRef("http/request@1"))
	assert.False(t, IsRef("reliantx/pr.next@1"))
}

// The reliant/ namespace is the server actions' alone: an integration with
// that id would make a `uses:` ambiguous between the two executors.
func TestNoIntegrationClaimsTheReliantNamespace(t *testing.T) {
	for _, m := range catalog.MustBuiltin().Manifests() {
		assert.NotEqual(t, "reliant", m.GetId(), "integration manifest %q claims the server actions' namespace", m.GetId())
	}
}

func TestBuiltinCatalogCompiles(t *testing.T) {
	_, err := builtin()
	require.NoError(t, err)
	for _, name := range Builtin().Names() {
		spec, err := Builtin().Resolve(name)
		require.NoError(t, err)
		assert.NotEmpty(t, spec.Description, "%s needs a description", name)
	}
}

func TestNewCatalogRefusesBadDeclarations(t *testing.T) {
	_, err := NewCatalog(Spec{Name: "reliant/pr.Next@1"})
	assert.Error(t, err, "malformed name")
	_, err = NewCatalog(Spec{Name: " reliant/pr.next@1"})
	assert.Error(t, err, "non-canonical name")
	_, err = NewCatalog(Spec{Name: "reliant/pr.next@1"}, Spec{Name: "reliant/pr.next@1"})
	assert.Error(t, err, "duplicate")
	_, err = NewCatalog(Spec{Name: "reliant/pr.next@1", Params: map[string]any{"type": 7}})
	assert.Error(t, err, "schema that does not compile")
}

func TestCatalogResolve(t *testing.T) {
	c, err := NewCatalog(Spec{Name: "reliant/test.echo@1", Description: "echo"})
	require.NoError(t, err)
	spec, err := c.Resolve("reliant/test.echo@1")
	require.NoError(t, err)
	assert.Equal(t, "echo", spec.Description)
	_, err = c.Resolve("reliant/test.echo@2")
	assert.ErrorIs(t, err, ErrUnknown)
	assert.Contains(t, err.Error(), "unknown server action")
	_, err = c.Resolve("reliant/Test.echo@1")
	assert.NotErrorIs(t, err, ErrUnknown, "a malformed name says so, not that it is unknown")
	assert.Equal(t, []string{"reliant/test.echo@1"}, c.Names())
}
