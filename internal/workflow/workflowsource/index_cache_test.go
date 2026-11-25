package workflowsource

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
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

// A catalog string hashed once answers its key from recent after, whether
// it is the same string or another with the same bytes; a string with other
// bytes, even of the same length, is hashed for its own key.
func TestCatalogIndexKey_RecentStringsAnswerWithoutRehashing(t *testing.T) {
	c := &indexCache{items: map[[sha256.Size]byte]*workflowref.Index{}}
	a := strings.Repeat("a", 1<<10)
	key := c.keyOf(a)
	require.Equal(t, sha256.Sum256([]byte(a)), key)
	require.Equal(t, key, c.keyOf(a))
	require.Equal(t, key, c.keyOf(strings.Clone(a)), "same bytes in another allocation")
	b := strings.Repeat("b", 1<<10)
	require.Equal(t, sha256.Sum256([]byte(b)), c.keyOf(b), "same length, other bytes")
	require.Len(t, c.recent, 2)
	require.Equal(t, b, c.recent[0].json, "newest first")

	for i := range 2 * recentCatalogs {
		c.keyOf(fmt.Sprintf("catalog-%d", i))
	}
	require.Len(t, c.recent, recentCatalogs, "bounded")
}
