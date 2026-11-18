// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discoverForTest(t *testing.T, root string) repoDiscoverResponse {
	t.Helper()
	payload, err := json.Marshal(repoDiscoverRequest{Path: root})
	require.NoError(t, err)
	out, err := handleRepoDiscover(context.Background(), payload)
	require.NoError(t, err)
	var resp repoDiscoverResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	return resp
}

// The forge project name is the key the web joins a Reliant project to its
// control-plane environments on, so discovery must carry it — and a forge.yaml
// forge itself would reject must never cost the project its repo discovery.
func TestRepoDiscoverReportsForgeProjectName(t *testing.T) {
	cases := []struct {
		name      string
		forgeYAML *string
		wantForge bool
		wantName  string
	}{
		{name: "no forge.yaml", forgeYAML: nil, wantForge: false, wantName: ""},
		{name: "named", forgeYAML: ptr("# manifest\nname: hounders\nmodule_path: github.com/x/y\n"), wantForge: true, wantName: "hounders"},
		{name: "name is trimmed", forgeYAML: ptr("name: \"  barksocial  \"\n"), wantForge: true, wantName: "barksocial"},
		{name: "no name key", forgeYAML: ptr("module_path: github.com/x/y\n"), wantForge: true, wantName: ""},
		{name: "unparseable", forgeYAML: ptr("name: [unterminated\n\t:::"), wantForge: true, wantName: ""},
		{name: "nested name is not the project name", forgeYAML: ptr("frontends:\n  - name: web\n"), wantForge: true, wantName: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.forgeYAML != nil {
				require.NoError(t, os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(*tc.forgeYAML), 0o644))
			}
			resp := discoverForTest(t, root)
			assert.Equal(t, tc.wantForge, resp.HasForge)
			assert.Equal(t, tc.wantName, resp.ForgeProjectName)
		})
	}
}

func ptr(s string) *string { return &s }

// A scan that cannot finish inside the command's deadline answers before it,
// with what it found and the flag that says it is partial, instead of being
// cut off by the deadline with no answer at all.
func TestRepoDiscoverAnswersPartialBeforeItsDeadline(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api", ".git"), 0o755))
	payload, err := json.Marshal(repoDiscoverRequest{Path: root})
	require.NoError(t, err)

	// Out of time before the walk starts: the answer is "partial", not an
	// error and not an empty list presented as complete.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	out, err := handleRepoDiscover(ctx, payload)
	require.NoError(t, err)
	var resp repoDiscoverResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.True(t, resp.Truncated)
	assert.NotEmpty(t, resp.TruncatedReason)

	// With time to finish it is whole, and says nothing about truncation.
	whole := discoverForTest(t, root)
	assert.False(t, whole.Truncated)
	require.Len(t, whole.Discovered, 1)
	assert.Equal(t, "api", whole.Discovered[0].RelativePath)
}

func TestAnswerByLeavesAMargin(t *testing.T) {
	assert.True(t, answerBy(context.Background()).IsZero(), "no deadline, no bound")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	assert.Equal(t, time.Second, deadline.Sub(answerBy(ctx)), "a second's margin at most")

	// A short budget keeps a fifth of what is left as margin: at most 100ms
	// of 500ms, however late this line runs, and never past the deadline.
	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	d2, _ := short.Deadline()
	margin := d2.Sub(answerBy(short))
	assert.Greater(t, margin, time.Duration(0))
	assert.LessOrEqual(t, margin, 100*time.Millisecond, "a fifth of a short budget")
}

// discoverForgeAppsForTest runs repo.discover asking for forge apps, as a
// server that knows the field does, and returns the raw answer too.
func discoverForgeAppsForTest(t *testing.T, root string) (repoDiscoverResponse, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(repoDiscoverRequest{Path: root, ForgeApps: true})
	require.NoError(t, err)
	out, err := handleRepoDiscover(context.Background(), payload)
	require.NoError(t, err)
	var resp repoDiscoverResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	var raw map[string]any
	require.NoError(t, json.Unmarshal(out, &raw))
	return resp, raw
}

// writeTree writes each file of files (path relative to root -> content), and
// makes each of gitDirs a checkout (a .git directory).
func writeTree(t *testing.T, root string, gitDirs []string, files map[string]string) {
	t.Helper()
	for _, dir := range gitDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir), ".git"), 0o755))
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

// A project shaped like this workspace: one root holding three repos
// (control-plane, forge, reliant), each a forge app at its root, plus the
// forge.yaml files that are NOT apps (a workflow preset, a dependency's) and
// one real nested app. Before, the project mapped to at most ONE forge name,
// read from the project root, which here has no forge.yaml at all.
func TestRepoDiscoverFindsEveryForgeAppInAWorkspace(t *testing.T) {
	root := t.TempDir()
	manifest := func(name string) string {
		return "name: " + name + "\nmodule_path: github.com/reliant-labs/" + name + "\nforge_version: v1.6.0\n"
	}
	writeTree(t, root, []string{"", "control-plane", "forge", "reliant"}, map[string]string{
		"reliant.md":                      "# workspace\n",
		"control-plane/forge.yaml":        manifest("control-plane"),
		"forge/forge.yaml":                manifest("forge"),
		"forge/node_modules/x/forge.yaml": manifest("not-an-app"),
		"reliant/forge.yaml":              manifest("reliant"),
		"reliant/internal/workflow/builtin/presets/forge.yaml": "name: forge\ntag: agent\n",
		"reliant/frontends/site/forge.yaml":                    "name: reliant-site\nforge_version: v1.6.0\n",
	})

	resp, raw := discoverForgeAppsForTest(t, root)
	assert.Equal(t, true, raw["forge_apps_scanned"], "the answer says the walk ran")
	assert.False(t, resp.HasForge, "the workspace root has no forge.yaml: is_forge is unchanged")

	got := map[string][]repoDiscoverForgeApp{}
	for _, d := range resp.Discovered {
		assert.False(t, d.ForgeAppsTruncated, d.RelativePath)
		got[d.RelativePath] = d.ForgeApps
	}
	assert.Equal(t, map[string][]repoDiscoverForgeApp{
		"control-plane": {{Subpath: "", Name: "control-plane"}},
		"forge":         {{Subpath: "", Name: "forge"}},
		"reliant":       {{Subpath: "", Name: "reliant"}, {Subpath: "frontends/site", Name: "reliant-site"}},
	}, got)
}

// A server older than the walk never asks: the answer then carries no forge
// apps and no forge_apps_scanned, which a newer server reads as unknown. The
// answer is otherwise exactly what it was.
func TestRepoDiscoverWalksForForgeAppsOnlyWhenAsked(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, []string{"api"}, map[string]string{"api/forge.yaml": "name: api\nforge_version: v1.6.0\n"})

	payload, err := json.Marshal(map[string]any{"path": root})
	require.NoError(t, err)
	out, err := handleRepoDiscover(context.Background(), payload)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "forge_apps")

	resp := discoverForTest(t, root)
	require.Len(t, resp.Discovered, 1)
	assert.Nil(t, resp.Discovered[0].ForgeApps)
	assert.False(t, resp.ForgeAppsScanned)
}

// The forge-app walk answers by the command's deadline like the repo scan:
// out of time, each repo says its list is partial rather than "no apps".
func TestRepoDiscoverForgeAppsAnswerPartialBeforeTheDeadline(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, []string{"api"}, map[string]string{"api/forge.yaml": "name: api\nforge_version: v1.6.0\n"})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	discovered := []repoDiscoverFound{{RelativePath: "api", Name: "api"}}
	findForgeApps(ctx, root, discovered)
	assert.True(t, discovered[0].ForgeAppsTruncated, "a walk that did not finish is partial, not empty")
	assert.Empty(t, discovered[0].ForgeApps)

	// A repo that cannot be walked at all is partial too: unknown, not none.
	gone := []repoDiscoverFound{{RelativePath: "gone", Name: "gone"}}
	findForgeApps(context.Background(), root, gone)
	assert.True(t, gone[0].ForgeAppsTruncated)
}
