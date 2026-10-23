package generator

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// The per-kind frontend rows.
//
// The advisory lane originally covered only the SHARED template roots, on
// the reasoning that a shared root is forge's own declaration of "this file
// is mechanism I own the design of". That reasoning is sound for deciding
// what forge should OWN. It is the wrong rule for deciding what forge should
// REPORT, and the gap it left was not theoretical: frontends/<name>/
// eslint.config.mjs is a file forge writes from a template and then never
// mentions again, in any command, ever. Hand-edit it and `forge project
// upgrade --check` says nothing at all — while .golangci.yml, the same kind
// of lint config on the backend side, reports a diff and both remedies
// because it is a Tier-2 managed file.
//
// The rule these tests pin is the generic one:
//
//	if forge wrote it from a template, its drift is reportable.
//
// Not "if it is a lint file". Registering eslint.config.mjs by name would
// re-create the same bug for the next per-kind template — so membership is
// derived from the template tree, exactly as the shared rows are, and these
// tests assert the DERIVATION rather than a list.

// perKindFrontendCfg describes a project with one frontend of the given
// template tree, at the conventional path.
func perKindFrontendCfg(tree string) *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:       "demo",
		ModulePath: "github.com/example/demo",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: tree}.WithDir(filepath.Join("frontends", "web")),
		},
	}
}

// advisoryPathSet renders the advisory rows for a config and returns their
// paths as a set.
func advisoryPathSet(t *testing.T, cfg *config.ProjectConfig) map[string]bool {
	t.Helper()
	rows, err := AdvisoryFilesFor(t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("AdvisoryFilesFor: %v", err)
	}
	set := map[string]bool{}
	for _, r := range rows {
		set[filepath.ToSlash(r.Path)] = true
	}
	return set
}

// TestAdvisories_CoversFrontendESLintConfig is the reported bug, stated
// directly: both web frontend flavors ship an eslint.config.mjs from a
// template, so both must be reportable.
//
// Reportable by ONE lane, though. This file and upgrade_frontend_managed.go
// landed in the same commit and both claimed the path, so `upgrade --check`
// listed it twice — and the two lanes do not offer the same remedies, so
// the duplicate was also a disagreement about who owns it. The managed lane
// keeps it (it refreshes a pristine copy and can offer `disown`; this lane
// only ever reports), which makes the assertion here the inverse: eslint
// must be absent from the advisory rows, and present in the managed set.
func TestAdvisories_CoversFrontendESLintConfig(t *testing.T) {
	t.Parallel()
	for _, tree := range []string{"nextjs", "vite-spa"} {
		t.Run(tree, func(t *testing.T) {
			t.Parallel()
			cfg := perKindFrontendCfg(tree)
			got := advisoryPathSet(t, cfg)
			const rel = "frontends/web/eslint.config.mjs"

			if got[rel] {
				t.Errorf("%s frontend: %q is an advisory row AND a managed file, so "+
					"`forge project upgrade --check` reports it twice with two different sets of remedies",
					tree, rel)
			}

			// Still covered — the original bug was total silence on this
			// file, and dropping the advisory row must not restore it.
			managed := false
			for _, f := range frontendManagedFiles(cfg) {
				if filepath.ToSlash(f.destPath) == rel {
					managed = true
				}
			}
			if !managed {
				t.Errorf("%s frontend: %q is in neither lane — hand-editing it is invisible to "+
					"`forge project upgrade --check` again", tree, rel)
			}
		})
	}
}

// TestAdvisories_CoverEveryRenderableTemplateFile is the generic rule, and
// the reason this file is not a list of names.
//
// Every file in a frontend's composed template tree is something forge
// wrote from a template. Each one must therefore be reportable, with the
// only admissible exceptions being files that are NOT a single fixed
// render — the ones another lane already owns (Tier-1 *_gen.* files
// regenerated every run, and the per-page/per-route templates whose
// destination depends on inventory this lane does not have).
func TestAdvisories_CoverEveryRenderableTemplateFile(t *testing.T) {
	t.Parallel()
	for _, tree := range []string{"nextjs", "vite-spa", "react-native"} {
		t.Run(tree, func(t *testing.T) {
			t.Parallel()
			files, err := templates.ListFrontendTree(tree)
			if err != nil {
				t.Fatalf("ListFrontendTree(%s): %v", tree, err)
			}
			got := advisoryPathSet(t, perKindFrontendCfg(tree))

			var missing []string
			for _, f := range files {
				rel := strings.TrimSuffix(f.Rel, ".tmpl")
				if advisoryExemptRel(rel) {
					continue
				}
				// A browser frontend's tsconfig peer pins are reconciled after
				// render to the installed layout and declaration files — a
				// second renderer (see webTsconfigRel). React Native's carries
				// no pins and stays covered, which this loop still asserts.
				if rel == webTsconfigRel && tree != "react-native" {
					continue
				}
				want := "frontends/web/" + rel
				if !got[want] {
					missing = append(missing, rel)
				}
			}
			if len(missing) > 0 {
				t.Errorf("%s: %d template-written file(s) have no advisory row, so drift in them "+
					"cannot be reported by any forge command:\n  %s",
					tree, len(missing), strings.Join(missing, "\n  "))
			}
		})
	}
}

// advisoryExemptRel is the ONLY admissible reason a template-written file
// may have no advisory row: some OTHER renderer owns that path, so this
// lane's copy would differ from what is legitimately on disk.
//
// This mirrors the lane's standing admission rule ("a row may only be added
// when the project has exactly ONE renderer for that path" — the reason the
// .github workflows are excluded). It is written out here, independently of
// the implementation, so the test constrains the exclusion set rather than
// restating whatever the implementation happens to exclude: adding a new
// name to the production list without adding it here fails this test.
func advisoryExemptRel(rel string) bool {
	// Tier-1 generated files: rewritten every `forge generate`, so the
	// drift probe already owns them and proves drift from an embedded
	// marker rather than inferring it.
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	if strings.HasSuffix(strings.TrimSuffix(base, ext), "_gen") {
		return true
	}
	// Seeded from the DISCOVERED entity set by the nav generator
	// (internal/cli/generate_frontend_nav.go). This lane has the frontend
	// config but not the inventory, so its render would show empty routes
	// against a populated project — a permanent false diff.
	switch rel {
	case "src/components/nav.tsx", "src/app/dashboard.tsx", "src/app/page.tsx":
		return true
	// Reconciled after the template render by EnsureWebRuntimeDependency,
	// which writes `^x.y.z` from a released forge and `file:<path>` from a
	// dev build. Two renderers, so the on-disk bytes legitimately differ
	// from a plain render — verified empirically: without this exclusion
	// TestAdvisories_FreshScaffoldSaysNothing reports package.json as
	// diverged on a tree the scaffolder just wrote.
	case "package.json":
		return true
	// Registered in the MANAGED lane (upgrade_frontend_managed.go), which
	// tracks and refreshes it and can offer `disown`. Both lanes claiming
	// it made `upgrade --check` print it twice with two different sets of
	// remedies — the same one-renderer rule, applied between lanes rather
	// than between registries.
	case frontendManagedRel:
		return true
	}
	// Per-entity output from other registries.
	for _, dir := range []string{"src/gen/", "src/mocks/"} {
		if strings.HasPrefix(rel, dir) {
			return true
		}
	}
	return false
}
