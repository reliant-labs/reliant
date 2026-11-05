package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestKCLVersionIsPinnedEverywhere holds forge to ONE KCL release,
// templates.KCLVersion, in every place that names one: the kcl-lang.io
// modules in go.mod (the runtime every render goes through), every workflow
// step that runs KCL's install-cli.sh, and scripts/bootstrap.sh.
//
// The drift it prevents had already happened: go.mod embedded the v0.13.0
// runtime while CI installed a v0.11.2 CLI, so the tests that shell out to
// `kcl` (internal/kcltest) judged a different KCL than the one forge renders
// with.
//
// It also refuses an install with no pin. Left unset, install-cli.sh resolves
// "latest" through an unauthenticated api.github.com call, rate-limited to 60
// an hour per runner IP. Exhausted, the script gets an empty tag and fails the
// job for a reason unrelated to the commit, and a new KCL release changes
// the build with no commit at all.
func TestKCLVersionIsPinnedEverywhere(t *testing.T) {
	want := templates.KCLVersion
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(want) {
		t.Fatalf("templates.KCLVersion = %q, want an explicit vX.Y.Z release", want)
	}
	root := forgeRepoRoot(t)

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mf, err := modfile.ParseLax("go.mod", gomod, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}
	modules := map[string]string{"kcl-lang.io/kcl-go": "", "kcl-lang.io/kpm": "", "kcl-lang.io/lib": ""}
	for _, r := range mf.Require {
		if _, ok := modules[r.Mod.Path]; ok {
			modules[r.Mod.Path] = r.Mod.Version
		}
	}
	for path, got := range modules {
		if got != want {
			t.Errorf("go.mod: %s %q, want %s", path, got, want)
		}
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	installs := 0
	for _, path := range workflows {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		installs += checkWorkflowKCLPin(t, "forge repo: "+rel, raw, want)
	}
	if installs == 0 {
		t.Error(".github/workflows: no KCL install found — the pin guard inspected nothing")
	}

	raw, err := os.ReadFile(filepath.Join(root, "scripts", "bootstrap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := string(raw)
	m := regexp.MustCompile(`(?m)^KCL_VERSION=(\S+)$`).FindStringSubmatch(bootstrap)
	if m == nil {
		t.Errorf("scripts/bootstrap.sh: no top-level KCL_VERSION=<release> pin")
	} else if m[1] != want {
		t.Errorf("scripts/bootstrap.sh: KCL_VERSION=%s, want %s", m[1], want)
	}
	if n := checkScriptKCLInstalls(t, "forge repo: scripts/bootstrap.sh", bootstrap); n == 0 {
		t.Error("scripts/bootstrap.sh: no KCL install found — the pin guard inspected nothing")
	}
}

// checkWorkflowKCLPin checks every step of a workflow that runs install-cli.sh
// and returns how many it found. The pin is the step's `env: KCL_VERSION`,
// which is how the installer takes a version.
func checkWorkflowKCLPin(t *testing.T, site string, body []byte, want string) int {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("%s: parse: %v", site, err)
	}
	installs := 0
	for job, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !strings.Contains(s.Run, kclInstaller) {
				continue
			}
			installs++
			if got := s.Env["KCL_VERSION"]; got != want {
				t.Errorf("%s (job %s, step %q): env KCL_VERSION %q, want %s", site, job, s.Name, got, want)
			}
		}
	}
	return installs
}

// checkScriptKCLInstalls checks that every install-cli.sh pipe in a shell
// script passes KCL_VERSION to the installer, and returns how many it found.
func checkScriptKCLInstalls(t *testing.T, site, body string) int {
	t.Helper()
	installs := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, kclInstaller) {
			continue
		}
		installs++
		if !strings.Contains(line, `| KCL_VERSION="${KCL_VERSION}" bash`) {
			t.Errorf("%s: %q runs the installer without KCL_VERSION — it installs latest", site, strings.TrimSpace(line))
		}
	}
	return installs
}

const kclInstaller = "kcl-lang.io/script/install-cli.sh"
