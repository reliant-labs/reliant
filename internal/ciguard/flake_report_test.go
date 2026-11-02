package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/flake-report.sh reads a failed main run's logs, reruns each failing
// Go test once and files the ones that pass as flake:suspected. Two ways it
// got a run wrong, both pinned here against fixtures in the shape the real
// scripts print:
//
//   - ATTRIBUTION. scripts/ci-test-shard.sh prints every package summary
//     (`ok`/`FAIL <pkg>`) BEFORE the failed tests' output, so a `--- FAIL`
//     cannot be attributed to the `FAIL <pkg>` line that follows it: there is
//     none in its step, and the next step's unrelated `FAIL` must not claim it.
//   - RACE. main's test jobs run under -race. A retry without it "passes" a
//     race-only failure, which would then be filed as a flake.

const flakeModule = "github.com/reliant-labs/forge"

// shardLog is ci-test-shard.sh's console output for a shard with two failed
// packages: summaries first, then the failed tests' output, then the
// annotations naming package and test.
const shardLog = "ok  \t" + flakeModule + "/internal/a\t0.1s\n" +
	"FAIL\t" + flakeModule + "/internal/b\t0.2s\n" +
	"FAIL\t" + flakeModule + "/internal/c\t0.3s\n" +
	"shard 0/2: 12s wall\n\n" +
	"::group::output of failed tests\n" +
	"=== RUN   TestB\n    b_test.go:5: boom\n--- FAIL: TestB (0.00s)\n" +
	"=== RUN   TestC\n    c_test.go:9: bang\n--- FAIL: TestC (0.01s)\n" +
	"::endgroup::\n" +
	"::error::FAIL internal/b TestB\n" +
	"::error::FAIL internal/c TestC\n"

// laterStepLog is a later step of the same run with a package failure of its
// own (a build failure: no test named).
const laterStepLog = "FAIL\t" + flakeModule + "/internal/z [build failed]\n"

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
  for a in "$@"; do case "$a" in '^('*) re="$a" ;; esac; done
  re="${re#^(}"; re="${re%)\$}"
  IFS='|' read -r -a ts <<<"$re"
  for t in "${ts[@]}"; do echo "--- PASS: $t (0.00s)"; done
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

// runFlakeReport runs the script from a module tree holding the given
// packages, over a --logs directory holding files.
func runFlakeReport(t *testing.T, files map[string]string, pkgs []string, args ...string) (out, calls string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "flake-report.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// The script runs as a child, which Go's test cache cannot see: read it here.
	if _, err := os.ReadFile(script); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	for name, body := range files {
		p := filepath.Join(logs, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod := filepath.Join(dir, "mod")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := os.MkdirAll(filepath.Join(mod, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin, callsFile := fakeGo(t, dir)
	cmd := exec.Command("bash", append([]string{script, "--logs", logs, "--sha", "abc"}, args...)...)
	cmd.Dir = mod
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_REPOSITORY=reliant-labs/forge")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("flake-report.sh: %v\n%s", err, b)
	}
	c, _ := os.ReadFile(callsFile)
	return string(b), string(c)
}

func TestFlakeReportAttributesShardFailuresRegardlessOfOrder(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH")
		}
	}
	out, _ := runFlakeReport(t, map[string]string{"1-short.log": shardLog, "2-later.log": laterStepLog}, nil, "--list")
	for _, want := range []string{
		"  " + flakeModule + "/internal/b\tTestB",
		"  " + flakeModule + "/internal/c\tTestC",
		"2 failing test(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "internal/z") {
		t.Errorf("a later step's package failure claimed the shard's tests:\n%s", out)
	}

	// The short tier's go test -json artifact names package and test outright,
	// with no console log at all.
	json := `{"Action":"run","Package":"` + flakeModule + `/internal/d","Test":"TestD"}` + "\n" +
		`{"Action":"output","Package":"` + flakeModule + `/internal/d","Test":"TestD","Output":"    d_test.go:3: no\n"}` + "\n" +
		`{"Action":"fail","Package":"` + flakeModule + `/internal/d","Test":"TestD"}` + "\n" +
		`{"Action":"fail","Package":"` + flakeModule + `/internal/d"}` + "\n"
	out, _ = runFlakeReport(t, map[string]string{"test-short-0/test-shard.json": json, "2-later.log": laterStepLog}, nil, "--list")
	if !strings.Contains(out, "  "+flakeModule+"/internal/d\tTestD") || strings.Contains(out, "internal/z") {
		t.Errorf("the -json stream was not attributed by its own package:\n%s", out)
	}
}

func TestFlakeReportNeverRetriesARaceWithoutTheDetector(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH")
		}
	}
	raceLog := "FAIL\t" + flakeModule + "/internal/r\t0.4s\n" +
		"=== RUN   TestRacy\n==================\nWARNING: DATA RACE\nWrite at 0x00c0000a by goroutine 8:\n==================\n" +
		"    testing.go:1490: race detected during execution of test\n--- FAIL: TestRacy (0.00s)\n" +
		"=== RUN   TestFlaky\n    r_test.go:20: timeout\n--- FAIL: TestFlaky (1.00s)\n" +
		"::error::FAIL internal/r TestRacy\n::error::FAIL internal/r TestFlaky\n"
	out, calls := runFlakeReport(t, map[string]string{"race.log": raceLog}, []string{"internal/r"}, "--dry-run")
	if !strings.Contains(out, "TestRacy reported a data race: a real failure") {
		t.Errorf("the race was not recognised:\n%s", out)
	}
	if strings.Contains(calls, "TestRacy") {
		t.Errorf("a race-only failure was retried (it would pass without the detector and be filed):\n%s", calls)
	}
	if !strings.Contains(calls, "-race") || !strings.Contains(calls, "TestFlaky") {
		t.Errorf("the other failure must be retried under -race, got:\n%s", calls)
	}
	if !strings.Contains(out, "TestFlaky PASSED on retry") || !strings.Contains(out, "would file 1 flake:suspected issue(s)") {
		t.Errorf("want TestFlaky alone reported:\n%s", out)
	}
}

// fakeIssueGH logs every call to $GH_LOG and answers `issue list` (from
// $GH_LIST, or $GH_LIST_SEARCH when the call names no --label) and `issue
// view` (from $GH_VIEW), applying -q/--jq as the real gh does.
const fakeIssueGH = `#!/usr/bin/env bash
echo "$*" >>"$GH_LOG"
case "$1 $2" in
  "issue list") fixture="$GH_LIST_SEARCH"; for a in "$@"; do [ "$a" = --label ] && fixture="$GH_LIST"; done ;;
  "issue view") fixture="$GH_VIEW" ;;
  *) exit 0 ;;
esac
q="" prev=""
for a in "$@"; do case "$prev" in -q|--jq) q="$a" ;; esac; prev="$a"; done
if [ -n "$q" ]; then jq -r "$q" "${fixture:-/dev/null}"; else cat "${fixture:-/dev/null}"; fi
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

// One issue per test: a later flake reopens a closed one rather than filing a
// second, and --sweep closes the ones nothing keeps alive.
func TestFlakeReportReopensAndSweepsItsIssues(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "flake-report.sh")
	logs := "FAIL\t" + flakeModule + "/internal/b\t0.2s\n" +
		"=== RUN   TestB\n    b_test.go:5: boom\n--- FAIL: TestB (0.00s)\n::error::FAIL internal/b TestB\n"
	out, calls := runWithFakeGH(t, script, map[string]string{
		"logs/1.log":               logs,
		"mod/internal/b/b_test.go": "package b\n",
		"list.json":                `[{"number":7,"title":"flake:suspected: TestB (internal/b)","state":"CLOSED"}]`,
	}, nil, "--logs", "../logs", "--sha", "abc")
	if !strings.Contains(calls, "issue reopen 7 ") || !strings.Contains(calls, "issue comment 7 ") {
		t.Errorf("a closed issue for the same test must be reopened and commented on:\n%s\n%s", out, calls)
	}
	if strings.Contains(calls, "issue create") {
		t.Errorf("a second issue was filed for one test:\n%s", calls)
	}

	// now = 2026-11-01T00:00:00Z
	_, calls = runWithFakeGH(t, script, map[string]string{
		"mod/go.mod":               "module " + flakeModule + "\n",
		"mod/internal/b/b_test.go": "package b\n\nfunc TestFresh(t *testing.T) {}\n\nfunc TestQuiet(t *testing.T) {}\n",
		"list.json": `[
  {"number":1,"title":"flake:suspected: TestFresh (internal/b)","createdAt":"2026-10-01T00:00:00Z",
   "comments":[{"body":"Failed then passed on retry again: run 9","createdAt":"2026-10-30T00:00:00Z"}]},
  {"number":2,"title":"flake:suspected: TestGone (internal/b)","createdAt":"2026-10-31T00:00:00Z","comments":[]},
  {"number":3,"title":"flake:suspected: TestQuiet (internal/b)","createdAt":"2026-10-20T00:00:00Z",
   "comments":[{"body":"Looking into it","createdAt":"2026-10-31T00:00:00Z"}]}
]`,
	}, []string{"FLAKE_SWEEP_NOW=1793491200"}, "--sweep")
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
}
