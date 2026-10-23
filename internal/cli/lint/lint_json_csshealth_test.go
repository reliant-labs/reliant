package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestCollectFrontendLintJSONCSSHealthIndependentOfInventorySource pins the
// property that broke: `css_health` is a CONFIG setting, so whether it applies
// must not depend on HOW the frontend inventory was discovered.
//
// collectFrontendLintJSON has two arms — one over `cfg.Frontends`, one that
// scans `frontends/` when the config declares none. `cssHealth` was read
// INSIDE the first arm, so a project with a forge.yaml that declares no
// `frontends:` block (control-plane) got its frontend found by the scan and
// linted with css_health stuck at false. Nothing said so: there is no
// "skipped" finding on that path, and the JSON report looked clean.
//
// The tell is the "css_health enabled but no npm lint:styles script found"
// skip. This fixture deliberately provides NO `lint:styles` script, so a
// css_health that is actually ON must produce that skip. Before the fix the
// scan arm produced nothing at all.
func TestCollectFrontendLintJSONCSSHealthIndependentOfInventorySource(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	const cssHealthSkipPhrase = "css_health enabled but no npm lint:styles script"

	// A Node frontend with a lint script but NO lint:styles, and a
	// node_modules so the lane is not reported unavailable before it gets
	// as far as the css_health branch.
	writeFrontend := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
			t.Fatalf("mkdir node_modules: %v", err)
		}
		pkg := `{"scripts":{"lint":"true"}}`
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
			t.Fatalf("write package.json: %v", err)
		}
	}

	hasCSSHealthSkip := func(findings []lintJSONFinding) bool {
		for _, f := range findings {
			if strings.Contains(f.Message, cssHealthSkipPhrase) {
				return true
			}
		}
		return false
	}

	cssHealthOn := func() config.ProjectConfig {
		var c config.ProjectConfig
		c.Lint.Frontend.CSSHealth = true
		return c
	}

	// ── Arm 1: inventory DECLARED in forge.yaml. This always worked. ──
	t.Run("declared inventory honours css_health", func(t *testing.T) {
		// Declared paths are PROJECT-RELATIVE, and the lane runs from the
		// project root — so the fixture chdirs to a root and declares a
		// relative path, rather than handing the resolver an absolute
		// temp dir outside any project (which FrontendConfig.Dir now
		// correctly refuses as an escape).
		root := t.TempDir()
		writeFrontend(t, filepath.Join(root, "apps", "dashboard"))
		inDir(t, root)

		cfg := cssHealthOn()
		cfg.Frontends = []config.FrontendConfig{config.FrontendConfig{Name: "dashboard"}.WithDir("apps/dashboard")}

		rc := testRunCtx(t, false)
		rc.cfg = &cfg

		got, _ := collectFrontendLintJSON(rc)
		if !hasCSSHealthSkip(got) {
			t.Fatalf("declared inventory: css_health did not apply; findings = %+v", got)
		}
	})

	// ── Arm 2: inventory DERIVED by the frontends/ scan. This is the bug. ──
	t.Run("derived inventory honours css_health", func(t *testing.T) {
		// The scan arm reads the relative path "frontends", so run from a
		// temp root containing frontends/dashboard.
		root := t.TempDir()
		feDir := filepath.Join(root, "frontends", "dashboard")
		if err := os.MkdirAll(feDir, 0o755); err != nil {
			t.Fatalf("mkdir frontend: %v", err)
		}
		writeFrontend(t, feDir)
		t.Chdir(root)

		// A real forge.yaml shape for this case: css_health on, and NO
		// `frontends:` block — exactly control-plane's config.
		cfg := cssHealthOn()

		rc := testRunCtx(t, false)
		rc.cfg = &cfg

		got, _ := collectFrontendLintJSON(rc)
		if !hasCSSHealthSkip(got) {
			t.Fatalf("derived inventory: css_health silently OFF — the setting is enabled in "+
				"config but the frontends/ scan arm never read it, so CSS health was skipped "+
				"with no finding to say so.\nfindings = %+v", got)
		}
	})
}
