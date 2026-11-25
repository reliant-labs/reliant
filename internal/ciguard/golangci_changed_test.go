//go:build !windows

// scripts/ci-golangci-changed.sh is bash over a Linux runner's tools, and the
// plan test drives it with real git and go, so the file builds for every OS
// but Windows rather than skipping there at run time.

package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/goexec"
)

var golangciChangedScript = filepath.Join("..", "..", "scripts", "ci-golangci-changed.sh")

// TestGolangciJobGatesTheMergeTier holds the merge tier's `golangci` job
// (owner, 2026-11-08; #1947) to what makes it a gate rather than a report: it
// runs in the merge queue, `CI gate` needs it and refuses a merge-queue run in
// which it skipped, it lints what the entry changed with the pinned
// golangci-lint, and it is its own job, needing nothing, so it starts beside
// test-short and checks instead of after them.
func TestGolangciJobGatesTheMergeTier(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			If    string   `yaml:"if"`
			Needs []string `yaml:"needs"`
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	gl, ok := wf.Jobs["golangci"]
	if !ok {
		t.Fatalf("%s has no golangci job", ciWorkflow)
	}
	if strings.TrimSpace(gl.If) != queueTier {
		t.Errorf("golangci runs if %q, want the merge tier's %q", gl.If, queueTier)
	}
	if len(gl.Needs) != 0 {
		t.Errorf("golangci needs %v: it would start after them, on the merge tier's critical path", gl.Needs)
	}
	var installs, lints bool
	for _, s := range gl.Steps {
		if strings.HasSuffix(s.Uses, "/install-release-tool") && s.With["tool"] == "golangci-lint" {
			installs = true
		}
		if strings.Contains(s.Run, "scripts/ci-golangci-changed.sh HEAD^1") {
			lints = true
		}
	}
	if !installs {
		t.Error("golangci does not install golangci-lint through install-release-tool (the pinned, cached release)")
	}
	if !lints {
		t.Error("golangci never runs scripts/ci-golangci-changed.sh HEAD^1")
	}
	gate := wf.Jobs["gate"]
	if !slices.Contains(gate.Needs, "golangci") {
		t.Error("CI gate does not need golangci: a finding would merge")
	}
	var every string
	for _, s := range gate.Steps {
		if s.Name == "Every job passed" {
			every = s.Run
		}
	}
	if !strings.Contains(every, `.key == "golangci"`) {
		t.Error(`CI gate's "Every job passed" does not refuse a merge-queue run whose golangci job skipped`)
	}
}

// TestGolangciChangedPlan runs the script's --plan over a throwaway module:
// a changed package is linted under the root config and one under pkg/ under
// pkg/.golangci.yml; a deleted directory, testdata, a nested module, a
// directory the go tool ignores and a build-tag-only directory are not; the
// lint (a fake golangci-lint) gets the base and --whole-files, fails on a
// finding in either pass and runs both; a failing go list fails the gate; and
// a diff with no Go package is a no-op that never looks for golangci-lint.
func TestGolangciChangedPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("runs scripts/ci-golangci-changed.sh with real git and go list over a throwaway module; runs in task test")
	}
	for _, tool := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s not on PATH", tool)
		}
	}
	script, err := os.ReadFile(golangciChangedScript)
	if err != nil {
		t.Fatal(err)
	}
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
	run := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@t", "GOFLAGS=-mod=mod", "GOWORK=off")
		cmd.Env = goexec.WithTrimpath(append(cmd.Env, env...))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	sh := func(args ...string) string {
		t.Helper()
		out, err := run(nil, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("ci-golangci-changed.sh", string(script))
	write("gone/gone.go", "package gone\n")
	write("kept/kept.go", "package kept\n")
	write("pkg/lib/lib.go", "package lib\n")
	write("internal/x/testdata/fixture/go.mod", "module fixture\n\ngo 1.22\n")
	write("internal/x/testdata/fixture/f.go", "package fixture\n")
	write("nested/go.mod", "module example.com/nested\n\ngo 1.22\n")
	write("nested/n.go", "package nested\n")
	sh("git", "init", "-q", "-b", "main")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "base")
	base := strings.TrimSpace(sh("git", "rev-parse", "HEAD"))

	sh("git", "rm", "-rq", "gone")
	write("kept/kept.go", "package kept\n\nfunc F() {}\n")
	write("kept/kept_test.go", "package kept\n")
	write("pkg/lib/lib.go", "package lib\n\nfunc F() {}\n")
	write("internal/x/testdata/fixture/f.go", "package fixture\n\nfunc F() {}\n")
	write("nested/n.go", "package nested\n\nfunc F() {}\n")
	write("_ignored/x.go", "package x\n")
	write("tagged/t.go", "//go:build e2e\n\npackage tagged\n")
	write("README.md", "docs\n")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "change")

	out := sh("bash", "ci-golangci-changed.sh", "--plan", base)
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  ") {
			got = append(got, strings.TrimSpace(l))
		}
	}
	if want := []string{"./kept", "pkg/lib"}; !slices.Equal(got, want) {
		t.Errorf("planned %q, want %q:\n%s", got, want, out)
	}
	if !strings.Contains(out, "1 package(s) under .golangci.yml, 1 under pkg/.golangci.yml") {
		t.Errorf("plan does not split the configs:\n%s", out)
	}

	// The lint itself, with a fake golangci-lint that records each run's
	// directory and arguments and fails the pass FAKE_FAIL names: a clean lint
	// passes, a finding in either pass fails the gate, and a root failure
	// still runs the pkg/ pass.
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fake := "#!/bin/sh\n" +
		"[ \"$1\" = version ] && { echo fake; exit 0; }\n" +
		"pass=root; case \"$(pwd -P)\" in */pkg) pass=pkg ;; esac\n" +
		"echo \"$pass $*\" >>" + calls + "\n" +
		"[ \"$FAKE_FAIL\" = \"$pass\" ] && exit 1\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	wantCalls := "root run --new-from-rev=" + base + " --whole-files ./kept\n" +
		"pkg run --new-from-rev=" + base + " --whole-files ./lib\n"
	for _, fail := range []string{"", "root", "pkg"} {
		_ = os.Remove(calls)
		out, err := run([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "FAKE_FAIL=" + fail},
			"bash", "ci-golangci-changed.sh", base)
		if (err != nil) != (fail != "") {
			t.Errorf("lint with the %q pass failing: err=%v\n%s", fail, err, out)
		}
		got, _ := os.ReadFile(calls)
		if string(got) != wantCalls {
			t.Errorf("lint with the %q pass failing ran\n%s\nwant\n%s", fail, got, wantCalls)
		}
	}

	// A go list that fails (a toolchain it cannot load) fails the gate; it
	// never reads as "nothing to lint".
	badGo := t.TempDir()
	if err := os.WriteFile(filepath.Join(badGo, "go"), []byte("#!/bin/sh\necho 'go: toolchain not available' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = run([]string{"PATH=" + badGo + ":" + bin + ":" + os.Getenv("PATH")}, "bash", "ci-golangci-changed.sh", base)
	if err == nil || strings.Contains(out, "nothing to lint") || !strings.Contains(out, "go list failed") {
		t.Errorf("a failing go list: err=%v, want the gate to fail:\n%s", err, out)
	}

	write("README.md", "more docs\n")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "docs")
	// No package changed: a clean exit before golangci-lint is looked up
	// (PATH holds git, not golangci-lint).
	git, _ := exec.LookPath("git")
	out, err = run([]string{"PATH=" + filepath.Dir(git) + ":/usr/bin:/bin"}, "bash", "ci-golangci-changed.sh", "HEAD^1")
	if err != nil || !strings.Contains(out, "nothing to lint") {
		t.Errorf("a docs-only diff: err=%v, want a clean no-op:\n%s", err, out)
	}
}
