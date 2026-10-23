package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// End-to-end cover for the scaffold-once advisory section of `forge
// project upgrade`. The generator-side unit tests
// (internal/generator/upgrade_advisory_test.go) pin the classification
// against a hand-built fixture; these run against a project the real
// scaffolder produced, and pin what a user actually sees.

// scaffoldAdvisoryProject creates a real project (service kind, one
// Next.js frontend) with `forge project new`'s own generator, so every
// file on disk is the birth-time render.
func scaffoldAdvisoryProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	g := generator.NewProjectGenerator("demo", dir, "github.com/example/demo")
	g.FrontendName = "web"
	g.ServiceName = "widgets"
	if err := g.Generate(); err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	// `forge project new` finishes with a generate run, which writes the
	// per-env config projection every env's KCL imports. Without it no env
	// renders, so the GitHub workflow rows — which need every env's render —
	// would be left out of the report instead of compared.
	cs, err := generator.LoadChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := loadAdvisoryConfig(t, dir)
	captureStdout(t, func() {
		if err := generatePerEnvDeployConfig(dir, cfg, cs, nil); err != nil {
			t.Fatalf("generatePerEnvDeployConfig: %v", err)
		}
	})
	return dir
}

// loadAdvisoryConfig reads the scaffolded forge.yaml back through the
// normal loader so the advisory rows see exactly the config a real run
// would.
func loadAdvisoryConfig(t *testing.T, dir string) *config.ProjectConfig {
	t.Helper()
	store, err := loadProjectStoreFrom(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}
	return store.Config()
}

// TestAdvisoryReport_FreshScaffoldIsSilent is the false-positive gate for
// the whole section: a project the scaffolder just wrote must be told
// nothing at all. It is also what keeps a row from being added on a
// hunch — a row whose render does not reproduce the scaffold's bytes
// fails here, which is exactly how the CI workflows were ruled out.
func TestAdvisoryReport_FreshScaffoldIsSilent(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)

	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("a freshly scaffolded project must have nothing to report:\n%s", out)
	}
}

// TestAdvisoryReport_GithubStartersAreInTheSet: every .github file forge
// scaffolds is a row. The PR template, CODEOWNERS and pre-commit.yml have a
// single renderer each; the GitHub Actions workflows share
// generator.CIWorkflows with `forge generate` (see
// TestCIWorkflows_NewAndGenerateRenderIdentically). All of them are the
// user's from birth, and without a row a fix to their template never reached
// a project that already had the file — which is how ci.yml's cancel-on-main
// concurrency outlived its fix.
//
// The fresh-scaffold silence test above is what makes these rows safe: it
// fails if a row's render does not reproduce the bytes the scaffold wrote.
func TestAdvisoryReport_GithubStartersAreInTheSet(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	// A workflow the user deleted stays deleted; reporting it as "absent"
	// on every upgrade would be forge asking for it back.
	deleted := filepath.Join(".github", "workflows", "e2e.yml")
	if err := os.Remove(filepath.Join(dir, deleted)); err != nil {
		t.Fatalf("precondition: the service scaffold writes %s: %v", deleted, err)
	}

	rows, ciNote, err := upgradeAdvisoryFiles(dir, cfg)
	if err != nil {
		t.Fatalf("upgradeAdvisoryFiles: %v", err)
	}
	if ciNote != "" {
		t.Fatalf("every env of a fresh scaffold renders, so the workflows must be compared; got %q", ciNote)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[filepath.ToSlash(r.Path)] = true
	}
	want := []string{".github/pull_request_template.md", ".github/CODEOWNERS", ".github/dependabot.yml"}
	workflows, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.yml"))
	if len(workflows) < 3 {
		t.Fatalf("precondition: expected the scaffold's workflows on disk, found %v", workflows)
	}
	for _, p := range workflows {
		rel, _ := filepath.Rel(dir, p)
		want = append(want, filepath.ToSlash(rel))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s should be an advisory row", w)
		}
	}
	if got[filepath.ToSlash(deleted)] {
		t.Errorf("%s was deleted by the user and must not be a row", deleted)
	}
}

// TestAdvisoryReport_PreFixCIConcurrencySurfaces is the defect this lane was
// extended for. A project scaffolded before the fix carries ci.yml's old
// `cancel-in-progress: true`, which on a busy runner pool lets each merge
// cancel the main run before it. `forge generate` never rewrites ci.yml — it
// is the user's — so the upgrade report is the only way that project learns
// of the fix: it must name ci.yml, `--check <path>` must show the corrected
// line, and `--force <path>` must adopt it without touching anything else.
func TestAdvisoryReport_PreFixCIConcurrencySurfaces(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	rel := filepath.Join(".github", "workflows", "ci.yml")
	const fixed = "cancel-in-progress: ${{ github.event_name == 'pull_request' }}"
	full := filepath.Join(dir, rel)
	born := mustRead(t, full)
	if !strings.Contains(born, fixed) {
		t.Fatalf("precondition: the scaffold's ci.yml carries %q:\n%s", fixed, born)
	}
	if err := os.WriteFile(full, []byte(strings.Replace(born, fixed, "cancel-in-progress: true", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	spared := filepath.Join(dir, ".github", "workflows", "pre-commit.yml")
	preCommit := strings.Replace(mustRead(t, spared),
		"cancel-in-progress: ${{ github.event_name == 'pull_request' }}", "cancel-in-progress: true", 1)
	if err := os.WriteFile(spared, []byte(preCommit), 0o644); err != nil {
		t.Fatal(err)
	}

	var report string
	withCwd(t, dir, func() {
		report = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})
	for _, want := range []string{filepath.ToSlash(rel), ".github/workflows/pre-commit.yml"} {
		if !strings.Contains(filepath.ToSlash(report), want) {
			t.Errorf("a pre-fix %s must be reported:\n%s", want, report)
		}
	}

	var detail string
	withCwd(t, dir, func() {
		detail = captureStdout(t, func() {
			if err := runUpgradeDetail(dir, cfg, []string{rel}); err != nil {
				t.Fatalf("runUpgradeDetail: %v", err)
			}
		})
	})
	if !strings.Contains(detail, "+  "+fixed) || !strings.Contains(detail, "-  cancel-in-progress: true") {
		t.Errorf("`upgrade --check %s` must show the concurrency fix as the diff:\n%s", rel, detail)
	}

	withCwd(t, dir, func() {
		_ = captureStdout(t, func() {
			if err := runUpgrade(false, true, []string{rel}, rootForgeVersion(dir)); err != nil {
				t.Fatalf("runUpgrade --force %s: %v", rel, err)
			}
		})
	})
	if got := mustRead(t, full); got != born {
		t.Errorf("--force %s must adopt the current template:\n%s", rel, got)
	}
	if mustRead(t, spared) != preCommit {
		t.Errorf("pre-commit.yml was not named after --force but was rewritten")
	}
}

// TestAdvisoryReport_UnrenderedEnvSaysWhyWorkflowsAreSkipped: which
// workflows a project has, and what they hold, come off a render of every
// env. With one env broken, comparing would report drift forge invented, so
// the workflows are left out — and the report says so, naming the env and
// the command that shows why, rather than going quietly silent.
func TestAdvisoryReport_UnrenderedEnvSaysWhyWorkflowsAreSkipped(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	broken := filepath.Join(dir, "deploy", "kcl", "staging", "main.k")
	if err := os.WriteFile(broken, []byte("this is not KCL (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stale ci.yml: it would be reported if the workflows were compared.
	staleFrontendFile(t, filepath.Join(dir, ".github", "workflows", "ci.yml"), 4)

	rows, ciNote, err := upgradeAdvisoryFiles(dir, cfg)
	if err != nil {
		t.Fatalf("upgradeAdvisoryFiles: %v", err)
	}
	for _, r := range rows {
		if strings.HasPrefix(filepath.ToSlash(r.Path), ".github/workflows/ci.yml") {
			t.Errorf("%s compared against a workflow set forge could not render", r.Path)
		}
	}
	if !strings.Contains(ciNote, "staging") || !strings.Contains(ciNote, "env render staging") {
		t.Errorf("the note must name the env that did not render and how to see why; got %q", ciNote)
	}

	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})
	if !strings.Contains(out, ciNote) {
		t.Errorf("the report must carry the note:\n%s", out)
	}
}

// TestAdvisoryReport_StaleGithubStarterSurfaces proves the .github rows
// carry real signal, not just membership.
func TestAdvisoryReport_StaleGithubStarterSurfaces(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	rel := filepath.Join(".github", "pull_request_template.md")
	staleFrontendFile(t, filepath.Join(dir, rel), 4)

	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})
	if !strings.Contains(out, rel) {
		t.Errorf("a stale PR template should be reported:\n%s", out)
	}
}

// TestAdvisoryReport_BehindFileSurfacesWithBothRemedies is the shape of
// the report a user reads. A file that is merely older gets a named,
// runnable adopt command; nothing implies the file was touched.
func TestAdvisoryReport_BehindFileSurfacesWithBothRemedies(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	rel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")
	staleFrontendFile(t, filepath.Join(dir, rel), 8)

	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})

	if !strings.Contains(out, "Files you own, whose forge templates have moved on") {
		t.Fatalf("expected the advisory section:\n%s", out)
	}
	if !strings.Contains(out, rel) {
		t.Errorf("expected %s to be named:\n%s", rel, out)
	}
	if !strings.Contains(out, "behind") {
		t.Errorf("expected the line-count measure of how far behind:\n%s", out)
	}
	// The measure must be a COUNT, not a diff. The report sizes a
	// difference; it never prints one. (`--check <path>` does.)
	if strings.Contains(out, "--- a/") || strings.Contains(out, "+++ b/") {
		t.Errorf("the summary must not print inline diffs:\n%s", out)
	}
	if !strings.Contains(out, "project upgrade --force "+rel) {
		t.Errorf("expected the per-path adopt command naming the file:\n%s", out)
	}
	if !strings.Contains(out, "Nothing above was changed") {
		t.Errorf("the section must say it changed nothing:\n%s", out)
	}
}

// TestAdvisoryReport_CustomizedFileIsFramedAsAMerge covers the hard case:
// customized AND behind. The user gets told what the template gained, that
// some lines here are theirs alone, and that taking the template is a
// merge they perform — never a bare "outdated" that writes off their work.
func TestAdvisoryReport_CustomizedFileIsFramedAsAMerge(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	rel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")
	full := filepath.Join(dir, rel)
	staleFrontendFile(t, full, 6)
	appendToFile(t, full, "\nexport const QUERY_KEY_SALT = \"control-plane\";\n")

	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})

	if !strings.Contains(out, "yours") {
		t.Errorf("a customized file must be reported as customized:\n%s", out)
	}
	if !strings.Contains(out, "a merge you make") {
		t.Errorf("adoption of a customized file must be framed as a merge:\n%s", out)
	}
	// A customized file is never handed a one-line adopt command, because
	// running it would silently discard the lines the report just counted.
	if strings.Contains(out, "project upgrade --force "+rel) {
		t.Errorf("a customized file must not be offered as a one-command adopt:\n%s", out)
	}
}

// TestRunUpgrade_AdoptsANamedScaffoldOnceFile drives the real command:
// `forge project upgrade --force <frontend path>` adopts that file and
// nothing else, and the path validator accepts it in the first place.
func TestRunUpgrade_AdoptsANamedScaffoldOnceFile(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	adopted := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")
	spared := filepath.Join("frontends", "web", "src", "lib", "events.ts")
	staleFrontendFile(t, filepath.Join(dir, adopted), 8)
	staleFrontendFile(t, filepath.Join(dir, spared), 8)
	sparedBefore := mustRead(t, filepath.Join(dir, spared))

	withCwd(t, dir, func() {
		_ = captureStdout(t, func() {
			if err := runUpgrade(false, true, []string{adopted}, rootForgeVersion(dir)); err != nil {
				t.Fatalf("runUpgrade --force %s: %v", adopted, err)
			}
		})
	})

	if mustRead(t, filepath.Join(dir, spared)) != sparedBefore {
		t.Errorf("%s was not named after --force but was rewritten", spared)
	}
	cfg := loadAdvisoryConfig(t, dir)
	var out string
	withCwd(t, dir, func() {
		out = captureStdout(t, func() {
			if _, err := runAdvisoryPass(dir, cfg, generator.ForceNone(), true, false); err != nil {
				t.Fatalf("runAdvisoryPass: %v", err)
			}
		})
	})
	if strings.Contains(out, adopted) {
		t.Errorf("%s was adopted but still reports as behind:\n%s", adopted, out)
	}
	if !strings.Contains(out, spared) {
		t.Errorf("%s is still behind and must still be reported:\n%s", spared, out)
	}
}

// TestResolveForceSelection_AcceptsScaffoldOncePaths pins that the path
// validator knows the advisory tier — otherwise the adopt command the
// report prints would be rejected by the command that printed it.
func TestResolveForceSelection_AcceptsScaffoldOncePaths(t *testing.T) {
	dir := scaffoldAdvisoryProject(t)
	cfg := loadAdvisoryConfig(t, dir)
	rel := filepath.Join("frontends", "web", "src", "lib", "query-client.ts")

	var sel generator.ForceSelection
	var err error
	withCwd(t, dir, func() {
		sel, err = resolveForceSelection(dir, cfg, true, []string{rel})
	})
	if err != nil {
		t.Fatalf("--force %s: %v", rel, err)
	}
	if !sel.Names(rel) {
		t.Errorf("%s should be explicitly named by the selection", rel)
	}
	if sel.Names(filepath.Join("frontends", "web", "src", "lib", "events.ts")) {
		t.Error("naming one path must not cover its neighbour")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// staleFrontendFile removes n lines from the middle of a file, leaving
// content that is a strict subset of the template's — the shape of a copy
// that is simply an older vintage.
func staleFrontendFile(t *testing.T, path string, n int) {
	t.Helper()
	lines := strings.Split(mustRead(t, path), "\n")
	if len(lines) < n*3 {
		t.Fatalf("%s too short (%d lines) to drop %d", path, len(lines), n)
	}
	mid := len(lines) / 2
	kept := append(append([]string{}, lines[:mid]...), lines[mid+n:]...)
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendToFile(t *testing.T, path, extra string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(mustRead(t, path)+extra), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
