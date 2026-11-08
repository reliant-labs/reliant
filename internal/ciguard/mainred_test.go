package ciguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The script under test, relative to this package. The test reads it itself
// and runs a copy, so the test cache sees an edit to it.
var mainRedScript = filepath.Join("..", "..", "scripts", "ci-main-red.sh")

// fakeGH answers `gh api [--paginate] <path> [--jq <expr>]` from fixture files
// named after the path (query string dropped, slashes as underscores).
const fakeGH = `#!/usr/bin/env bash
path=""; expr=""
while [ $# -gt 0 ]; do
  case "$1" in
    api) ;;
    --jq) shift; expr=$1 ;;
    --*) ;;
    *) path=$1 ;;
  esac
  shift
done
f="$FAKE_GH_DIR/$(sed 's/?.*//; s#/#_#g' <<<"$path")"
[ -f "$f" ] || { echo "fake gh: no fixture for $path" >&2; exit 1; }
if [ -n "$expr" ]; then jq -r "$expr" "$f"; else cat "$f"; fi
`

type fakeJob struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Conclusion string     `json:"conclusion"`
	Steps      []fakeStep `json:"steps"`
}

type fakeStep struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// One of main's ci.yml push runs. An unfinished run has no conclusion.
type fakeRun struct {
	ID         int    `json:"id"`
	Conclusion string `json:"conclusion,omitempty"`
}

type mainRedCase struct {
	event  string // pull_request, merge_group or push
	author string
	labels []string
	mine   []fakeJob // this run's jobs
	main   []fakeJob // the jobs of main's run 200
	logs   map[int]string
	// main's runs, newest first. Default: run 200 alone, concluded failure
	// when a job in main failed and success otherwise.
	runs  []fakeRun
	other map[int][]fakeJob // the jobs of main's runs other than 200
	// jq writes CRLF line ends, as a native jq.exe does under Git Bash.
	crlfJQ bool
}

const repo = "o/r"

func failedJob(id int, name, step string) fakeJob {
	return fakeJob{ID: id, Name: name, Conclusion: "failure", Steps: []fakeStep{{"Set up job", "success"}, {step, "failure"}}}
}

func passedJob(id int, name string) fakeJob {
	return fakeJob{ID: id, Name: name, Conclusion: "success", Steps: []fakeStep{{"Set up job", "success"}}}
}

// A go test log line as the Actions log API returns it: timestamped.
func failLine(test string) string {
	return "2026-10-21T12:00:00.0000000Z --- FAIL: " + test + " (0.01s)\n" +
		"2026-10-21T12:00:00.0000000Z     --- FAIL: " + test + "/subtest (0.00s)\n"
}

// The same failure printed unnested, as a parallel -short shard prints it:
// the subtest's line at column 0, after its parent's.
func unnestedFailLine(test string) string {
	return "2026-10-21T12:00:00.0000000Z --- FAIL: " + test + "/subtest (0.00s)\n" +
		"2026-10-21T12:00:00.0000000Z --- FAIL: " + test + " (0.01s)\n"
}

func runMainRed(t *testing.T, c mainRedCase) (string, bool) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "comm", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is not on PATH; CI must provide it", tool)
			}
			t.Skipf("%s is not on PATH", tool)
		}
	}
	script, err := os.ReadFile(mainRedScript)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin, fixtures := filepath.Join(dir, "bin"), filepath.Join(dir, "fixtures")
	for _, d := range []string{bin, fixtures} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	fixture := func(apiPath string, v any) {
		t.Helper()
		var data []byte
		if s, ok := v.(string); ok {
			data = []byte(s)
		} else if data, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(fixtures, strings.ReplaceAll(apiPath, "/", "_")), data, 0o644)
	}
	write(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755)
	write(filepath.Join(dir, "ci-main-red.sh"), script, 0o755)

	var labels []map[string]string
	for _, l := range c.labels {
		labels = append(labels, map[string]string{"name": l})
	}
	author := c.author
	if author == "" {
		author = "reliant-01"
	}
	fixture("repos/"+repo+"/pulls/42", map[string]any{"number": 42, "user": map[string]string{"login": author}, "labels": labels})
	fixture("repos/"+repo+"/actions/runs/100/jobs", map[string]any{"jobs": c.mine})
	runs := c.runs
	if runs == nil {
		conclusion := "success"
		for _, j := range c.main {
			if j.Conclusion == "failure" {
				conclusion = "failure"
			}
		}
		runs = []fakeRun{{ID: 200, Conclusion: conclusion}}
	}
	fixture("repos/"+repo+"/actions/workflows/ci.yml/runs", map[string]any{"workflow_runs": runs})
	fixture("repos/"+repo+"/actions/runs/200/jobs", map[string]any{"jobs": c.main})
	for id, jobs := range c.other {
		fixture("repos/"+repo+"/actions/runs/"+strconv.Itoa(id)+"/jobs", map[string]any{"jobs": jobs})
	}
	if c.crlfJQ {
		jq, err := exec.LookPath("jq")
		if err != nil {
			t.Fatal(err)
		}
		shim := "#!/usr/bin/env bash\nset -o pipefail\n'" + filepath.ToSlash(jq) + "' \"$@\" | sed 's/$/\\r/'\n"
		write(filepath.Join(bin, "jq"), []byte(shim), 0o755)
	}
	for id, log := range c.logs {
		fixture("repos/"+repo+"/actions/jobs/"+strconv.Itoa(id)+"/logs", log)
	}

	event := map[string]any{"pull_request": map[string]int{"number": 42}}
	if c.event == "merge_group" {
		event = map[string]any{"merge_group": map[string]string{"head_ref": "refs/heads/gh-readonly-queue/main/pr-42-0123abcd"}}
	}
	evData, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	evPath := filepath.Join(dir, "event.json")
	write(evPath, evData, 0o644)

	cmd := exec.Command("bash", filepath.Join(dir, "ci-main-red.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_GH_DIR="+fixtures,
		"GITHUB_REPOSITORY="+repo,
		"GITHUB_RUN_ID=100",
		"GITHUB_EVENT_NAME="+c.event,
		"GITHUB_EVENT_PATH="+evPath,
		"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary.md"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("run ci-main-red.sh: %v", err)
		}
	}
	return string(out), err == nil
}

// TestMainRedOverride pins what the gate's one escape hatch accepts: a
// labelled, non-Dependabot pull request or queue entry whose every failure is
// already failing on main. Everything else must stay red.
//
// Each case forks a few dozen jq/sed processes, ~0.2s apiece on a loaded
// machine, so -short (the pull-request tier and the flake gate) runs the
// cases marked short: the accept path and the two refusals a mistake is most
// likely to break. task test runs every case.
func TestMainRedOverride(t *testing.T) {
	t.Parallel()
	gate := fakeJob{ID: 9, Name: "CI gate"}
	mainRed := []fakeJob{failedJob(21, "Test", "Go tests"), failedJob(22, "Lint (golangci-lint, go vet, -tags e2e)", "golangci-lint"), passedJob(23, "Verify Generated Code + Validate KCL")}
	mainLogs := map[int]string{21: failLine("TestOnMain") + failLine("TestAlsoOnMain")}

	for _, tc := range []struct {
		name   string
		short  bool // also runs under -short
		c      mainRedCase
		accept bool
		want   string
	}{
		{
			name: "a failing test that also fails on main", short: true,
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), passedJob(12, "Lint (golangci-lint, go vet, -tags e2e)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")})},
			accept: true, want: "tests: TestOnMain",
		},
		{
			name: "the same, through a jq that writes CRLF (Windows)", short: true,
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"}, crlfJQ: true,
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")})},
			accept: true, want: "tests: TestOnMain",
		},
		{
			// A push to main cancels the run queued before it, so main's
			// newest completed run is often cancelled (25283f30's, which
			// refused #1071 as "green"). It and an unfinished run are skipped.
			name: "main's newest runs are unfinished and cancelled", short: true,
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")}),
				runs: []fakeRun{{ID: 301}, {ID: 300, Conclusion: "cancelled"}, {ID: 200, Conclusion: "failure"}},
				other: map[int][]fakeJob{
					301: {{ID: 41, Name: "Test"}},
					300: {passedJob(31, "Quick checks"), {ID: 32, Name: "Test", Conclusion: "cancelled"}},
				}},
			accept: true, want: "every failure also fails on main (run 200)",
		},
		{
			name: "no run on main finished green or red",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")}),
				runs:  []fakeRun{{ID: 300, Conclusion: "cancelled"}},
				other: map[int][]fakeJob{300: {{ID: 32, Name: "Test", Conclusion: "cancelled"}}}},
			want: "found no ci.yml run on main that finished green or red",
		},
		{
			name: "the same, as a merge-queue entry",
			c: mainRedCase{event: "merge_group", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")})},
			accept: true, want: "accepted for #42",
		},
		{
			name: "a non-test job that failed at the same step on main",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(13, "Lint (golangci-lint, go vet, -tags e2e)", "golangci-lint"), gate},
				main: mainRed, logs: mainLogs},
			accept: true, want: "job and step: golangci-lint",
		},
		{
			name: "a failing test main does not fail", short: true,
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain") + failLine("TestNew")})},
			want: "TestNew TestNew/subtest do not fail on main",
		},
		{
			// The subtest is the same test whether or not go test nested
			// its line under the parent's.
			name: "a failing subtest main fails nested, printed unnested here", short: true,
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: unnestedFailLine("TestOnMain")})},
			accept: true, want: "tests: TestOnMain TestOnMain/subtest",
		},
		{
			// ...and a subtest main does not fail is new, even under a
			// parent that main fails.
			name: "a new subtest of a test main fails",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain") + "--- FAIL: TestOnMain/other (0.00s)\n"})},
			want: "TestOnMain/other do not fail on main",
		},
		{
			name: "a non-test job that passed on main",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(14, "Verify Generated Code + Validate KCL", "Verify generated code"), gate},
				main: mainRed, logs: mainLogs},
			want: "passed on main",
		},
		{
			name: "a non-test job that failed at another step on main",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(13, "Lint (golangci-lint, go vet, -tags e2e)", "go vet ./..."), gate},
				main: mainRed, logs: mainLogs},
			want: "failed at [go vet ./...] here, at [golangci-lint] on main",
		},
		{
			name: "no label",
			c: mainRedCase{event: "pull_request",
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")})},
			want: "has no ci:main-red label",
		},
		{
			name: "a Dependabot pull request", short: true,
			c: mainRedCase{event: "pull_request", author: "dependabot[bot]", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: mainRed, logs: merge(mainLogs, map[int]string{11: failLine("TestOnMain")})},
			want: "Dependabot",
		},
		{
			name: "a cancelled job",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{{ID: 15, Name: "Windows (cross-compile + vet)", Conclusion: "cancelled"}, gate},
				main: mainRed, logs: mainLogs},
			want: "was cancelled",
		},
		{
			name: "main is green",
			c: mainRedCase{event: "pull_request", labels: []string{"ci:main-red"},
				mine: []fakeJob{failedJob(11, "Test (-short) 1/2", "Go tests (-short)"), gate},
				main: []fakeJob{passedJob(21, "Test")}, logs: map[int]string{11: failLine("TestOnMain")}},
			want: "is green",
		},
		{
			name: "a push to main",
			c:    mainRedCase{event: "push", labels: []string{"ci:main-red"}},
			want: "judged as it is",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if testing.Short() && !tc.short {
				t.Skip("forks a few dozen processes; runs in task test")
			}
			t.Parallel()
			out, accepted := runMainRed(t, tc.c)
			if accepted != tc.accept {
				t.Errorf("accepted = %v, want %v; output:\n%s", accepted, tc.accept, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not say %q:\n%s", tc.want, out)
			}
		})
	}
}

func merge(a, b map[int]string) map[int]string {
	out := map[int]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
