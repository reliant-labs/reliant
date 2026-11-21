package pkgguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// TestLibraryReadsNoAmbientEnvironment is the guard on forge/pkg: no package
// in the library that ships into a user's binary reads the process
// environment directly, except the packages pkg/.golangci.yml allowlists.
//
// Both halves of the rule are DERIVED from that config — the forbidden calls
// from its `forbid` patterns, the exemptions from its `exclusions.rules` —
// so this test and the lint that gates CI cannot drift apart, and neither
// names a file it does not own. Loosening the rule means editing the config,
// where the loosening is visible.
//
// Why it matters, stated once: a library changes behaviour only through the
// arguments its caller passed. A value read out of the ambient environment
// appears in no config proto, no KCL env block and no typed config object,
// so nothing the application can read explains why the same binary behaves
// differently on two machines — and in the worst case (an authentication
// mode) a shell variable decides whether the server checks credentials.
func TestLibraryReadsNoAmbientEnvironment(t *testing.T) {
	t.Parallel()
	pkgRoot := filepath.Join(repoRoot(t), "pkg")

	policy, err := LoadForbidigoPolicy(filepath.Join(pkgRoot, ".golangci.yml"))
	if err != nil {
		t.Fatalf("load the library's lint policy: %v", err)
	}
	if !policy.Enabled {
		t.Fatal("forbidigo is not in linters.enable in pkg/.golangci.yml — a settings block alone " +
			"configures a linter that never runs, which is exactly how the root config reported " +
			"green over 12 environment reads")
	}
	if len(policy.Forbid) == 0 {
		t.Fatal("pkg/.golangci.yml forbids nothing — this guard would then certify every call in the library")
	}

	findings, files, err := Scan(pkgRoot, policy)
	if err != nil {
		t.Fatalf("scan the library: %v", err)
	}
	if files == 0 {
		t.Fatal("the walk parsed no Go files under pkg/ — the guard is broken, and a guard that " +
			"inspects nothing certifies everything")
	}
	for _, f := range findings {
		t.Errorf("%s — forge/pkg compiles into every generated binary and must take this value from "+
			"its caller (a field on the options/config struct the package already has), not from the "+
			"ambient environment. The same rule fails a generated project's build: see "+
			"internal/templates/project/golangci.yml.tmpl. If this package is genuinely a sanctioned "+
			"reader, allowlist it in pkg/.golangci.yml with the reason written down.", f)
	}
	t.Logf("scanned %d Go files under pkg/", files)
}

// TestLibraryLintIsGatedInCI closes the failure mode the guard above cannot
// see: a lint config that nothing runs.
//
// The repo-root .golangci.yml excludes pkg/, and golangci-lint reads the
// config it finds walking up from the directory it runs in, so ci.yml's root
// `lint` job never reads pkg/.golangci.yml. Only a run from pkg/ does, and
// without one every linter that config enables gates nothing.
//
// The set is derived by walking the workflows and matching the COMMANDS, not
// by naming a job or a file, and it fails loudly when the walk finds no
// golangci-lint step at all.
//
// There used to be a `go test` half, requiring a `cd pkg && go test` step:
// while pkg/ was a separate module, `./...` from the repo root could not
// reach it, and every pkg/** test ran on developer machines only. pkg/ is
// part of the root module now and ci.yml's test jobs cover it;
// TestLibraryIsPartOfTheRootModule pins the fact that makes that so.
func TestLibraryLintIsGatedInCI(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}

	var lintSteps []step
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // repo-relative workflow path
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		var wf workflow
		if uerr := yaml.Unmarshal(raw, &wf); uerr != nil {
			t.Fatalf("parse %s: %v", e.Name(), uerr)
		}
		for _, job := range wf.Jobs {
			for _, s := range job.Steps {
				if strings.Contains(s.Uses, "golangci-lint-action") || strings.Contains(s.Run, "golangci-lint run") {
					lintSteps = append(lintSteps, s)
				}
			}
		}
	}

	if len(lintSteps) == 0 {
		t.Fatal("no workflow runs golangci-lint at all — this assertion inspected nothing")
	}

	if !runsFromPkgDir(lintSteps) {
		t.Error("no CI step lints forge/pkg under pkg/.golangci.yml: the root config excludes pkg/, " +
			"and golangci-lint reads the config it finds walking up from the directory it runs in, so " +
			"a job without `working-directory: pkg` (or a `cd pkg`) leaves the library that ships into " +
			"every user binary unlinted — which is how 12 forbidden environment reads accumulated " +
			"under a config that already forbade them")
	}
}

// TestLibraryIsPartOfTheRootModule pins why CI has no pkg-specific test step.
// ci.yml's test jobs run every package `go list ./...` reports from the repo
// root (scripts/ci-test-packages.sh), and that reaches pkg/** only while pkg/
// belongs to the root module. A pkg/go.mod would carve every library test out
// of that set without failing anything — which is exactly how they once ran
// on developer machines only.
func TestLibraryIsPartOfTheRootModule(t *testing.T) {
	t.Parallel()
	pkgRoot := filepath.Join(repoRoot(t), "pkg")
	if fi, err := os.Stat(pkgRoot); err != nil || !fi.IsDir() {
		t.Fatalf("pkg/ is not a directory at %s (%v) — this assertion would inspect nothing", pkgRoot, err)
	}
	_, err := os.Stat(filepath.Join(pkgRoot, "go.mod"))
	switch {
	case err == nil:
		t.Error("pkg/go.mod exists, so pkg/ is a separate module again and `go test ./...` from the " +
			"repo root — what ci.yml's test jobs run — no longer reaches a single library test. Keep " +
			"pkg/ a directory of the forge module, or add a CI step that tests it from pkg/ and " +
			"replace this assertion with one that pins that step")
	case !errors.Is(err, fs.ErrNotExist):
		t.Fatalf("stat pkg/go.mod: %v", err)
	}
}

// runsFromPkgDir reports whether any step is invoked with pkg/ as its
// working directory — declared via working-directory, or entered by the
// script itself.
func runsFromPkgDir(steps []step) bool {
	for _, s := range steps {
		if strings.Trim(s.workingDir(), "./") == "pkg" {
			return true
		}
		if strings.Contains(s.Run, "cd pkg") {
			return true
		}
	}
	return false
}

type workflow struct {
	Jobs map[string]struct {
		Steps []step `yaml:"steps"`
	} `yaml:"jobs"`
}

type step struct {
	Uses             string `yaml:"uses"`
	Run              string `yaml:"run"`
	WorkingDirectory string `yaml:"working-directory"`
	// With carries an action's inputs. golangci-lint-action takes its
	// working directory there rather than as a step key, so a guard that
	// only read the step key would report "unlinted" for a correctly wired
	// job — and, worse, could be "fixed" by moving the key where the action
	// ignores it.
	With map[string]any `yaml:"with"`
}

// workingDir is where this step actually runs, from either spelling.
func (s step) workingDir() string {
	if s.WorkingDirectory != "" {
		return s.WorkingDirectory
	}
	wd, _ := s.With["working-directory"].(string)
	return wd
}

// repoRoot is two levels above this package's source directory, which is the
// working directory `go test` runs it in whatever directory it was invoked
// from.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	return root
}
