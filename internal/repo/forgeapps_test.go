// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile writes content to root/rel, creating its directories.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// forgeManifestYAML is a forge project manifest naming name, as `forge
// project new` writes one.
func forgeManifestYAML(name string) string {
	return fmt.Sprintf("# Forge project manifest\nname: %s\nmodule_path: example.com/%s\nforge_version: v1.6.0\n", name, name)
}

func TestFindForgeApps_RootAndNestedApps(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))
	writeFile(t, repoRoot, "forge.yaml", forgeManifestYAML("shop"))
	// A static site: forge_version only, no Go module.
	writeFile(t, repoRoot, "sites/landing/forge.yaml", "name: landing\nforge_version: v1.0.15\n")
	// An older manifest with neither key, beside its deploy/kcl tree.
	writeFile(t, repoRoot, "services/api/forge.yaml", "name: shop-api\n")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "services/api/deploy/kcl"), 0o755))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.False(t, scan.Truncated, scan.TruncatedReason)
	assert.Equal(t, []ForgeApp{
		{Subpath: "", Name: "shop"},
		{Subpath: "services/api", Name: "shop-api"},
		{Subpath: "sites/landing", Name: "landing"},
	}, scan.Apps)
}

// A file called forge.yaml is not always a forge app: this workspace's own
// reliant repo has a workflow preset by that name, and dependency trees,
// generated code and fixtures carry them too.
func TestFindForgeApps_IgnoresWhatIsNotAnApp(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "forge.yaml", forgeManifestYAML("reliant"))
	writeFile(t, repoRoot, "internal/workflow/builtin/presets/forge.yaml",
		"name: forge\ndescription: Balanced general-purpose agent\ntag: agent\n")
	writeFile(t, repoRoot, "node_modules/pkg/forge.yaml", forgeManifestYAML("dep"))
	writeFile(t, repoRoot, "vendor/x/forge.yaml", forgeManifestYAML("vendored"))
	writeFile(t, repoRoot, "gen/forge.yaml", forgeManifestYAML("generated"))
	writeFile(t, repoRoot, "internal/cli/testdata/forge.yaml", forgeManifestYAML("fixture"))
	writeFile(t, repoRoot, ".reliant/forge.yaml", forgeManifestYAML("hidden"))
	writeFile(t, repoRoot, "nameless/forge.yaml", "forge_version: v1.0.0\n")
	writeFile(t, repoRoot, "broken/forge.yaml", "name: [unterminated\n")
	// A nested checkout is its own repository; its apps are reported on it.
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "third_party/lib/.git"), 0o755))
	writeFile(t, repoRoot, "third_party/lib/forge.yaml", forgeManifestYAML("lib"))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.False(t, scan.Truncated)
	assert.Equal(t, []ForgeApp{{Subpath: "", Name: "reliant"}}, scan.Apps)
}

// The root forge.yaml counts whenever it names something, exactly as
// projects.is_forge has always read it.
func TestFindForgeApps_RootNeedsOnlyAName(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "forge.yaml", "name: '  barksocial  '\n")
	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Equal(t, []ForgeApp{{Subpath: "", Name: "barksocial"}}, scan.Apps)
}

func TestFindForgeApps_DepthBound(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "a/b/c/d/forge.yaml", forgeManifestYAML("four-deep"))
	writeFile(t, repoRoot, "a/b/c/d/e/forge.yaml", forgeManifestYAML("five-deep"))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Equal(t, []ForgeApp{{Subpath: "a/b/c/d", Name: "four-deep"}}, scan.Apps,
		"an app at depth %d is found and one deeper is not", ForgeAppMaxDepth)
	assert.False(t, scan.Truncated, "the depth bound is the walk's shape, not an early stop")

	scan, err = FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{MaxDepth: 2})
	require.NoError(t, err)
	assert.Empty(t, scan.Apps)
}

func TestFindForgeApps_EntryBudgetStopsAndSaysSo(t *testing.T) {
	repoRoot := t.TempDir()
	for i := range 20 {
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, fmt.Sprintf("dir%02d", i)), 0o755))
	}
	writeFile(t, repoRoot, "zz/forge.yaml", forgeManifestYAML("late"))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{MaxEntries: 5})
	require.NoError(t, err)
	assert.True(t, scan.Truncated)
	assert.Contains(t, scan.TruncatedReason, "5 entries")
	assert.Empty(t, scan.Apps, "the walk stopped long before zz/")
}

func TestFindForgeApps_AppCapStopsAndSaysSo(t *testing.T) {
	repoRoot := t.TempDir()
	for i := range MaxForgeAppsPerRepo + 3 {
		writeFile(t, repoRoot, fmt.Sprintf("apps/a%03d/forge.yaml", i), forgeManifestYAML(fmt.Sprintf("a%03d", i)))
	}
	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Len(t, scan.Apps, MaxForgeAppsPerRepo)
	assert.True(t, scan.Truncated)
	assert.Contains(t, scan.TruncatedReason, "forge apps")
}

func TestFindForgeApps_DeadlineStopsAndSaysSo(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "forge.yaml", forgeManifestYAML("shop"))
	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{Deadline: time.Now().Add(-time.Second)})
	require.NoError(t, err)
	assert.True(t, scan.Truncated)
	assert.Equal(t, "it ran out of time", scan.TruncatedReason)
}

func TestFindForgeApps_DoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	writeFile(t, outside, "forge.yaml", forgeManifestYAML("elsewhere"))
	writeFile(t, outside, "app/forge.yaml", forgeManifestYAML("elsewhere-app"))

	repoRoot := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(repoRoot, "linked")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "forge.yaml"), filepath.Join(repoRoot, "forge.yaml")))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Empty(t, scan.Apps)
}

func TestFindForgeApps_MissingRootIsAnError(t *testing.T) {
	_, err := FindForgeApps(context.Background(), filepath.Join(t.TempDir(), "gone"), ForgeAppOptions{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "repository path"), err.Error())
}
