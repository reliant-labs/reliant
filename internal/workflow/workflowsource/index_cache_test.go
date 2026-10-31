package workflowsource

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

func catalogJSON(t *testing.T, yaml string) *string {
	t.Helper()
	s, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "release", Name: "release", RelativePath: ".reliant/workflows/release.yaml", YAMLContent: yaml}})
	require.NoError(t, err)
	return s
}

// A catalog is indexed once per content: the same bytes, read again (another
// trigger, another child, another request), reuse the index; a sync that
// changes them gets a new one.
func TestCatalogIndex_KeyedByContent(t *testing.T) {
	first, err := catalogIndex(catalogJSON(t, "name: release\nentry: [a]\n"))
	require.NoError(t, err)
	again, err := catalogIndex(catalogJSON(t, "name: release\nentry: [a]\n"))
	require.NoError(t, err)
	require.Same(t, first, again)

	changed, err := catalogIndex(catalogJSON(t, "name: shipit\nentry: [a]\n"))
	require.NoError(t, err)
	require.NotSame(t, first, changed)
	_, err = changed.Lookup("shipit")
	require.NoError(t, err)
	_, err = changed.Lookup("release")
	require.Error(t, err, "the new content's index, not the old one's")
}
