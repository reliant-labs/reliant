package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/exitcode"
)

const covBGateYAML = `name: gatedemo
module_path: example.com/gatedemo
ci:
  gate:
    extra_checks:
      - name: say-ok
        command: [sh, -c, "echo all good"]
        group: demo
      - name: say-boom
        command: [sh, -c, "echo kaboom >&2; exit 3"]
        group: demo
      - name: needs-ghost
        command: [sh, -c, "true"]
        requires: [definitely-not-a-binary-forge-test]
      - name: nightly-only
        command: [sh, -c, "true"]
        lane: docker
`

// covBGateProject is a tiny project whose gate has only shell extra checks,
// pinned as the project dir, with the verdict cache and logs outside it.
func covBGateProject(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture's extra checks are POSIX sh commands")
	}
	covBRequireTool(t, "sh")
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "forge.yaml"), covBGateYAML)
	if err := cmdutil.SetProjectDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })
	t.Setenv(cigate.CacheDirEnv, t.TempDir())
	t.Setenv("CI_LOG_DIR", filepath.Join(t.TempDir(), "logs"))
	for _, e := range []string{cigate.OnlyEnv, cigate.SkipEnv, cigate.FullEnv, cigate.BaseEnv, cigate.NoCacheEnv, "CI_REPORT", "GITHUB_STEP_SUMMARY", "GITHUB_ACTIONS"} {
		t.Setenv(e, "")
	}
	return dir
}

func covBRunGate(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newCIGateCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestCIGateListPrintsSchedulingNotes(t *testing.T) {
	covBGateProject(t)
	out, _, err := covBRunGate(t, "checks", "--list", "--only", "say-ok,say-boom,needs-ghost", "--skip", "say-boom")
	if err != nil {
		t.Fatalf("gate --list: %v", err)
	}
	for _, name := range []string{"say-ok", "say-boom", "needs-ghost"} {
		if !strings.Contains(out, name) {
			t.Errorf("--list missing %q:\n%s", name, out)
		}
	}
	if strings.Contains(out, "nightly-only") {
		t.Errorf("a custom lane never appears in the checks lane:\n%s", out)
	}
	if !strings.Contains(out, "demo") {
		t.Errorf("--list must show each check's group:\n%s", out)
	}

	out, _, err = covBRunGate(t, "docker", "--list")
	if err != nil || !strings.Contains(out, "nightly-only") {
		t.Fatalf("gate docker --list = %q, %v; want the project's own lane", out, err)
	}
}

func TestCIGateRunsSelectedChecksAndReports(t *testing.T) {
	dir := covBGateProject(t)
	report := filepath.Join(t.TempDir(), "report.md")
	summary := filepath.Join(t.TempDir(), "summary.md")
	mustWriteFile(t, summary, "earlier step\n")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)

	// One passing and one failing check: every selected check runs, the
	// table says which failed, and the verdict is the gate's error.
	out, _, err := covBRunGate(t, "checks", "--only", "say-ok,say-boom", "--no-cache", "--report", report, "--jobs", "2")
	var fail gateFailure
	if err == nil || err.Error() != fail.Error() {
		t.Fatalf("a failing check must fail the gate with gateFailure, got %v", err)
	}
	for _, want := range []string{"CI gate summary (checks)", "say-ok", "PASS", "say-boom", "FAIL", "verdict cache: off (--no-cache)", "heavy slots:"} {
		if !strings.Contains(out, want) {
			t.Errorf("gate output missing %q:\n%s", want, out)
		}
	}
	md, rerr := os.ReadFile(report)
	if rerr != nil || !strings.Contains(string(md), "### CI gate — checks") || !strings.Contains(string(md), "kaboom") {
		t.Errorf("--report must hold this run's table with the failing tail: %q, %v", md, rerr)
	}
	if b, _ := os.ReadFile(summary); !strings.HasPrefix(string(b), "earlier step\n") || !strings.Contains(string(b), "### CI gate — checks") {
		t.Errorf("$GITHUB_STEP_SUMMARY must be appended to, not truncated: %q", b)
	}
	logs, _ := os.ReadFile(filepath.Join(os.Getenv("CI_LOG_DIR"), "say-boom.log"))
	if !strings.Contains(string(logs), "kaboom") {
		t.Errorf("each check's output goes to CI_LOG_DIR/<check>.log, got %q", logs)
	}
	_ = dir
}

func TestCIGateJSONDocumentForARun(t *testing.T) {
	covBGateProject(t)
	out, stderr, err := covBRunGate(t, "checks", "--only", "say-ok,needs-ghost", "--json", "--no-cache")
	if err == nil {
		t.Fatal("a SKIPPED check (missing binary) must fail the gate")
	}
	var doc cigate.Document
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout must be exactly one document: %v\n%s", jerr, out)
	}
	if doc.Lane != "checks" || doc.OK || doc.ExitCode != exitcode.Of(err) || doc.Verdict == "" {
		t.Errorf("document header = %+v for %v", doc, err)
	}
	status := map[string]string{}
	for _, c := range doc.Checks {
		status[c.Name] = string(c.Status)
	}
	if status["say-ok"] != "pass" || status["needs-ghost"] != "skip" {
		t.Errorf("statuses = %v; want say-ok pass, needs-ghost skip", status)
	}
	if !strings.Contains(stderr, "CI gate summary") {
		t.Errorf("under --json the table goes to stderr:\n%s", stderr)
	}

	// CI_SKIP waives the skip: the same run then passes.
	t.Setenv(cigate.SkipEnv, "needs-ghost")
	out, _, err = covBRunGate(t, "checks", "--only", "say-ok,needs-ghost", "--json", "--no-cache")
	if err != nil {
		t.Fatalf("CI_SKIP=needs-ghost must let the gate pass: %v", err)
	}
	var ok cigate.Document
	if json.Unmarshal([]byte(out), &ok) != nil || !ok.OK {
		t.Errorf("document = %s", out)
	}
}

func TestCIGateRefusesNamesThatMatchNothing(t *testing.T) {
	covBGateProject(t)
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"typo in --only", []string{"checks", "--only", "say-okk"}, nil, "names no check or group of lane \"checks\": say-okk"},
		{"typo in CI_ONLY", []string{"checks"}, map[string]string{cigate.OnlyEnv: "nope"}, "CI_ONLY names no check or group"},
		{"typo in --skip", []string{"checks", "--skip", "lint-ish"}, nil, "CI_SKIP names no check: lint-ish"},
		{"unknown lane", []string{"lint"}, nil, `unknown lane "lint"`},
		{"unknown tier", []string{"--tier", "weekly"}, nil, `unknown tier "weekly"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, _, err := covBRunGate(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestCIGateTierMarksUnrunLanesDeferred(t *testing.T) {
	covBGateProject(t)
	out, _, err := covBRunGate(t, "checks", "--tier", "pr", "--list", "--only", "say-ok")
	if err != nil {
		t.Fatalf("--tier pr --list: %v", err)
	}
	if !strings.Contains(out, "say-ok") {
		t.Errorf("a project's extra check stays in every tier:\n%s", out)
	}
	out, _, err = covBRunGate(t, "checks", "--event", "pull_request", "--only", "say-ok", "--no-cache")
	if err != nil {
		t.Fatalf("pr-tier run: %v", err)
	}
	if !strings.Contains(out, "tier: pr — ") {
		t.Errorf("a tiered run says which tier it is:\n%s", out)
	}
}

func TestPlanGateSelectsLanesTiersAndNames(t *testing.T) {
	all := []cigate.Check{
		{Name: "a", Lane: cigate.LaneChecks, Group: "g1"},
		{Name: "b", Lane: cigate.LaneChecks, Group: "g2", After: []string{"a"}},
		{Name: "t", Lane: cigate.LaneTest},
		{Name: "dock", Lane: "docker"},
	}
	names := func(cs []cigate.Check) string {
		var n []string
		for _, c := range cs {
			n = append(n, c.Name)
		}
		return strings.Join(n, ",")
	}
	t.Setenv(cigate.OnlyEnv, "")
	t.Setenv(cigate.SkipEnv, "")

	p, err := planGate(all, nil, gateFlags{}, cigate.Tier{}, false)
	if err != nil || names(p.selected) != "a,b,t" || p.label != "all" {
		t.Fatalf("default plan = %q (%s), %v; want the three built-in lanes, never a custom one", names(p.selected), p.label, err)
	}
	p, err = planGate(all, []string{"docker"}, gateFlags{}, cigate.Tier{}, false)
	if err != nil || names(p.selected) != "dock" {
		t.Fatalf("docker lane = %q, %v", names(p.selected), err)
	}
	// Selecting a check by its group pulls in what it runs after.
	p, err = planGate(all, []string{"checks"}, gateFlags{only: []string{"g2"}}, cigate.Tier{}, false)
	if err != nil || names(p.selected) != "a,b" {
		t.Fatalf("--only g2 = %q, %v; want b and its dependency a", names(p.selected), err)
	}
	// CI_ONLY and --only are additive; CI_SKIP and --skip too.
	t.Setenv(cigate.OnlyEnv, "t")
	t.Setenv(cigate.SkipEnv, "a")
	p, err = planGate(all, nil, gateFlags{only: []string{"b"}, skip: []string{"t"}}, cigate.Tier{}, false)
	if err != nil || names(p.selected) != "a,b,t" || strings.Join(p.skips, ",") != "a,t" {
		t.Fatalf("combined env + flags = %q skips %v, %v", names(p.selected), p.skips, err)
	}
	// --list tolerates a skip name only a different lane knows.
	t.Setenv(cigate.SkipEnv, "")
	if _, err = planGate(all, nil, gateFlags{skip: []string{"ghost"}, list: true}, cigate.Tier{}, false); err != nil {
		t.Errorf("--list must not refuse an unknown --skip: %v", err)
	}
	if _, err = planGate(all, nil, gateFlags{skip: []string{"ghost"}}, cigate.Tier{}, false); err == nil || !strings.Contains(err.Error(), "valid names: a, b, t, dock") {
		t.Errorf("an unknown skip must list the valid names, got %v", err)
	}
}

func TestWriteGateListNotes(t *testing.T) {
	var b bytes.Buffer
	writeGateList(&b, []cigate.Check{
		{Name: "plain", Lane: cigate.LaneChecks, Group: "g"},
		{Name: "full", Lane: cigate.LaneTest, Group: "g", Heavy: true, MutatesTree: true, NoCache: true, After: []string{"x", "y"}, NotApplicable: "no frontend", Deferred: "pr tier"},
	})
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("one line per check, got %q", b.String())
	}
	if strings.TrimSpace(lines[0]) != "checks       g            plain" {
		t.Errorf("a check with no notes prints lane, group, name only: %q", lines[0])
	}
	if want := "heavy; mutates-tree; no-cache; after x,y; n/a: no frontend; deferred: pr tier"; !strings.HasSuffix(lines[1], want) {
		t.Errorf("notes = %q, want suffix %q", lines[1], want)
	}
}

func TestGateLogDirPrefersIgnoredLocations(t *testing.T) {
	covBRequireTool(t, "git")
	root := t.TempDir()
	t.Setenv("CI_LOG_DIR", "")
	if got, want := gateLogDir(root, "checks"), filepath.Join(root, ".forge", "ci", "checks"); got != want {
		t.Errorf("outside a git repo the fallback is %s, got %s", want, got)
	}
	covBGit(t, root, "init", "-q")
	if got, want := gateLogDir(root, "checks"), filepath.Join(root, ".forge", "ci", "checks"); got != want {
		t.Errorf("a repo that ignores nothing falls back to %s, got %s", want, got)
	}
	mustWriteFile(t, filepath.Join(root, ".gitignore"), "tmp/\n")
	if got, want := gateLogDir(root, "test"), filepath.Join(root, "tmp", "ci", "test"); got != want {
		t.Errorf("tmp/ ignored: want %s, got %s", want, got)
	}
	t.Setenv("CI_LOG_DIR", "custom/logs")
	if got, want := gateLogDir(root, "test"), filepath.Join(root, "custom", "logs"); got != want {
		t.Errorf("a relative CI_LOG_DIR resolves against the project root: want %s, got %s", want, got)
	}
	abs := t.TempDir()
	t.Setenv("CI_LOG_DIR", abs)
	if got := gateLogDir(root, "test"); got != abs {
		t.Errorf("an absolute CI_LOG_DIR is used as given: %s", got)
	}
}

func TestGitHeadReportsCommitAndDirtiness(t *testing.T) {
	covBRequireTool(t, "git")
	root := t.TempDir()
	if head, dirty := gitHead(root); head != "" || dirty {
		t.Errorf("outside a repo: %q, %v", head, dirty)
	}
	covBGit(t, root, "init", "-q")
	mustWriteFile(t, filepath.Join(root, "a.txt"), "a")
	covBGit(t, root, "add", ".")
	covBGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-qm", "init")
	head, dirty := gitHead(root)
	if len(head) < 4 || dirty {
		t.Errorf("a clean checkout = %q, %v", head, dirty)
	}
	mustWriteFile(t, filepath.Join(root, "b.txt"), "b")
	if h2, dirty := gitHead(root); h2 != head || !dirty {
		t.Errorf("an untracked file makes the tree dirty at the same head: %q, %v", h2, dirty)
	}
}

func covBGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGit(t, dir, args...)
}

func TestGateSlotsHonoursTheHeavyLimit(t *testing.T) {
	var out, errb bytes.Buffer
	t.Setenv("FORGE_HEAVY_SLOTS", "off")
	if f := gateSlots(&out, &errb); f != nil || !strings.Contains(out.String(), "heavy slots: off (FORGE_HEAVY_SLOTS=off)") {
		t.Errorf("FORGE_HEAVY_SLOTS=off: slot func %v, out %q", f != nil, out.String())
	}
	out.Reset()
	t.Setenv("FORGE_HEAVY_SLOTS", "banana")
	if f := gateSlots(&out, &errb); f != nil || !strings.Contains(errb.String(), "heavy slots:") || !strings.Contains(errb.String(), "banana") {
		t.Errorf("a malformed limit must warn and fall back: slot func %v, err %q", f != nil, errb.String())
	}
}

func TestLicenseWarnFirstFollowsTheDial(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "p"}
	cfg.Lint.Rules = map[string]string{"license-check": "error"}
	if got := licenseWarnFirst(cfg); got != "" {
		t.Errorf("a rule dialed to error gates, got %q", got)
	}
	cfg.Lint.Rules = map[string]string{"license-check": "warn"}
	got := licenseWarnFirst(cfg)
	if !strings.Contains(got, "does not gate yet") || !strings.Contains(got, "license-check") {
		t.Errorf("a rule dialed to warn names the dial that lowered it, got %q", got)
	}
	if got := licenseWarnFirst(nil); got != "" {
		t.Errorf("no config gates, got %q", got)
	}
}

func covBRequireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not on PATH", name)
	}
}
