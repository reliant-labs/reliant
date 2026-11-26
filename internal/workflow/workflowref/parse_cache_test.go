// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// countParses makes parses empty for the test and counts the parses it does
// not save.
func countParses(t *testing.T) *int {
	t.Helper()
	saved, savedParse := parses, parseWorkflow
	parses = &parseCache{items: map[[sha256.Size]byte]*parsedEntry{}}
	n := 0
	parseWorkflow = func(data []byte) (*reliantv1.Workflow, error) {
		n++
		return wfyaml.ParseWorkflow(data)
	}
	t.Cleanup(func() { parses, parseWorkflow = saved, savedParse })
	return &n
}

// A definition resolved again is parsed once, and each resolution owns what
// it gets: an edit to one never reaches the next.
func TestResolveParsesEachDefinitionOnce(t *testing.T) {
	n := countParses(t)
	src := Sources{User: func(string) ([]byte, error) { return []byte(wf("deploy")), nil }}

	first, err := Resolve("deploy", src)
	require.NoError(t, err)
	fresh, err := wfyaml.ParseWorkflow([]byte(wf("deploy")))
	require.NoError(t, err)
	assert.True(t, proto.Equal(fresh, first.Workflow), "a parse through the cache is the parse")

	first.Workflow.Name = "edited"
	first.Workflow.Nodes[0].Id = "edited"

	second, err := Resolve("deploy", src)
	require.NoError(t, err)
	assert.Equal(t, 1, *n, "the same bytes parse once")
	assert.True(t, proto.Equal(fresh, second.Workflow), "an earlier caller's edit must not reach a later one")
	assert.NotSame(t, first.Workflow, second.Workflow)

	second.Workflow.Name = "edited again"
	third, err := Resolve("deploy", src)
	require.NoError(t, err)
	assert.Equal(t, "deploy", third.Workflow.GetName(), "nor a hit's edit")
}

// One changed byte is another definition, parsed afresh.
func TestResolveReparsesChangedDefinition(t *testing.T) {
	n := countParses(t)
	body := wf("deploy")
	src := Sources{User: func(string) ([]byte, error) { return []byte(body), nil }}

	_, err := Resolve("deploy", src)
	require.NoError(t, err)
	body = wf("deplox")
	got, err := Resolve("deploy", src)
	require.NoError(t, err)
	assert.Equal(t, 2, *n)
	assert.Equal(t, "deplox", got.Workflow.GetName())
}

// A definition that does not parse is not cached: each resolution reports
// the parser's own error.
func TestResolveDoesNotCacheParseFailures(t *testing.T) {
	n := countParses(t)
	src := Sources{User: func(string) ([]byte, error) { return []byte("name: [unclosed\n"), nil }}

	_, err1 := Resolve("deploy", src)
	_, err2 := Resolve("deploy", src)
	require.Error(t, err1)
	require.Error(t, err2)
	assert.Equal(t, err1.Error(), err2.Error())
	assert.Equal(t, 2, *n)
	assert.Empty(t, parses.items)
}

// A hit is decided by the bytes, not the digest that found the entry: an
// entry under data's key that was parsed from other bytes is not returned.
func TestParseCacheComparesBytesOnHit(t *testing.T) {
	c := &parseCache{items: map[[sha256.Size]byte]*parsedEntry{}}
	data := []byte(wf("deploy"))
	c.items[sha256.Sum256(data)] = &parsedEntry{src: wf("other"), wf: &reliantv1.Workflow{Name: "other"}, size: 1}
	got, err := c.parse(data)
	require.NoError(t, err)
	assert.Equal(t, "deploy", got.GetName())
}

// The cache holds at most parseCacheEntries definitions and parseCacheBytes
// of their source, oldest out first.
func TestParseCacheIsBounded(t *testing.T) {
	c := &parseCache{items: map[[sha256.Size]byte]*parsedEntry{}}
	entry := &parsedEntry{wf: &reliantv1.Workflow{}, size: 1}
	for i := 0; i < parseCacheEntries+10; i++ {
		c.add(sha256.Sum256([]byte{byte(i), byte(i >> 8)}), entry)
	}
	assert.Len(t, c.items, parseCacheEntries)
	assert.Len(t, c.order, parseCacheEntries)
	_, kept := c.items[sha256.Sum256([]byte{0, 0})]
	assert.False(t, kept, "the oldest goes first")

	big := &parsedEntry{wf: &reliantv1.Workflow{}, size: parseCacheBytes / 2}
	c.add(sha256.Sum256([]byte("a")), big)
	c.add(sha256.Sum256([]byte("b")), big)
	c.add(sha256.Sum256([]byte("c")), big)
	assert.LessOrEqual(t, c.bytes, parseCacheBytes)
	_, keptA := c.items[sha256.Sum256([]byte("a"))]
	assert.False(t, keptA, "the byte bound evicts too")

	c.add(sha256.Sum256([]byte("huge")), &parsedEntry{wf: &reliantv1.Workflow{}, size: parseCacheBytes + 1})
	_, keptHuge := c.items[sha256.Sum256([]byte("huge"))]
	assert.False(t, keptHuge, "a definition over the whole budget is never kept")
}

// BenchmarkParseWorkflowCached compares a fresh parse of the largest builtin
// with a hit (hash + clone).
func BenchmarkParseWorkflowCached(b *testing.B) {
	data, err := BuiltinYAML("forge-one-shot")
	require.NoError(b, err)
	b.Run("parse", func(b *testing.B) {
		for range b.N {
			if _, err := wfyaml.ParseWorkflow(data); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		c := &parseCache{items: map[[sha256.Size]byte]*parsedEntry{}}
		if _, err := c.parse(data); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for range b.N {
			if _, err := c.parse(data); err != nil {
				b.Fatal(err)
			}
		}
	})
}
