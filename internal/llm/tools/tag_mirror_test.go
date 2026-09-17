// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sort"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/stretchr/testify/assert"
)

// names.AllToolTags is a hand-maintained mirror of the tags this registry
// defines. It cannot import the registry — the names package exists to break
// the cycle between tools and workflow/parser — so the copy is unavoidable and
// the drift has to be caught here, in the one package that can see both.
//
// What drift costs: names.AllToolTags is the validator for tag references in
// workflow YAML. A tag the registry has and the mirror lacks is rejected as a
// typo even though it works; a tag the mirror has and the registry lacks is
// accepted and then silently expands to nothing. Both failures point at the
// workflow author rather than at this list.
func TestAllToolTagsMatchRegistry(t *testing.T) {
	t.Parallel()

	inRegistry := map[string]bool{}
	for _, def := range GetToolRegistry() {
		for _, tag := range def.Tags {
			inRegistry[string(tag)] = true
		}
	}

	// Tags whose members are not in the static registry because they are
	// resolved per request. tag:mcp expands to whatever MCP servers the chat
	// has connected (ExpandToolFilter assigns tagIndex[TagMCP] = mcpToolNames),
	// so no compiled-in tool carries it and its absence here is correct rather
	// than drift.
	dynamic := map[string]bool{string(TagMCP): true}

	inMirror := map[string]bool{}
	for _, tag := range names.AllToolTags {
		inMirror[tag] = true
	}
	for tag := range dynamic {
		inRegistry[tag] = true
	}

	var missingFromMirror, missingFromRegistry []string
	for tag := range inRegistry {
		if !inMirror[tag] {
			missingFromMirror = append(missingFromMirror, tag)
		}
	}
	for tag := range inMirror {
		if !inRegistry[tag] {
			missingFromRegistry = append(missingFromRegistry, tag)
		}
	}
	sort.Strings(missingFromMirror)
	sort.Strings(missingFromRegistry)

	if len(missingFromMirror) > 0 {
		t.Errorf("tags on registry tools but absent from names.AllToolTags: %v\n"+
			"A workflow naming one of these is rejected as an unknown tag. Add them to the mirror.",
			missingFromMirror)
	}
	if len(missingFromRegistry) > 0 {
		t.Errorf("tags in names.AllToolTags that no registry tool carries: %v\n"+
			"A workflow naming one of these passes validation and then expands to zero tools. "+
			"Either tag some tools with it or drop it from the mirror.",
			missingFromRegistry)
	}
}

// Curated bundles are namespaced so that reading a tag tells you whether it is
// a fact about the tool or one product's editorial grouping. This pins the
// convention: anything that is somebody's opinion about which tools go
// together carries a namespace, and nothing bare claims to be a default.
func TestCuratedBundlesAreNamespaced(t *testing.T) {
	t.Parallel()

	// A bare tag named for importance rather than behaviour is the shape that
	// caused the outage: `default` reads as "what an agent obviously gets",
	// which is a question only a product can answer.
	forbidden := map[string]string{
		"default": "a bare `default` cannot be universal — namespace it (coding:default)",
		"plan":    "plan mode is a product's concept — namespace it (coding:plan)",
		"primary": "same class as `default`",
		"basic":   "same class as `default`",
	}

	for _, def := range GetToolRegistry() {
		for _, tag := range def.Tags {
			if why, bad := forbidden[string(tag)]; bad {
				t.Errorf("tool %q carries tag %q: %s", def.Name, tag, why)
			}
		}
	}
}

// TagDescriptions is what the generated tool reference renders, so a tag
// missing from it shows up in the docs as a blank cell, and a stale entry
// shows up as a confident wrong sentence. The doc generator used to keep its
// own copy of this table; that copy had gone stale on one tag and never
// learned about another, which is the argument for deriving it and testing it
// rather than maintaining it twice.
func TestTagDescriptionsAreComplete(t *testing.T) {
	t.Parallel()

	declared := map[ToolTag]bool{}
	for _, def := range GetToolRegistry() {
		for _, tag := range def.Tags {
			declared[tag] = true
		}
	}
	// Resolved per request rather than carried by a compiled-in tool, but it
	// is still a tag an author can write and therefore still needs a meaning.
	declared[TagMCP] = true

	for tag := range declared {
		desc, ok := TagDescriptions[tag]
		assert.True(t, ok, "tag %q has no entry in TagDescriptions — the docs will render a blank cell", tag)
		assert.NotEmpty(t, desc, "tag %q has an empty description", tag)
	}

	for tag := range TagDescriptions {
		assert.True(t, declared[tag],
			"TagDescriptions documents %q, which no tool carries — drop it or tag some tools with it", tag)
	}
}
