package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestPrettierVersionIsPinnedEverywhere holds every formatter of a
// scaffolded frontend's sources to ONE prettier release,
// templates.PrettierVersion: forge's own pre-commit hook (it formats
// pkg/components, which projects receive verbatim), the scaffolded
// pre-commit hook, and each frontend template's package.json.
//
// The failure it prevents is the one that held forge main's pre-commit red:
// the hook ran prettier 3.1.0 over library files formatted with 3.5.3, and
// the two releases disagree about a template literal and a parenthesised
// `??`. Neither side is wrong; they are just two versions, and whichever
// ran last wins.
func TestPrettierVersionIsPinnedEverywhere(t *testing.T) {
	want := templates.PrettierVersion
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(want) {
		t.Fatalf("templates.PrettierVersion = %q, want an exact X.Y.Z release", want)
	}

	root := forgeRepoRoot(t)
	own, err := os.ReadFile(filepath.Join(root, ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checkPreCommitPrettierPin(t, "forge repo: .pre-commit-config.yaml", own, want)

	g := &ProjectGenerator{Name: "pin-probe", Path: t.TempDir()}
	if err := g.generatePreCommitConfig(); err != nil {
		t.Fatal(err)
	}
	scaffolded, err := os.ReadFile(filepath.Join(g.Path, ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checkPreCommitPrettierPin(t, "scaffold: .pre-commit-config.yaml", scaffolded, want)

	manifests, err := filepath.Glob(filepath.Join(root, "internal", "templates", "frontend", "*", "package.json.tmpl"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no frontend package.json templates found (err=%v) — the pin guard would inspect nothing", err)
	}
	for _, path := range manifests {
		rel, _ := filepath.Rel(root, path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := prettierDependency(raw)
		if !ok {
			t.Errorf("%s: declares no prettier devDependency; its frontend would format with whatever is on PATH", rel)
			continue
		}
		if got != want {
			t.Errorf("%s: prettier %q, want exactly %q (templates.PrettierVersion) — a range drifts away from the hook one install at a time", rel, got, want)
		}
	}
}

// checkPreCommitPrettierPin asserts a pre-commit config runs prettier at
// exactly want, through the pinned local hook — not a mirror repo, whose rev
// is a second place the version could live.
func checkPreCommitPrettierPin(t *testing.T, site string, raw []byte, want string) {
	t.Helper()
	var cfg struct {
		Repos []struct {
			Repo  string `yaml:"repo"`
			Hooks []struct {
				ID   string   `yaml:"id"`
				Deps []string `yaml:"additional_dependencies"`
			} `yaml:"hooks"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: not valid YAML: %v", site, err)
	}
	found := 0
	for _, repo := range cfg.Repos {
		if strings.Contains(repo.Repo, "mirrors-prettier") {
			t.Errorf("%s: runs prettier from %s, whose rev is a version this guard cannot see; use the pinned local hook", site, repo.Repo)
		}
		for _, h := range repo.Hooks {
			if h.ID != "prettier" {
				continue
			}
			found++
			if len(h.Deps) != 1 || h.Deps[0] != "prettier@"+want {
				t.Errorf("%s: prettier hook installs %v, want exactly [prettier@%s] (templates.PrettierVersion)", site, h.Deps, want)
			}
		}
	}
	if found == 0 {
		t.Errorf("%s: no prettier hook found — the pin guard inspected nothing here", site)
	}
}

// prettierDependency reads the prettier version a package.json template
// declares. The templates carry Go actions, so they are not JSON until
// rendered; the one `"prettier": "<version>"` entry is read textually.
func prettierDependency(raw []byte) (string, bool) {
	m := prettierDepRE.FindSubmatch(raw)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

var prettierDepRE = regexp.MustCompile(`"prettier"\s*:\s*"([^"]*)"`)
