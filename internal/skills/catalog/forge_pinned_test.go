// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePinnedForge stands in for the forge a project pins: resolve says which
// one (or why it cannot be had), run plays its `skill list --json` and
// `skill load`, and counts the calls.
type fakePinnedForge struct {
	mu         sync.Mutex
	resolveErr error
	unpinned   bool
	runs       []string
	resolves   int
	// withBodies: the pinned forge has `skill list --with-bodies`.
	withBodies bool
}

func useFakePinnedForge(t *testing.T, f *fakePinnedForge) {
	t.Helper()
	prevResolve, prevRun, prevDir := resolvePinnedForge, runPinnedForge, pinnedSkillCacheDir
	cache := t.TempDir()
	t.Cleanup(func() {
		resolvePinnedForge, runPinnedForge, pinnedSkillCacheDir = prevResolve, prevRun, prevDir
		pinnedForgeCache.Range(func(k, _ any) bool { pinnedForgeCache.Delete(k); return true })
		pinnedResolutions.Range(func(k, _ any) bool { pinnedResolutions.Delete(k); return true })
	})
	pinnedSkillCacheDir = func() string { return cache }
	resolvePinnedForge = func(_ context.Context, _, command string, _ io.Writer) (forgecli.PinnedForge, bool, error) {
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
	runPinnedForge = func(bin, _ string, args ...string) ([]byte, error) {
		f.mu.Lock()
		f.runs = append(f.runs, strings.Join(args, " "))
		f.mu.Unlock()
		switch {
		case strings.Join(args, " ") == "skill list --json --with-bodies" && f.withBodies:
			return []byte(`[{"path": "only-in-v104", "name": "only-in-v104", "description": "a skill only the pin ships", "scope": "forge", "body": "# only-in-v104, listed with its body\n"}]`), nil
		case strings.Join(args, " ") == "skill list --json":
			return []byte(`[
  {"path": "forge", "name": "forge", "description": "v1.0.4 start here", "scope": "forge"},
  {"path": "db", "name": "db", "description": "v1.0.4 db", "scope": "forge", "emit": "both"},
  {"path": "only-in-v104", "name": "only-in-v104", "description": "a skill only the pin ships", "scope": "forge"},
  {"path": "general-agent", "name": "general-agent", "description": "methodology", "scope": "forge", "emit": "general"},
  {"path": "mine", "name": "mine", "description": "a user skill, read where it is", "scope": "user"}
]`), nil
		case len(args) == 3 && args[0] == "skill" && args[1] == "load":
			return []byte("# " + args[2] + " as forge v1.0.4 renders it\n"), nil
		}
		return nil, errors.New("unexpected " + strings.Join(args, " "))
	}
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
	pinnedForgeCache.Range(func(k, _ any) bool { pinnedForgeCache.Delete(k); return true })
	_ = discoverForgeSkills(dir, "", false)
	_, _ = loadForgeDefinition(defs["forge/only-in-v104"])
	assert.Equal(t, []string{"skill list --json --with-bodies", "skill list --json", "skill load only-in-v104"}, f.runs, "the pinned forge was asked again for what it already said")
	assert.Equal(t, 1, f.resolves, "which forge the project pins was resolved again within the TTL")
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

// The pinned forge names itself as an agent types it, so a body says
// `reliant forge …` as the embedded forge's did.
func TestExecPinnedForge_NamesItselfAsReliantForge(t *testing.T) {
	if os.Getenv("CATALOG_ARGV0_HELPER") == "1" {
		return
	}
	out, err := execPinnedForgeWith(t, os.Args[0], "-test.run=^TestArgv0Helper$")
	require.NoError(t, err)
	assert.Equal(t, reliantForgeName(), strings.TrimSpace(string(out)))
}

func execPinnedForgeWith(t *testing.T, bin string, args ...string) ([]byte, error) {
	t.Helper()
	t.Setenv("CATALOG_ARGV0_HELPER", "1")
	t.Setenv("FORGE_VERSION_DELEGATED", "1")
	return execPinnedForge(bin, t.TempDir(), args...)
}

// TestArgv0Helper is not a test: re-executed above, it prints its argv[0] and
// whether a hand-off marker reached it.
func TestArgv0Helper(t *testing.T) {
	if os.Getenv("CATALOG_ARGV0_HELPER") != "1" {
		return
	}
	if _, ok := os.LookupEnv("FORGE_VERSION_DELEGATED"); ok {
		os.Stdout.WriteString("marker leaked\n")
	}
	os.Stdout.WriteString(os.Args[0] + "\n")
	os.Exit(0)
}

// A pinned forge that lists every body in one call is asked once, for all of
// them.
func TestPinnedProject_ACatalogWithBodiesIsReadInOneCall(t *testing.T) {
	isolateHome(t)
	f := &fakePinnedForge{withBodies: true}
	useFakePinnedForge(t, f)
	dir := pinnedProject(t)

	defs := paths(discoverForgeSkills(dir, "", true))
	require.Contains(t, defs, "forge/only-in-v104")
	assert.Equal(t, "# only-in-v104, listed with its body\n", defs["forge/only-in-v104"].Body)
	assert.Equal(t, []string{"skill list --json --with-bodies"}, f.runs)
}
