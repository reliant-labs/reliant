// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Every MODEL tag a shipped workflow selects by must exist in the catalog.
//
// Tag resolution is best-match: a selector of [fast, smart] where nothing
// carries `smart` quietly resolves as [fast]. So an unknown tag never errors —
// it just stops meaning anything, and the YAML keeps claiming a preference the
// engine does not honour. build-workflow and pitch-deck shipped `smart` (a tag
// the catalog never had) for exactly that reason: nothing looked.
//
// The tool-tag sibling of this check is TestShippedTagReferencesExist.
func TestShippedModelTagReferencesExist(t *testing.T) {
	t.Parallel()

	known := map[string]bool{}
	for _, tag := range models.MustGetRegistry().ListAllTags() {
		known[tag] = true
	}
	require.NotEmpty(t, known, "the embedded catalog declares no tags")

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(e.Name())
		require.NoError(t, err)
		var doc any
		require.NoError(t, yaml.Unmarshal(data, &doc), e.Name())

		var unknown []string
		for _, tag := range modelSelectorTags(doc) {
			checked++
			if !known[tag] {
				unknown = append(unknown, tag)
			}
		}
		sort.Strings(unknown)
		assert.Empty(t, dedupe(unknown),
			"%s selects a model by tag(s) the catalog does not define: %v\n"+
				"An unknown tag is silently ignored by best-match resolution. Use a tag from "+
				"internal/llm/models/definitions/models.yaml, or add the tag there.",
			e.Name(), dedupe(unknown))
	}
	require.Positive(t, checked, "found no model tag selectors in shipped workflows; the walker is broken")
}

// modelSelectorTags returns the literal tags of every `model: {tags: [...]}`
// in a parsed workflow document. Entries that are CEL expressions rather than
// literal tag names are skipped: they are resolved at run time.
func modelSelectorTags(node any) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if k == "model" {
				if sel, ok := child.(map[string]any); ok {
					if tags, ok := sel["tags"].([]any); ok {
						for _, tag := range tags {
							if s, ok := tag.(string); ok && isLiteralTag(s) {
								out = append(out, s)
							}
						}
					}
				}
			}
			out = append(out, modelSelectorTags(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, modelSelectorTags(child)...)
		}
	}
	return out
}

func isLiteralTag(s string) bool {
	return s != "" && !strings.ContainsAny(s, "{}$. ()")
}
