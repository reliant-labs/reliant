// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testdata/demo is a miniature repository root with one provider, "demo": its
// specs, its pinned upstream schema, its hand-written hooks and its catalog
// directory. The files the generator owns in it (zz_projection_generated.go, the
// events/ and *_schema.yaml fragments, the manifest's trigger list) are the
// golden output: the test regenerates into a copy and compares every one.
//
// To update a golden after an intended change:
//
//	go run ./tools/integrationgen -root tools/integrationgen/testdata/demo demo

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}))
}

func demoCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyTree(t, "testdata/demo", root)
	return root
}

func TestGoldenOutputMatchesTheCheckedInTree(t *testing.T) {
	root := demoCopy(t)
	out, err := generate(root, "demo")
	require.NoError(t, err)
	require.NotEmpty(t, out.files)

	for path, got := range out.files {
		want, err := os.ReadFile(filepath.Join("testdata/demo", path))
		require.NoError(t, err, "%s has no golden file", path)
		assert.Equal(t, string(want), string(got), path)
	}
	// And the other way: no generated file in the golden tree that the
	// generator stopped producing.
	for _, dir := range out.owned {
		entries, err := os.ReadDir(filepath.Join("testdata/demo", dir))
		require.NoError(t, err)
		for _, e := range entries {
			rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
			b, _ := os.ReadFile(filepath.Join("testdata/demo", rel))
			if bytes.Contains(b, []byte(generatedMarker)) {
				_, ok := out.files[rel]
				assert.True(t, ok, "%s is golden but no longer generated", rel)
			}
		}
	}
}

func TestGenerationIsIdempotentAndCheckModeSeesDrift(t *testing.T) {
	root := demoCopy(t)
	out, err := generate(root, "demo")
	require.NoError(t, err)
	stale, err := apply(root, out, true)
	require.NoError(t, err)
	assert.Empty(t, stale, "the golden tree is current, so -check finds nothing")

	gen := filepath.Join(root, "internal/integrations/webhook/demo/zz_projection_generated.go")
	require.NoError(t, os.WriteFile(gen, []byte("package demo\n"), 0o644))
	stale, err = apply(root, out, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/integrations/webhook/demo/zz_projection_generated.go"}, stale)

	stale, err = apply(root, out, false)
	require.NoError(t, err)
	assert.Empty(t, stale)
	again, err := generate(root, "demo")
	require.NoError(t, err)
	stale, err = apply(root, again, true)
	require.NoError(t, err)
	assert.Empty(t, stale, "a second run changes nothing")
}

func TestDeletedSpecRemovesItsFragment(t *testing.T) {
	root := demoCopy(t)
	frag := filepath.Join(root, "internal/integrations/catalog/demo/events/orphan.created.yaml")
	require.NoError(t, os.WriteFile(frag, []byte("# "+generatedMarker+"\nid: orphan.created\n"), 0o644))
	hand := filepath.Join(root, "internal/integrations/catalog/demo/events/hand.yaml")
	require.NoError(t, os.WriteFile(hand, []byte("id: hand\n"), 0o644))

	out, err := generate(root, "demo")
	require.NoError(t, err)
	stale, err := apply(root, out, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/integrations/catalog/demo/events/orphan.created.yaml (no spec generates it)"}, stale)

	_, err = apply(root, out, false)
	require.NoError(t, err)
	assert.NoFileExists(t, frag)
	assert.FileExists(t, hand, "a file without the generated marker is not the generator's to remove")
}

// editSpec rewrites a file of the demo copy and reports what generate says.
func editSpec(t *testing.T, rel, old, replacement string) error {
	t.Helper()
	root := demoCopy(t)
	path := filepath.Join(root, rel)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(b), old, "the edit must apply")
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(b), old, replacement, 1)), 0o644))
	_, err = generate(root, "demo")
	return err
}

const (
	demoEvent    = "internal/integrations/webhook/demo/projection/thing.created.yaml"
	demoProvider = "internal/integrations/webhook/demo/projection/provider.yaml"
)

func TestUpstreamIsTheCheck(t *testing.T) {
	t.Run("a kept path upstream does not have", func(t *testing.T) {
		err := editSpec(t, demoEvent, "      draft: boolean\n", "      draft: boolean\n      colour: string\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `field "colour" reads "colour": upstream has no "colour"`)
		assert.Contains(t, err.Error(), "@demo/webhooks-schemas@1.0.0 thing$created")
	})
	t.Run("a kept path whose upstream type the kind cannot read", func(t *testing.T) {
		err := editSpec(t, demoEvent, "      id: integer\n      name:", "      id: integer\n      draft_flag: { kind: integer, from: draft }\n      name:")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `field "draft_flag" (integer) reads "draft"`)
	})
	t.Run("an attribute path upstream does not have", func(t *testing.T) {
		err := editSpec(t, demoProvider, "from: thing.owner.login", "from: thing.owner.handle")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `attribute "owner" reads "thing.owner.handle"`)
	})
	t.Run("an event upstream does not define", func(t *testing.T) {
		err := editSpec(t, demoEvent, "event: thing.created", "event: thing.deleted")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no definition "thing$deleted"`)
	})
	t.Run("a vendored schema that is not the pinned one", func(t *testing.T) {
		err := editSpec(t, demoProvider, "version: 1.0.0\n  file: upstream/schema.json\n  sha256: 27ac", "version: 1.0.0\n  file: upstream/schema.json\n  sha256: 00ac")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pins 00ac")
		assert.Contains(t, err.Error(), "a vendored schema changes together with its pin")
	})
	t.Run("an excused path is not checked", func(t *testing.T) {
		err := editSpec(t, demoEvent, "      draft: boolean\n", "      draft: boolean\n      colour: { kind: string, unchecked: computed by the parser }\n")
		require.NoError(t, err)
	})
}

func TestSpecErrors(t *testing.T) {
	cases := []struct {
		name, file, old, new, want string
	}{
		{"unknown key", demoEvent, "keywords: [thing, new]", "keywords: [thing, new]\nkeywrds: [x]", "keywrds"},
		{"unknown hook", demoEvent, "hook: hasNotes", "hook: hasNothing", `hook "hasNothing" is not a func(any) bool`},
		{"unknown kind", demoEvent, "draft: boolean", "draft: bool", `kind "bool"`},
		{"unknown object", demoEvent, "owner: { kind: object, object: user }", "owner: { kind: object, object: nobody }", `object "nobody" has no objects/nobody.yaml`},
		{"unknown group", demoEvent, "{ group: owned }", "{ group: nobody }", `attribute group "nobody"`},
		{"unknown envelope omit", demoEvent, "keywords: [thing, new]", "keywords: [thing, new]\nomit: [nothing]", `omit "nothing"`},
		{"cap on a number", demoEvent, "id: integer\n      name", "id: { kind: integer, cap: true }\n      name", "cap applies to a string"},
		{"duplicate key", demoEvent, "event: thing.created", "event: thing.created\nsummary: x", "already defined"},
		{"file name is the id", demoEvent, "id: thing.created", "id: thing.made", `file name must be the trigger id it declares, "thing.created"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := editSpec(t, c.file, c.old, c.new)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestYAMLWriterRoundTrips(t *testing.T) {
	m := ymap{
		{"plain", "value"},
		{"needs quoting", "yes"},
		{"colon", "a: b"},
		{"number-ish", "123"},
		{"leading dash", "- x"},
		{"quote", `say "hi"`},
		{"unicode", "… [truncated]"},
		{"long", strings.Repeat("word ", 30)},
		{"list", []any{"a", "b: c", true, 3}},
		{"nested", ymap{{"k", ymap{{"deep", "v"}}}, {"properties", ymap{{"a", ymap{{"type", "string"}}}}}}},
		{"empty", ymap{}},
	}
	text := body(m)
	require.NoError(t, roundTrips(text, m), text)
}

// The real repository's generated files are current: the same check as
// `go run ./tools/integrationgen -check`, run by `go test` so a spec edited
// without regenerating fails the unit lane, not only the generate drift check.
func TestRepositoryGeneratedFilesAreCurrent(t *testing.T) {
	root, err := findRoot()
	require.NoError(t, err)
	out, err := generate(root, "github")
	require.NoError(t, err)
	stale, err := apply(root, out, true)
	require.NoError(t, err)
	assert.Empty(t, stale, "run `task generate:forge` and commit")
}
