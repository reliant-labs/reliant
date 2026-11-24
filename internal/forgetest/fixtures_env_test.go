package forgetest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixture mode (frontends/web/src/lib/fixturesConfig.ts) turns the production
// web build into a page that serves sample data and reaches no backend. Its
// switch is runtime config: FIXTURES_SCENARIO / FIXTURES_READ_ONLY in config.js,
// rendered from `fixtures = forge.Fixtures {...}` on a frontend (forge#2719) or
// set by hand in runtime_config. Only the hosted preview env, deploy/kcl/preview
// (phase 2, P2.3), may ever set them: on any other env they would show
// app.reliantlabs.io's visitors sample data.
//
// Prod's own config.js is rendered by control-plane (its deploy/kcl), which
// these tests do not see; the post-merge check of the PR that added fixture
// mode covers it.

// fixturesEnv is the one env allowed to declare fixtures.
const fixturesEnv = "preview"

// What declares fixture mode in KCL source: forge's field, or its two keys.
var fixturesDecl = regexp.MustCompile(`\bFIXTURES_(SCENARIO|READ_ONLY)\b|\bforge\.Fixtures\b|^\s*fixtures\s*=`)

func kclRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	return root
}

// envOf is the env directory a deploy/kcl file belongs to ("" for shared files).
func envOf(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// Every .k file under deploy/kcl, read by this process (so Go's test cache sees
// each one): no env but preview mentions fixture mode.
func TestOnlyThePreviewEnvDeclaresFixtures(t *testing.T) {
	dir := filepath.Join(kclRoot(t), "deploy", "kcl")
	var scanned int
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".k" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		scanned++
		if envOf(rel) == fixturesEnv {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			code, _, _ := strings.Cut(line, "#")
			assert.False(t, fixturesDecl.MatchString(code),
				"deploy/kcl/%s:%d declares fixture mode (%q); only deploy/kcl/%s may", filepath.ToSlash(rel), i+1, strings.TrimSpace(line), fixturesEnv)
		}
		return nil
	}))
	require.Greater(t, scanned, 0, "no .k files under deploy/kcl")
}

// Each env as forge renders it: no FIXTURES_* key in any env's output but
// preview's. The source scan above cannot see a key a shared module computes.
func TestNoRenderedEnvButPreviewCarriesFixtures(t *testing.T) {
	forge := Pinned(t) // Use the same KCL evaluator as env build, in the full lane.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root := kclRoot(t)

	entries, err := os.ReadDir(filepath.Join(root, "deploy", "kcl"))
	require.NoError(t, err)
	var rendered int
	for _, e := range entries {
		main := filepath.Join("deploy", "kcl", e.Name(), "main.k")
		if !e.IsDir() || e.Name() == fixturesEnv {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, main)); err != nil {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			cmd := forge.Command(t.Context(), "kcl", "eval", main, "-S", "output")
			cmd.Dir = root
			out, err := cmd.Output()
			require.NoError(t, err, "render %s: %s", e.Name(), out)
			assert.NotRegexp(t, `FIXTURES_(SCENARIO|READ_ONLY)`, string(out), "env %s renders a fixture-mode key", e.Name())
		})
		rendered++
	}
	require.Greater(t, rendered, 0, "no env under deploy/kcl has a main.k")
}

func TestEnvOf(t *testing.T) {
	assert.Equal(t, "preview", envOf("preview/main.k"))
	assert.Equal(t, "", envOf("workloads.k"))
	assert.Equal(t, "testdata", envOf("testdata/x.k"))
	assert.True(t, fixturesDecl.MatchString(`    fixtures = forge.Fixtures { scenario = "shop" }`))
	assert.True(t, fixturesDecl.MatchString(`        FIXTURES_SCENARIO = "shop"`))
	assert.False(t, fixturesDecl.MatchString(`    runtime_config = _runtime_config`))
}
