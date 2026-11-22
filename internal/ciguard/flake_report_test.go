package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// scripts/flake-report.sh is, for a red run, a thin wrapper over `forge test
// flakes confirm` and `forge test flakes --issues` (internal/testflakes pins
// their attribution, race handling and issue filing, with the fixtures these
// tests used to hold). What the script still owns, and these tests pin: its
// arguments become forge's, it always exits 0, and --sweep is its own.

const flakeModule = "github.com/reliant-labs/forge"

// fakeGo answers `go list -m` with the module and records every `go test`,
// passing whatever it was asked to run.
func fakeGo(t *testing.T, dir string) (binDir, calls string) {
	t.Helper()
	binDir = filepath.Join(dir, "bin")
	calls = filepath.Join(dir, "go-calls")
	script := `#!/usr/bin/env bash
if [ "$1 $2" = "list -m" ]; then echo ` + flakeModule + `; exit 0; fi
if [ "$1" = test ]; then
  echo "$*" >>"` + calls + `"
  exit 0
fi
exit 1
`
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir, calls
}

// fakeIssueGH logs every call to $GH_LOG and answers `issue list` (from
// $GH_LIST, or $GH_LIST_SEARCH when the call names no --label) and `issue
// view` (from $GH_VIEW), applying -q/--jq as the real gh does — LF-only, as
// the real gh prints, even where jq is a native jq.exe that prints CRLF.
const fakeIssueGH = `#!/usr/bin/env bash
echo "$*" >>"$GH_LOG"
case "$1 $2" in
  "issue list") fixture="$GH_LIST_SEARCH"; for a in "$@"; do [ "$a" = --label ] && fixture="$GH_LIST"; done ;;
  "issue view") fixture="$GH_VIEW" ;;
  *) exit 0 ;;
esac
q="" prev=""
for a in "$@"; do case "$prev" in -q|--jq) q="$a" ;; esac; prev="$a"; done
if [ -n "$q" ]; then jq -r "$q" "${fixture:-/dev/null}" | tr -d '\r'; else cat "${fixture:-/dev/null}"; fi
`

// runWithFakeGH runs script with args from dir/mod, after writing files
// (paths relative to dir) and a fake gh and go first on PATH. It returns the
// script's output and the gh calls it made.
func runWithFakeGH(t *testing.T, script string, files map[string]string, env []string, args ...string) (out, ghCalls string) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH")
		}
	}
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	// The script runs as a child, which Go's test cache cannot see: read it here.
	if _, err := os.ReadFile(abs); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files["mod/.keep"] = ""
	// What the real gh answers when there is nothing to find.
	for name, empty := range map[string]string{"list.json": "[]", "search.json": "[]", "view.json": `{"body":"","comments":[]}`} {
		if _, ok := files[name]; !ok {
			files[name] = empty
		}
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin, _ := fakeGo(t, dir)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeIssueGH), 0o755); err != nil {
		t.Fatal(err)
	}
	// The script runs from mod/ as from the module root, so its
	// scripts/ci-forge.sh is mod/scripts/ci-forge.sh, here running the stub.
	env = append(env, installStubForge(t, filepath.Join(dir, "mod")))
	log := filepath.Join(dir, "gh.log")
	cmd := exec.Command("bash", append([]string{abs}, args...)...)
	cmd.Dir = filepath.Join(dir, "mod")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_LOG="+log, "GITHUB_REPOSITORY=o/r", "GH_REPO=o/r",
		"GH_LIST="+filepath.Join(dir, "list.json"), "GH_LIST_SEARCH="+filepath.Join(dir, "search.json"),
		"GH_VIEW="+filepath.Join(dir, "view.json"))
	cmd.Env = append(cmd.Env, env...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, b)
	}
	c, _ := os.ReadFile(log)
	return string(b), string(c)
}

// --sweep closes the open issues nothing keeps alive, and never runs forge.
func TestFlakeReportSweepsItsIssues(t *testing.T) {
	testkit.Slow(t, "execs bash and git")
	script := filepath.Join("..", "..", "scripts", "flake-report.sh")
	forgeLog := filepath.Join(t.TempDir(), "forge-log")
	// now = 2026-11-01T00:00:00Z
	_, calls := runWithFakeGH(t, script, map[string]string{
		"mod/go.mod":               "module " + flakeModule + "\n",
		"mod/internal/b/b_test.go": "package b\n\nfunc TestFresh(t *testing.T) {}\n\nfunc TestQuiet(t *testing.T) {}\n",
		"list.json": `[
  {"number":1,"title":"flake:suspected: TestFresh (internal/b)","createdAt":"2026-10-01T00:00:00Z",
   "comments":[{"body":"Failed then passed on retry again: run 9","createdAt":"2026-10-30T00:00:00Z"}]},
  {"number":2,"title":"flake:suspected: TestGone (internal/b)","createdAt":"2026-10-31T00:00:00Z","comments":[]},
  {"number":3,"title":"flake:suspected: TestQuiet (internal/b)","createdAt":"2026-10-20T00:00:00Z",
   "comments":[{"body":"Looking into it","createdAt":"2026-10-31T00:00:00Z"}]}
]`,
	}, []string{"FLAKE_SWEEP_NOW=1793491200", "FORGE_LOG=" + forgeLog}, "--sweep")
	var closed []string
	for _, l := range strings.Split(calls, "\n") {
		if strings.HasPrefix(l, "issue close ") {
			closed = append(closed, l)
		}
	}
	if len(closed) != 2 || !strings.HasPrefix(closed[0], "issue close 2 ") || !strings.Contains(closed[0], "`TestGone` no longer exists") ||
		!strings.HasPrefix(closed[1], "issue close 3 ") || !strings.Contains(closed[1], "not flaked again for 12 days") {
		t.Errorf("want #2 closed as deleted and #3 as quiet (a human comment is no occurrence), #1 kept; got:\n%s", strings.Join(closed, "\n"))
	}
	if b, _ := os.ReadFile(forgeLog); len(b) > 0 {
		t.Errorf("--sweep must not run forge:\n%s", b)
	}
}

// A red run is confirmed by forge and its issues synced: the script's
// arguments and MAX_TESTS become confirm's flags, --list stops after
// confirm, --dry-run reaches --issues, and nothing forge does fails it.
func TestFlakeReportConfirmsThroughForge(t *testing.T) {
	testkit.Slow(t, "execs bash")
	script := filepath.Join("..", "..", "scripts", "flake-report.sh")
	for _, tc := range []struct {
		name string
		env  []string
		args []string
		want []string
	}{
		{"run", []string{"MAX_TESTS=7"}, []string{"12345", "--dry-run"}, []string{
			"test flakes confirm --run 12345 --repo o/r --max 7",
			"test flakes --issues --repo o/r --dry-run",
		}},
		{"logs", nil, []string{"--logs", "../logs", "--sha", "abc", "--run-url", "https://ci/run/1"}, []string{
			"test flakes confirm --logs ../logs --sha abc --run-url https://ci/run/1 --repo o/r --max 15",
			"test flakes --issues --repo o/r",
		}},
		{"list", nil, []string{"--logs", "../logs", "--list"}, []string{
			"test flakes confirm --logs ../logs --repo o/r --list --max 15",
		}},
		{"forge fails", []string{"STUB_EXIT=3"}, []string{"12345"}, []string{
			"test flakes confirm --run 12345 --repo o/r --max 15",
			"test flakes --issues --repo o/r",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "forge-log")
			// runWithFakeGH fails the test unless the script exits 0.
			_, _ = runWithFakeGH(t, script, map[string]string{"logs/1.log": "ok\n"}, append(tc.env, "FORGE_LOG="+log), tc.args...)
			b, _ := os.ReadFile(log)
			if got := strings.Split(strings.TrimSpace(string(b)), "\n"); strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("forge calls:\n%s\nwant:\n%s", b, strings.Join(tc.want, "\n"))
			}
		})
	}
}
