package forgetest

import (
	"encoding/json"
	"io/fs"
	"net/url"
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

// fixturesGuardFile is true for deploy/kcl/fixtures_guard.k and its test
// fixtures: they name the fixture keys to REFUSE a fixture-mode frontend that
// points at a live origin, and declare no env. The render check below still
// covers any key they could compute into an env.
func fixturesGuardFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	return rel == "fixtures_guard.k" || strings.HasPrefix(rel, "testdata/fixtures_guard_")
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
		if envOf(rel) == fixturesEnv || fixturesGuardFile(rel) {
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

// The preview env as forge renders it: fixture mode on, read-only, every
// origin the app dials a fake one, no real key. Its config.js is what makes
// the production build serve the shop pack (frontends/web/src/lib/
// fixturesConfig.ts), so a render that dropped a key, or named a real backend,
// would ship a preview whose sign-in code reaches live services.
func TestThePreviewEnvRendersFixtureMode(t *testing.T) {
	forge := Pinned(t) // Use the same KCL evaluator as env build, in the full lane.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root := kclRoot(t)

	cmd := forge.Command(t.Context(), "kcl", "eval", filepath.Join("deploy", "kcl", fixturesEnv, "main.k"), "-S", "output")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "render %s: %s", fixturesEnv, out)

	var doc struct {
		Frontends []struct {
			Name              string                            `json:"name"`
			RuntimeConfigSpec map[string]struct{ Value string } `json:"runtime_config_spec"`
		} `json:"frontends"`
	}
	require.NoError(t, json.Unmarshal(out, &doc), "render %s: not JSON", fixturesEnv)
	require.Len(t, doc.Frontends, 1, "env %s declares one frontend", fixturesEnv)
	web := doc.Frontends[0]
	assert.Equal(t, "web", web.Name)
	config := map[string]string{}
	for k, v := range web.RuntimeConfigSpec {
		config[k] = v.Value
	}

	assert.Equal(t, "shop", config["FIXTURES_SCENARIO"], "env %s serves the shop pack", fixturesEnv)
	assert.Equal(t, "true", config["FIXTURES_READ_ONLY"], "env %s refuses every write", fixturesEnv)
	assertInert(t, config)

	// The rendered document is checked in, and the browser test
	// (frontends/web/e2e/visual/preview-inert.visual.ts) serves exactly it as
	// config.js. UPDATE_PREVIEW_CONFIG=1 rewrites it from this render.
	golden := filepath.Join(root, previewConfigGolden)
	want, err := json.MarshalIndent(config, "", "  ")
	require.NoError(t, err)
	want = append(want, '\n')
	if os.Getenv("UPDATE_PREVIEW_CONFIG") == "1" { //nolint:forbidigo // a test's own regenerate switch, not app configuration
		require.NoError(t, os.WriteFile(golden, want, 0o644))
	}
	got, err := os.ReadFile(golden)
	require.NoError(t, err, "%s: run this test with UPDATE_PREVIEW_CONFIG=1 to write it", previewConfigGolden)
	assert.Equal(t, string(want), string(got), "%s is not env %s's render: run this test with UPDATE_PREVIEW_CONFIG=1", previewConfigGolden, fixturesEnv)
}

// previewConfigGolden is the preview env's rendered runtime config, the
// config.js the browser test serves.
const previewConfigGolden = "deploy/kcl/preview/runtime_config.golden.json"

// The checked-in render is inert too (read here, so a hand edit to it is seen
// by the -short lane as well).
func TestThePreviewConfigGoldenIsInert(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(kclRoot(t), previewConfigGolden))
	require.NoError(t, err)
	var config map[string]string
	require.NoError(t, json.Unmarshal(data, &config))
	assert.Equal(t, "shop", config["FIXTURES_SCENARIO"])
	assert.Equal(t, "true", config["FIXTURES_READ_ONLY"])
	assertInert(t, config)
}

// assertInert: every origin the page could dial is empty or under .invalid,
// and no key is a real one.
func assertInert(t *testing.T, config map[string]string) {
	t.Helper()
	var origins int
	for key, value := range config {
		if !strings.HasSuffix(key, "_URL") && key != "OTEL_ENDPOINT" {
			continue
		}
		origins++
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		require.NoError(t, err, "%s", key)
		assert.True(t, (u.Scheme == "https" || u.Scheme == "http") && strings.HasSuffix(u.Hostname(), ".invalid"),
			"%s = %q must be empty or a fake origin under .invalid, which never resolves", key, value)
	}
	for _, key := range []string{"API_URL", "CONTROL_PLANE_API_URL", "GATEWAY_URL", "SUPABASE_URL"} {
		assert.NotEmpty(t, config[key], "%s: cloud mode needs it, so it must be named, and fake", key)
	}
	assert.GreaterOrEqual(t, origins, 4)
	assert.Empty(t, config["STRIPE_PUBLISHABLE_KEY"], "no Stripe key")
	for _, prefix := range []string{"eyJ", "sb_", "pk_"} {
		assert.False(t, strings.HasPrefix(config["SUPABASE_ANON_KEY"], prefix), "SUPABASE_ANON_KEY looks like a real key (%s…)", prefix)
	}
	assert.Contains(t, []string{"", "false"}, config["OTEL_REPLAY"], "no session replay")
}

func TestEnvOf(t *testing.T) {
	assert.Equal(t, "preview", envOf("preview/main.k"))
	assert.Equal(t, "", envOf("workloads.k"))
	assert.Equal(t, "testdata", envOf("testdata/x.k"))
	assert.True(t, fixturesGuardFile("fixtures_guard.k"))
	assert.True(t, fixturesGuardFile("testdata/fixtures_guard_refused_api.k"))
	assert.False(t, fixturesGuardFile("demo/fixtures_guard.k"))
	assert.False(t, fixturesGuardFile("testdata/dev_sign_in_render.k"))
	assert.True(t, fixturesDecl.MatchString(`    fixtures = forge.Fixtures { scenario = "shop" }`))
	assert.True(t, fixturesDecl.MatchString(`        FIXTURES_SCENARIO = "shop"`))
	assert.False(t, fixturesDecl.MatchString(`    runtime_config = _runtime_config`))
}
