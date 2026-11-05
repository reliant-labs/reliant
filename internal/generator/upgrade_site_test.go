package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// A static site — forge.yaml, a root Vite app and deploy/kcl envs, no go.mod
// — was upgraded into a Go service: its deploy/kcl tree derives the kind as
// "service", so the templates phase wrote cmd/<name>/cmd/*.go,
// pkg/middleware, internal/metrics, Dockerfile, buf.yaml, Taskfile.yml and
// docker-compose.yml beside the app (reliant-labs/landing-page, v0.1.47 →
// v1.0.14), and the lint phase then failed on a buf module with no protos.
//
// The project is loaded from disk, as `forge project upgrade` loads it,
// because whether it is a Go module is read from the tree. The same fixture
// with a go.mod is the control: it proves the upgrade run below does write
// the Go project's files when they apply, so their absence for the site is
// the fix at work and not an upgrade that wrote nothing at all.
func TestUpgrade_SiteWithoutGoModuleWritesNoGoProjectFiles(t *testing.T) {
	t.Parallel()
	goProjectFiles := []string{"cmd", "pkg", "internal", "Dockerfile", "buf.yaml", "Taskfile.yml", "docker-compose.yml", ".golangci.yml"}

	newProject := func(t *testing.T, goModule bool) (string, *config.ProjectConfig) {
		t.Helper()
		dir := t.TempDir()
		write := func(rel, body string) {
			t.Helper()
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		yaml := "name: landing\nforge_version: v1.0.14\n"
		if goModule {
			yaml += "module_path: example.com/landing\n"
			write("go.mod", "module example.com/landing\n\ngo 1.24\n")
		}
		write("forge.yaml", yaml)
		write("package.json", `{"name":"landing","devDependencies":{"vite":"^5.0.0"}}`)
		write("deploy/kcl/prod/main.k", "import forge\n")

		cfg, err := config.LoadProjectDir(dir)
		if err != nil {
			t.Fatalf("LoadProjectDir: %v", err)
		}
		if cfg.HasGoModule() != goModule {
			t.Fatalf("HasGoModule = %v, want %v: the fixture no longer exercises the shape it names", cfg.HasGoModule(), goModule)
		}
		return dir, cfg
	}

	// The writing run: not --check, and --force over everything, so no
	// managed file the project's shape selects is held back.
	upgrade := func(t *testing.T, dir string, cfg *config.ProjectConfig) []UpgradeResult {
		t.Helper()
		results, err := UpgradeSelection(dir, cfg, ForceAll(), false)
		if err != nil {
			t.Fatalf("UpgradeSelection: %v", err)
		}
		return results
	}

	t.Run("go module control", func(t *testing.T) {
		t.Parallel()
		dir, cfg := newProject(t, true)
		upgrade(t, dir, cfg)
		for _, rel := range []string{"Dockerfile", "cmd"} {
			if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
				t.Errorf("upgrade of a Go module did not write %s (%v): the site case below would pass vacuously", rel, err)
			}
		}
	})

	t.Run("static site", func(t *testing.T) {
		t.Parallel()
		dir, cfg := newProject(t, false)
		for _, r := range upgrade(t, dir, cfg) {
			if filepath.Ext(r.Path) == ".go" || filepath.Base(r.Path) == "Dockerfile" || r.Path == "buf.yaml" {
				t.Errorf("upgrade planned Go project file %s (%s) for a static site", r.Path, r.Status)
			}
		}
		for _, rel := range append([]string{"go.mod"}, goProjectFiles...) {
			if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
				t.Errorf("upgrade wrote %s into a static site with no Go module", rel)
			}
		}
		for _, p := range ManagedPathsFor(cfg) {
			if filepath.Ext(p) == ".go" {
				t.Errorf("ManagedPathsFor lists Go file %s for a site", p)
			}
		}
	})
}
