// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/forgepin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var pinRE = regexp.MustCompile(`(?m)^forge_version:\s*(\S+)`)

// fakePinnedForges stands in for the forges projects pin, read from each
// forge.yaml: "unobtainable" cannot be had, "old" has no `project memory`,
// anything else prints a guide naming its version. No pin is the embedded one.
type fakePinnedForges struct {
	mu   sync.Mutex
	runs []string
}

func useFakePinnedForges(t *testing.T) *fakePinnedForges {
	t.Helper()
	f := &fakePinnedForges{}
	prev := pinnedForge
	t.Cleanup(func() { pinnedForge = prev })
	cache := t.TempDir()
	resolve := func(_ context.Context, root, _ string, _ io.Writer) (forgecli.PinnedForge, bool, error) {
		yaml, _ := os.ReadFile(filepath.Join(root, "forge.yaml"))
		m := pinRE.FindSubmatch(yaml)
		switch {
		case m == nil:
			return forgecli.PinnedForge{}, false, nil
		case string(m[1]) == "unobtainable":
			return forgecli.PinnedForge{}, false, errors.New("could not build forge unobtainable: no such host")
		}
		v := string(m[1])
		return forgecli.PinnedForge{Path: "/pinned/" + v, Key: "pin:" + v, Want: "forge_version " + v}, true, nil
	}
	run := func(bin, dir string, args ...string) ([]byte, error) {
		f.mu.Lock()
		f.runs = append(f.runs, filepath.Base(bin)+" "+strings.Join(args, " "))
		f.mu.Unlock()
		if strings.Join(args, " ") != "project memory --json" || filepath.Base(bin) == "old" {
			return nil, errors.New(`Error: unknown command "memory" for "forge project"`)
		}
		return json.Marshal(map[string]string{
			"forge_version": filepath.Base(bin),
			"memory":        "# guide of forge " + filepath.Base(bin) + " for " + filepath.Base(dir) + "\n" + strings.Repeat("rule\n", 1500),
		})
	}
	f.restart(resolve, run, cache)
	return f
}

// restart is a new process: fresh memory, the same disk cache.
func (f *fakePinnedForges) restart(resolve func(context.Context, string, string, io.Writer) (forgecli.PinnedForge, bool, error), run func(string, string, ...string) ([]byte, error), cache string) {
	pinnedForge = &forgepin.Source{Resolve: resolve, Run: run, CacheDir: func() string { return cache }}
}

func forgeDir(t *testing.T, name, pin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	yaml := "name: " + name + "\nmodule_path: example.com/" + name + "\n"
	if pin != "" {
		yaml += "forge_version: " + pin + "\n"
	}
	writeRepoFile(t, dir, "forge.yaml", yaml)
	return dir
}

// A project pinned to another forge gets THAT forge's guide, asked once and
// cached per pinned version — never the embedded forge's.
func TestForgeMemory_APinnedProjectGetsThePinnedForgesGuide(t *testing.T) {
	f := useFakePinnedForges(t)
	dir := forgeDir(t, "cp", "v1.0.4")

	got := string(projectMemoryWithForgeFramework(dir, []byte("user notes")))
	require.True(t, strings.HasPrefix(got, "# guide of forge v1.0.4 for cp\n"), "got:\n%.200s", got)
	assert.True(t, strings.HasSuffix(got, "\n\nuser notes"))
	embedded, err := forgecli.RenderProjectMemory(dir)
	require.NoError(t, err)
	assert.NotContains(t, got, string(embedded[:200]), "the embedded forge's guide was injected")

	_ = projectMemoryWithForgeFramework(dir, nil)
	resolve, run := pinnedForge.Resolve, pinnedForge.Run
	f.restart(resolve, run, pinnedForge.CacheDir())
	again := string(projectMemoryWithForgeFramework(dir, []byte("user notes")))
	assert.Equal(t, got, again)
	assert.Equal(t, []string{"v1.0.4 project memory --json"}, f.runs, "the pinned forge was asked again for its guide")

	// The guide names the project: a renamed project is rendered again.
	writeRepoFile(t, dir, "forge.yaml", "name: renamed\nmodule_path: example.com/cp\nforge_version: v1.0.4\n")
	assert.Len(t, f.runs, 1)
	_ = projectMemoryWithForgeFramework(dir, nil)
	assert.Len(t, f.runs, 2, "a changed forge.yaml reused the old guide")
}

// No pin: the forge compiled into reliant renders it, as before.
func TestForgeMemory_AnUnpinnedProjectGetsTheEmbeddedGuide(t *testing.T) {
	f := useFakePinnedForges(t)
	dir := forgeDir(t, "plain", "")
	want, err := forgecli.RenderProjectMemory(dir)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(projectMemoryWithForgeFramework(dir, nil)))
	assert.Empty(t, f.runs)
}

// A pin that cannot be obtained, or that cannot print its guide, gets an
// explicit note — never the embedded forge's guide.
func TestForgeMemory_AnUnavailablePinGetsANoteNotTheWrongGuide(t *testing.T) {
	useFakePinnedForges(t)
	for pin, cause := range map[string]string{
		"unobtainable": "no such host",
		"old":          `unknown command "memory"`,
	} {
		dir := forgeDir(t, "p-"+pin, pin)
		embedded, err := forgecli.RenderProjectMemory(dir)
		require.NoError(t, err)

		got := string(projectMemoryWithForgeFramework(dir, []byte("user notes")))
		assert.True(t, strings.HasPrefix(got, "# Forge framework guide: unavailable"), "%s:\n%.300s", pin, got)
		assert.Contains(t, got, cause)
		assert.Contains(t, got, "skill load forge")
		assert.Contains(t, got, "user notes", "the user's own memory must survive")
		assert.NotContains(t, got, string(embedded[:200]), "%s: the embedded forge's guide was injected", pin)
	}
}

// Repos pinned to different forges each carry their own guide; repos on one
// forge share it, as before.
func TestCollectRepoMemories_EachForgeCarriesItsOwnGuide(t *testing.T) {
	useFakePinnedForges(t)
	root := t.TempDir()
	for n, pin := range map[string]string{"a": "v1.0.4", "b": "v1.0.5", "c": "v1.0.4", "d": ""} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, n, ".git"), 0o755))
		yaml := "name: " + n + "\n"
		if pin != "" {
			yaml += "forge_version: " + pin + "\n"
		}
		writeRepoFile(t, filepath.Join(root, n), "forge.yaml", yaml)
	}
	mem, _ := collectRepoMemories(root)
	assert.Contains(t, string(mem["a"]), "# guide of forge v1.0.4 for a")
	assert.Contains(t, string(mem["b"]), "# guide of forge v1.0.5 for b", "b was pointed at another forge's guide")
	assert.Contains(t, string(mem["c"]), "`a`", "c shares a's forge, so it points at a's guide")
	assert.Less(t, len(mem["c"]), 1000)
	assert.Contains(t, string(mem["d"]), "Use forge skills", "d has no pin: the embedded guide")
}
