// Copyright (c) 2025 Reliant Labs
package db

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cache's reads against a database, through db.Repo, are tested in
// internal/store (catalog_column_cache_test.go), on its faster harness.

// The cache holds at most its byte budget, least recently used out first,
// keeps no value larger than a quarter of it, and serves a value only at the
// version it was read at.
func TestCatalogColumnCacheEvictsByBytesAndMatchesVersions(t *testing.T) {
	t.Parallel()
	key := func(p string) catalogColumnKey {
		return catalogColumnKey{column: "machine.workflows", projectID: p, daemonID: "d"}
	}
	entry := int64(128 + len("v1") + 1000)
	c := newCatalogColumnCache(4 * entry)
	for _, p := range []string{"a", "b", "c", "d"} {
		c.add(key(p), "v1", strp(strings.Repeat("x", 1000)))
	}
	_, ok := c.get(key("a"), "v1") // a is now the most recently used
	require.True(t, ok)
	c.add(key("e"), "v1", strp(strings.Repeat("x", 1000)))
	for p, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true, "e": true} {
		_, ok := c.get(key(p), "v1")
		assert.Equal(t, want, ok, "project %s", p)
	}
	assert.LessOrEqual(t, c.bytes, c.maxBytes)

	_, ok = c.get(key("a"), "v2")
	assert.False(t, ok, "a value is served only at the version it was read at")

	c.add(key("huge"), "v1", strp(strings.Repeat("x", int(entry)*2)))
	_, ok = c.get(key("huge"), "v1")
	assert.False(t, ok, "a value over a quarter of the cache is not kept")

	c.add(key("a"), "v2", nil)
	got, ok := c.get(key("a"), "v2")
	assert.True(t, ok)
	assert.Nil(t, got, "a NULL column is kept as NULL")
	_, ok = c.get(key("a"), "v1")
	assert.False(t, ok, "and replaces the older version")
}
