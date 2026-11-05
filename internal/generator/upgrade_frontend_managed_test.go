package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// The per-frontend lint config as a Tier-2 managed file.
//
// The bug, stated as the user found it: scaffold a project, hand-edit both
// `.golangci.yml` and `frontends/web/eslint.config.mjs`, then run
// `forge project upgrade --check`. The first reports `user-modified
// (skipped)` with a diff and both remedies. The second produces ZERO
// mentions anywhere in the output. Same class of file, same gesture,
// opposite outcome.
//
// These tests pin the symmetry, and they pin it against `.golangci.yml`
// itself rather than against a hard-coded expectation, so the two cannot
// drift apart again.

// managedFrontendCfg is a project with one nextjs frontend.
func managedFrontendCfg() *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:       "demo",
		ModulePath: "github.com/example/demo",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir(filepath.Join("frontends", "web")),
		},
	}
}

// TestFrontendESLintIsManaged is the registration itself: the file must be
// in the managed set, at Tier-2, for both web frontend flavors.
func TestFrontendESLintIsManaged(t *testing.T) {
	t.Parallel()
	for _, tree := range []string{"nextjs", "vite-spa"} {
		t.Run(tree, func(t *testing.T) {
			t.Parallel()
			cfg := managedFrontendCfg()
			cfg.Frontends[0].Type = tree

			want := filepath.Join("frontends", "web", "eslint.config.mjs")
			var found *managedFile
			for _, f := range managedFilesForCfg(cfg) {
				if f.destPath == want {
					entry := f
					found = &entry
					break
				}
			}
			if found == nil {
				t.Fatalf("%s: %s is not a managed file, so `forge project upgrade` cannot "+
					"report or refresh it — the same gesture on .golangci.yml reports a diff "+
					"and both remedies", tree, want)
			}
			if found.tier != Tier2 {
				t.Errorf("tier = %d, want Tier2 (%d): a lint config is a file users legitimately "+
					"edit, so Tier-1 would stomp those edits every generate", found.tier, Tier2)
			}
			body, err := renderManagedFile(*found, projectTemplateData{})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if !strings.Contains(string(body), "export default") {
				t.Errorf("rendered eslint config does not look like a flat config:\n%s", firstN(string(body), 200))
			}
		})
	}
}

// TestFrontendESLintReportsLikeGolangci is the symmetry test, and it is the
// actual user-visible bug.
//
// Both files are hand-edited in the same way; both must produce the same
// STATUS from the same upgrade pass. Comparing the two verdicts (rather
// than asserting a literal) is what keeps them from diverging again.
func TestFrontendESLintReportsLikeGolangci(t *testing.T) {
	dir := t.TempDir()
	cfg := managedFrontendCfg()

	// Lay down pristine renders of both files, exactly as a scaffold does,
	// then append a line to each — the user's hand edit.
	eslintRel := filepath.Join("frontends", "web", "eslint.config.mjs")
	golangciRel := ".golangci.yml"
	for _, tc := range []struct{ rel, edit string }{
		{eslintRel, "\n// LOCAL EDIT: a rule this project added by hand\n"},
		{golangciRel, "\n# LOCAL EDIT: a linter this project added by hand\n"},
	} {
		var entry *managedFile
		for _, f := range managedFilesForCfg(cfg) {
			if f.destPath == tc.rel {
				e := f
				entry = &e
				break
			}
		}
		if entry == nil {
			t.Fatalf("%s is not a managed file", tc.rel)
		}
		body, err := renderManagedFile(*entry, buildTemplateData(cfg, dir))
		if err != nil {
			t.Fatalf("render %s: %v", tc.rel, err)
		}
		full := filepath.Join(dir, tc.rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, append(body, []byte(tc.edit)...), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	results, err := UpgradeSelection(dir, cfg, ForceNone(), true)
	if err != nil {
		t.Fatalf("UpgradeSelection: %v", err)
	}
	byPath := map[string]UpgradeResult{}
	for _, r := range results {
		byPath[r.Path] = r
	}

	golangci, ok := byPath[golangciRel]
	if !ok {
		t.Fatalf("%s missing from the upgrade results — the comparison baseline is gone", golangciRel)
	}
	eslint, ok := byPath[eslintRel]
	if !ok {
		t.Fatalf("%s produced no upgrade result at all. This is the reported bug: forge writes "+
			"the file from a template and then never mentions it, while %s in the same run "+
			"reports %q with a diff and both remedies.", eslintRel, golangciRel, golangci.Status)
	}
	if eslint.Status != golangci.Status {
		t.Errorf("%s reports %q but %s reports %q — the same hand edit to the same class of "+
			"file must produce the same verdict", eslintRel, eslint.Status, golangciRel, golangci.Status)
	}
	if eslint.Diff == "" {
		t.Errorf("%s reported %q with no diff; %s shows one, and the diff is what makes the "+
			"report actionable", eslintRel, eslint.Status, golangciRel)
	}
}

// TestFrontendESLintPristineIsUpToDate is the false-positive gate: an
// untouched render must be silent. Without this, every project with a
// frontend would see a permanent diff on a file it never touched.
func TestFrontendESLintPristineIsUpToDate(t *testing.T) {
	dir := t.TempDir()
	cfg := managedFrontendCfg()
	rel := filepath.Join("frontends", "web", "eslint.config.mjs")

	var entry *managedFile
	for _, f := range managedFilesForCfg(cfg) {
		if f.destPath == rel {
			e := f
			entry = &e
			break
		}
	}
	if entry == nil {
		t.Fatal("eslint.config.mjs is not a managed file")
	}
	body, err := renderManagedFile(*entry, buildTemplateData(cfg, dir))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := UpgradeSelection(dir, cfg, ForceNone(), true)
	if err != nil {
		t.Fatalf("UpgradeSelection: %v", err)
	}
	for _, r := range results {
		if r.Path != rel {
			continue
		}
		if r.Status != UpgradeUpToDate {
			t.Errorf("a pristine render reports %q, want %q — this would show a diff on every "+
				"project for a file nobody touched:\n%s", r.Status, UpgradeUpToDate, r.Diff)
		}
		return
	}
	t.Fatalf("%s produced no result", rel)
}

// TestFrontendESLintExcludedFromStaleSweep is the data-loss guard.
//
// A Tier-2 managed file is STAMPED and is written by `forge project
// upgrade`, not by `forge generate`. The stale-artifact sweep flags any
// certified file the current generate run did not write, and
// `--force-cleanup` DELETES the pristine ones. Per-frontend paths cannot
// appear in the static UpgradeManagedPaths union (they depend on the
// frontends a project declares), so the sweep needs the shape test — and
// this pins that it has one.
func TestFrontendESLintExcludedFromStaleSweep(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"frontends/web/eslint.config.mjs",
		"frontends/admin-console/eslint.config.mjs",
	} {
		if !IsFrontendManagedPath(rel) {
			t.Errorf("%s is not recognised as upgrade-managed, so `forge generate --force-cleanup` "+
				"would DELETE it as a stale certified artifact", rel)
		}
	}
	for _, rel := range []string{
		"eslint.config.mjs",                   // project root: not a frontend file
		"frontends/web/src/eslint.config.mjs", // too deep to be the frontend's own
		"frontends/web/next.config.ts",        // different file
		"packages/ui-web/eslint.config.mjs",   // not under frontends/
	} {
		if IsFrontendManagedPath(rel) {
			t.Errorf("%s must not be treated as a per-frontend managed file", rel)
		}
	}
}

// TestFrontendESLintAbsentWithoutFrontends pins that a project with no
// frontends (or a react-native one, which ships no lint config) gets no
// entry — an entry with no template behind it would report every project as
// missing a file forge never writes.
func TestFrontendESLintAbsentWithoutFrontends(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  *config.ProjectConfig
	}{
		{"no frontends", &config.ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo"}},
		{"react-native only", &config.ProjectConfig{
			Name: "demo", ModulePath: "github.com/example/demo",
			Frontends: []config.FrontendConfig{
				config.FrontendConfig{Name: "mobile", Type: "react-native"}.WithDir(filepath.Join("frontends", "mobile")),
			},
		}},
		{"nil config", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, f := range frontendManagedFiles(tc.cfg) {
				t.Errorf("unexpected managed entry %s", f.destPath)
			}
		})
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
