// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/forgepin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePinnedForge stands in for the forge a project pins: resolve says which
// one (or why it cannot be had), run plays its `skill list --json
// --with-bodies`, and counts the calls.
type fakePinnedForge struct {
	mu         sync.Mutex
	resolveErr error
	unpinned   bool
	runs       []string
	resolves   int
	// restart stands in for a new process: fresh memory, the same disk cache.
	restart func()
	// cache is forgepin's disk cache directory.
	cache string
}

func useFakePinnedForge(t *testing.T, f *fakePinnedForge) {
	t.Helper()
	prev := pinnedForge
	cache := t.TempDir()
	f.cache = cache
	t.Cleanup(func() {
		pinnedForge = prev
		clearPinnedCatalog()
	})
	resolve := func(_ context.Context, _, command string, _ io.Writer) (forgecli.PinnedForge, bool, error) {
		f.mu.Lock()
		f.resolves++
		f.mu.Unlock()
		if f.resolveErr != nil {
			return forgecli.PinnedForge{}, false, f.resolveErr
		}
		if f.unpinned {
			return forgecli.PinnedForge{}, false, nil
		}
		return forgecli.PinnedForge{Path: "/pinned/forge", Key: "pin:v1.0.4", Want: "forge_version v1.0.4"}, true, nil
	}
	run := func(bin, _ string, args ...string) ([]byte, error) {
		f.mu.Lock()
		f.runs = append(f.runs, strings.Join(args, " "))
		f.mu.Unlock()
		switch {
		case strings.Join(args, " ") == "skill list --json --with-bodies":
			return []byte(`[
  {"path": "forge", "name": "forge", "description": "v1.0.4 start here", "scope": "forge", "body": "# forge as forge v1.0.4 renders it\n"},
  {"path": "db", "name": "db", "description": "v1.0.4 db", "scope": "forge", "emit": "both", "body": "# db as forge v1.0.4 renders it\n"},
  {"path": "only-in-v104", "name": "only-in-v104", "description": "a skill only the pin ships", "scope": "forge", "body": "# only-in-v104 as forge v1.0.4 renders it\n"},
  {"path": "general-agent", "name": "general-agent", "description": "methodology", "scope": "forge", "emit": "general", "body": "# general-agent as forge v1.0.4 renders it\n"},
  {"path": "mine", "name": "mine", "description": "a user skill, read where it is", "scope": "user"}
]`), nil
		}
		return nil, errors.New("unexpected " + strings.Join(args, " "))
	}
	f.restart = func() {
		clearPinnedCatalog()
		pinnedForge = forgepin.New(forgepin.Deps{Resolve: resolve, Run: run, CacheDir: func() string { return cache }})
	}
	f.restart()
}

// clearPinnedCatalog forgets every parsed pinned catalog, as a new process would.
func clearPinnedCatalog() {
	pinnedForgeCache.Range(func(k, _ any) bool { pinnedForgeCache.Delete(k); return true })
}

func pinnedProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: p\nforge_version: v1.0.4\n"), 0o644))
	return dir
}

func paths(defs []Definition) map[string]Definition {
	out := map[string]Definition{}
	for _, d := range defs {
		out[d.SkillPath] = d
	}
	return out
}

// Inside a project pinned to another forge, the catalog is that forge's:
// its list, its bodies — and nothing the embedded forge ships instead.
func TestPinnedProject_SkillsComeFromThePinnedForge(t *testing.T) {
	isolateHome(t)
	f := &fakePinnedForge{}
	useFakePinnedForge(t, f)
	dir := pinnedProject(t)

	defs := paths(discoverForgeSkills(dir, "", false))
	require.Contains(t, defs, "forge/only-in-v104", "a skill only the pinned forge ships is missing")
	require.Contains(t, defs, "db", "an emit:both skill surfaces at its bare path")
	require.Contains(t, defs, "general-agent")
	assert.Equal(t, "v1.0.4 db", defs["db"].Description)
	assert.NotContains(t, defs, "forge/mine", "the pinned listing's user skills are read from disk, not from it")
	// A skill the embedded forge ships and the pin does not is not shown.
	for p := range defs {
		if strings.HasPrefix(p, "forge/") && p != "forge/only-in-v104" && p != "forge/forge" {
			t.Errorf("%s surfaced from the embedded forge", p)
		}
	}
	assert.Contains(t, defs["forge"].Body, "forge/only-in-v104", "the namespace map lists the pinned forge's skills")

	loaded, err := loadForgeDefinition(defs["forge/only-in-v104"])
	require.NoError(t, err)
	assert.Equal(t, "# only-in-v104 as forge v1.0.4 renders it\n", loaded.Body)

	// Cached per pinned forge: in memory, and on disk across a restart.
	_ = discoverForgeSkills(dir, "", false)
	_, _ = loadForgeDefinition(defs["forge/only-in-v104"])
	f.restart()
	_ = discoverForgeSkills(dir, "", false)
	_, _ = loadForgeDefinition(defs["forge/only-in-v104"])
	assert.Equal(t, []string{"skill list --json --with-bodies"}, f.runs, "the pinned forge was asked again for what it already said")
	// One resolution per process: the restart asked again.
	assert.Equal(t, 2, f.resolves, "which forge the project pins was resolved more than once per process")
}

// A disk cache an earlier release wrote, of `skill list --json` with no
// bodies, under the old name, is not read: the catalog is asked for again,
// with bodies, and every listed skill loads.
func TestPinnedProject_MetadataOnlyCacheFromAnEarlierReleaseIsNotRead(t *testing.T) {
	isolateHome(t)
	f := &fakePinnedForge{}
	useFakePinnedForge(t, f)
	dir := pinnedProject(t)

	sum := sha256.Sum256([]byte("pin:v1.0.4")) // forgepin's per-pin directory
	old := filepath.Join(f.cache, hex.EncodeToString(sum[:12]))
	require.NoError(t, os.MkdirAll(old, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(old, "skills-list.json"), []byte(`[
  {"path": "forge", "name": "forge", "description": "v1.0.4 start here", "scope": "forge"},
  {"path": "only-in-v104", "name": "only-in-v104", "description": "a skill only the pin ships", "scope": "forge"}
]`), 0o644))

	defs := paths(discoverForgeSkills(dir, "", false))
	require.Contains(t, defs, "forge/only-in-v104")
	loaded, err := loadForgeDefinition(defs["forge/only-in-v104"])
	require.NoError(t, err, "a skill listed by the old metadata-only cache cannot load its body")
	assert.Equal(t, "# only-in-v104 as forge v1.0.4 renders it\n", loaded.Body)
	assert.Equal(t, []string{"skill list --json --with-bodies"}, f.runs)

	// And the catalog it fetched is what the next process reads.
	f.restart()
	defs = paths(discoverForgeSkills(dir, "", false))
	loaded, err = loadForgeDefinition(defs["forge/only-in-v104"])
	require.NoError(t, err)
	assert.Equal(t, "# only-in-v104 as forge v1.0.4 renders it\n", loaded.Body)
	assert.Equal(t, []string{"skill list --json --with-bodies"}, f.runs, "the refreshed catalog was not kept on disk")
}

// With no pin (or the pin is the embedded forge), nothing changes.
func TestUnpinnedProject_SkillsComeFromTheEmbeddedForge(t *testing.T) {
	isolateHome(t)
	f := &fakePinnedForge{unpinned: true}
	useFakePinnedForge(t, f)
	defs := paths(discoverForgeSkills(pinnedProject(t), "", false))
	assert.Contains(t, defs, "forge/forge")
	assert.NotContains(t, defs, "forge/only-in-v104")
	assert.Empty(t, f.runs)
}

// A pin that cannot be obtained is never answered with the embedded forge's
// framework skills: the namespace says why, and the methodology skills a
// preset preloads still load.
func TestPinnedProject_UnobtainablePinSaysSoAndServesNoOtherForge(t *testing.T) {
	isolateHome(t)
	f := &fakePinnedForge{resolveErr: errors.New("could not build forge v1.0.4: no such host")}
	useFakePinnedForge(t, f)
	dir := pinnedProject(t)

	defs := paths(discoverForgeSkills(dir, "", false))
	require.Contains(t, defs, "forge")
	assert.Contains(t, defs["forge"].Body, "no such host")
	assert.Contains(t, defs["forge"].Body, "unavailable")
	for p, d := range defs {
		if strings.HasPrefix(p, "forge/") {
			t.Errorf("%s served from the embedded forge although the project pins another", p)
		}
		if p != "forge" && d.SkillPath == "general-agent" {
			_, err := loadForgeDefinition(d)
			assert.NoError(t, err, "a methodology skill stopped loading")
		}
	}
	require.Contains(t, defs, "general-agent", "methodology skills presets preload must keep loading")

	_, err := loadForgeDefinition(Definition{Scope: defs["forge"].Scope, Path: forgeSyntheticPath(dir, "db"), SkillPath: "forge/db"})
	require.Error(t, err, "a framework skill loaded from the embedded forge")
}
