// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every tag a shipped workflow or preset names must exist, and the default
// bundle must expand to something.
//
// THE OUTAGE THESE COME FROM. A field rename in tools_config left one gate
// reading the old name, which read as "no tools", and every agent ran with an
// empty tool list until someone noticed. What made it expensive was not the
// nil check — it was that nothing compared what the shipped YAML SAYS against
// what the engine can actually resolve. The declarations and the registry were
// free to drift, and the only detector was a human reading a chat transcript.
//
// A model handed no tools does not error, which is why that detector is so
// poor: it narrates the call it wanted in prose, so the symptom is text where
// a tool call should be and a turn that ends early. Neither names tools.
//
// These tests close that gap at the level it actually broke: the pairing of
// product YAML with engine capability.

var tagRef = regexp.MustCompile(`tag:[a-zA-Z0-9_:-]+`)

// knownTags is the registry's tags plus the ones resolved per request.
func knownTags(t *testing.T) map[string]bool {
	t.Helper()
	known := map[string]bool{
		// tag:mcp expands to whatever MCP servers a chat has connected, so no
		// compiled-in tool carries it.
		"tag:mcp": true,
	}
	for _, def := range tools.GetToolRegistry() {
		for _, tag := range def.Tags {
			known["tag:"+string(tag)] = true
		}
	}
	return known
}

func TestShippedTagReferencesExist(t *testing.T) {
	t.Parallel()

	known := knownTags(t)

	for _, fs := range []struct {
		name string
		read func() ([]string, func(string) ([]byte, error))
	}{
		{"workflows", func() ([]string, func(string) ([]byte, error)) {
			entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
			require.NoError(t, err)
			var names []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					names = append(names, e.Name())
				}
			}
			return names, builtin.BuiltinWorkflowsFS.ReadFile
		}},
		{"presets", func() ([]string, func(string) ([]byte, error)) {
			entries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
			require.NoError(t, err)
			var names []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					names = append(names, "presets/"+e.Name())
				}
			}
			return names, builtin.BuiltinPresetsFS.ReadFile
		}},
	} {
		names, read := fs.read()
		require.NotEmpty(t, names, "expected to find shipped %s", fs.name)

		for _, name := range names {
			data, err := read(name)
			require.NoError(t, err)

			var unknown []string
			for _, ref := range tagRef.FindAllString(string(data), -1) {
				// Trailing punctuation from prose in comments.
				ref = strings.TrimRight(ref, ":-_")
				if !known[ref] {
					unknown = append(unknown, ref)
				}
			}
			sort.Strings(unknown)
			unknown = dedupe(unknown)

			assert.Empty(t, unknown,
				"%s names tag(s) no tool carries: %v\n"+
					"A tag that does not resolve expands to zero tools, silently. If a tag was "+
					"renamed, update the shipped YAML; if it is new, tag some tools with it.",
				name, unknown)
		}
	}
}

// The coding agent's starting bundle must actually contain tools. This is the
// end of the chain the outage broke: a real tag name, run through the real
// expansion, producing a real non-empty list.
func TestCodingDefaultBundleIsNotEmpty(t *testing.T) {
	t.Parallel()

	expanded := tools.ExpandToolFilter([]string{"tag:" + string(tools.TagCodingDefault)}, nil)

	assert.NotEmpty(t, expanded,
		"tag:%s expanded to nothing — the default coding agent would be handed no tools "+
			"and would narrate its tool calls as prose instead of making them",
		tools.TagCodingDefault)

	// The shell is the one tool whose absence is unmistakable in a coding
	// agent, and it is what the reported failure was trying to call.
	assert.Contains(t, expanded, tools.ShellToolName,
		"the coding bundle must include the shell")
}

// A curated bundle is granted because a workflow named it, never by default.
// If this ever fails, some path has started handing out a bundle nobody asked
// for — which is the shape of the original design mistake.
func TestBundlesAreNotGrantedByDefault(t *testing.T) {
	t.Parallel()

	assert.Empty(t, tools.ExpandToolFilter(nil, nil),
		"an absent tool list must expand to nothing, not to a default bundle")
	assert.Empty(t, tools.ExpandToolFilter([]string{}, nil),
		"an empty tool list must expand to nothing, not to a default bundle")
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
