package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// reliant's forge bump (reliant#2327): `forge project upgrade` wrote
// docker-compose.yml, which reliant#1539 had deleted, and
// frontends/web/eslint.config.mjs beside the frontend's own eslint.config.js,
// and reliant had to `forge project disown --absent` eight files. None of them
// was in the birth ledger, and "not recorded" was all upgrade asked.
// upgrade_absent.go holds the rules these tests pin.

// everyTemplateNew is the worst case for the rules: a range across which
// every template is new, so only the project's own state can hold one back.
func everyTemplateNew(string) bool { return true }

func absentFixture(t *testing.T) (string, *config.ProjectConfig) {
	t.Helper()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	return t.TempDir(), managedFrontendCfg()
}

func writeAbsentFixture(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func resultFor(t *testing.T, results []UpgradeResult, rel string) UpgradeResult {
	t.Helper()
	for _, r := range results {
		if filepath.ToSlash(r.Path) == rel {
			return r
		}
	}
	t.Fatalf("no upgrade result for %s", rel)
	return UpgradeResult{}
}

func requireAbsent(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Errorf("upgrade wrote %s (stat err %v)", rel, err)
	}
}

// A sibling variant is the project's own copy under another name: even a
// template new across the range is not written beside it.
func TestUpgrade_NeverWritesAScaffoldBesideTheProjectsOwnVariant(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir, cfg := absentFixture(t)
	writeAbsentFixture(t, dir, "frontends/web/eslint.config.js", "export default [];\n")
	writeAbsentFixture(t, dir, "compose.yaml", "services: {}\n")
	writeAbsentFixture(t, dir, ".golangci.yaml", "version: \"2\"\n")

	results, err := UpgradeWith(dir, cfg, UpgradeOptions{NewSince: everyTemplateNew})
	if err != nil {
		t.Fatalf("UpgradeWith: %v", err)
	}
	for rel, own := range map[string]string{
		"frontends/web/eslint.config.mjs": "frontends/web/eslint.config.js",
		"docker-compose.yml":              "compose.yaml",
		".golangci.yml":                   ".golangci.yaml",
	} {
		requireAbsent(t, dir, rel)
		if r := resultFor(t, results, rel); r.Status != UpgradeSkipped || r.Equivalent != own {
			t.Errorf("%s: status %q equivalent %q, want skipped with equivalent %s", rel, r.Status, r.Equivalent, own)
		}
	}
}

// A template that is not new across the range, for a file the project never
// had, is offered with the diff adding it would apply — and written only when
// --force names it.
func TestUpgrade_OffersAnOldScaffoldTheProjectNeverHad(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir, cfg := absentFixture(t)
	const rel = "idp-steps.yaml"

	results, err := UpgradeWith(dir, cfg, UpgradeOptions{NewSince: func(v string) bool { return v == "v9.9.9" }})
	if err != nil {
		t.Fatalf("UpgradeWith: %v", err)
	}
	requireAbsent(t, dir, rel)
	r := resultFor(t, results, rel)
	if !r.Offered || r.Status != UpgradeSkipped || r.Since != "v0.0.4" || r.Diff == "" || r.Missing == 0 {
		t.Errorf("%s: %+v, want an offer since v0.0.4 carrying its diff", rel, r)
	}
	if checksums.ScaffoldRecorded(dir, rel) {
		t.Errorf("an offer recorded a birth for %s", rel)
	}

	// UpgradeSelection knows no range: nothing is new to it, so it offers too.
	results, err = UpgradeSelection(dir, cfg, ForceNone(), false)
	if err != nil {
		t.Fatalf("UpgradeSelection: %v", err)
	}
	requireAbsent(t, dir, rel)
	if r := resultFor(t, results, rel); !r.Offered {
		t.Errorf("UpgradeSelection: %s = %+v, want offered", rel, r)
	}

	// Naming the path is the adoption gesture.
	if _, err := UpgradeSelection(dir, cfg, ForcePaths(rel), false); err != nil {
		t.Fatalf("UpgradeSelection --force %s: %v", rel, err)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Errorf("--force %s did not add it: %v", rel, err)
	}
}

// A file the project deleted in git, before the ledger could record it, stays
// deleted — even when its template is new across the range. The project is a
// subdirectory of the checkout, as in a monorepo, so git's paths must be read
// relative to it.
func TestUpgrade_NeverRecreatesAScaffoldDeletedInGitHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git to build a history; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo, cfg := absentFixture(t)
	dir := filepath.Join(repo, "app")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t",
			"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	writeAbsentFixture(t, dir, "docker-compose.yml", "services: {}\n")
	writeAbsentFixture(t, dir, "README.md", "app\n")
	git("add", "-A")
	git("commit", "-q", "-m", "scaffold")
	git("rm", "-q", "app/docker-compose.yml")
	git("commit", "-q", "-m", "drop compose (reliant#1539)")

	results, err := UpgradeWith(dir, cfg, UpgradeOptions{NewSince: everyTemplateNew})
	if err != nil {
		t.Fatalf("UpgradeWith: %v", err)
	}
	requireAbsent(t, dir, "docker-compose.yml")
	r := resultFor(t, results, "docker-compose.yml")
	if !r.LeftDeleted || r.DeletedIn == "" || r.Status != UpgradeSkipped {
		t.Errorf("docker-compose.yml: %+v, want left deleted naming the commit that deleted it", r)
	}
	// The control: a template new across the range that git never saw, with
	// no variant beside it, is still born.
	if _, err := os.Stat(filepath.Join(dir, "idp-steps.yaml")); err != nil {
		t.Errorf("a new scaffold git never saw was not added: %v", err)
	}
}

// Each rule above needs the entry's since: an entry without one could never
// be born, which is a template nobody can receive by upgrading.
func TestEveryTier2ManagedFileDeclaresSince(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{config.ProjectKindService, config.ProjectKindCLI, config.ProjectKindLibrary} {
		for _, binary := range []string{config.ProjectBinaryPerService, config.ProjectBinaryShared} {
			files := append(managedFilesForKindBinary(kind, binary, "app"), frontendManagedFiles(managedFrontendCfg())...)
			for _, f := range files {
				if f.tier == Tier2 && !semver.IsValid(f.since) {
					t.Errorf("%s (%s/%s): since %q is not a release; set it to the release whose upgrade first ships the file", f.destPath, kind, binary, f.since)
				}
			}
		}
	}
}
