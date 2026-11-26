package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/exitcode"
)

func TestGateLanes(t *testing.T) {
	known := []cigate.Lane{cigate.LaneChecks, cigate.LaneTest, cigate.LaneIntegration, "docker"}
	for _, c := range []struct {
		args []string
		want []cigate.Lane
	}{
		{nil, []cigate.Lane{cigate.LaneChecks, cigate.LaneTest, cigate.LaneIntegration}},
		{[]string{"all"}, []cigate.Lane{cigate.LaneChecks, cigate.LaneTest, cigate.LaneIntegration}},
		{[]string{"checks"}, []cigate.Lane{cigate.LaneChecks}},
		{[]string{"test"}, []cigate.Lane{cigate.LaneTest}},
		{[]string{"integration"}, []cigate.Lane{cigate.LaneIntegration}},
		// A lane a project's extra checks declare runs by name only.
		{[]string{"docker"}, []cigate.Lane{"docker"}},
	} {
		got, _, err := gateLanes(c.args, known)
		if err != nil || len(got) != len(c.want) {
			t.Errorf("gateLanes(%v) = %v, %v; want %v", c.args, got, err, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("gateLanes(%v)[%d] = %s, want %s", c.args, i, got[i], c.want[i])
			}
		}
	}
	if _, _, err := gateLanes([]string{"lint"}, known); err == nil || !strings.Contains(err.Error(), "all, checks, test, integration, docker") {
		t.Errorf("an unknown lane must name the valid ones, got %v", err)
	}
}

// The job summary: a table appended (not truncated) to $GITHUB_STEP_SUMMARY, so
// the gate's output sits beside whatever earlier steps wrote. CI_REPORT, by
// contrast, is this run's table alone.
func TestAppendStepSummaryAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(path, []byte("earlier step\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := cigate.Report{Lane: "checks", Results: []cigate.Result{
		{Name: "go-build", Status: cigate.Pass},
		{Name: "vuln-go", Status: cigate.Skip, Note: "govulncheck is not on PATH"},
	}}
	if err := writeReport(path, rep, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	for _, want := range []string{"earlier step", "### CI gate — checks", "| `go-build` | ✅ PASS", "| `vuln-go` | ⏭️ SKIPPED", "❌ **FAIL**"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if err := writeReport(path, rep, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "earlier step") {
		t.Errorf("CI_REPORT must hold this run's table alone:\n%s", b)
	}
}

func TestTruthy(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "YES": true, "0": false, "": false, "no": false} {
		if truthy(v) != want {
			t.Errorf("truthy(%q) = %v", v, !want)
		}
	}
}

// --json always prints a document, even when the gate cannot run: a check run
// reads the verdict from it rather than from a log. The process status is the
// document's exit_code.
func TestCIGateJSONReportsAGateThatCouldNotRun(t *testing.T) {
	root := newTestRootCmd(t)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"ci", "gate", "test", "--tier", "nightly", "--json"})
	err := root.Execute()
	if err == nil {
		t.Fatal("an unknown tier must fail")
	}
	var doc cigate.Document
	if jerr := json.Unmarshal(stdout.Bytes(), &doc); jerr != nil {
		t.Fatalf("stdout is not one document: %v\n%s", jerr, stdout.String())
	}
	if doc.Schema != cigate.DocumentSchema || doc.Verdict != cigate.VerdictError || doc.OK ||
		doc.ExitCode != exitcode.Of(err) || doc.Lane != "test" || len(doc.Checks) != 0 {
		t.Errorf("document %+v for %v", doc, err)
	}
	if !strings.Contains(doc.Error, `unknown tier "nightly"`) {
		t.Errorf("error %q must say why", doc.Error)
	}
}

func TestCIGateTierFlagsAreExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"ci", "gate", "--tier", "pr", "--event", "push"},
		{"ci", "gate", "--list", "--json"},
	} {
		root := newTestRootCmd(t)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "none of the others can be") {
			t.Errorf("%v: want a mutually-exclusive error, got %v", args, err)
		}
	}
}

func TestGateTier(t *testing.T) {
	for _, c := range []struct {
		flags gateFlags
		want  string
	}{
		{gateFlags{}, ""},
		{gateFlags{tier: "merge"}, cigate.TierQueue},
		{gateFlags{event: "pull_request.synchronize"}, cigate.TierPR},
		{gateFlags{event: "push"}, cigate.TierMain},
	} {
		tier, ok, err := gateTier(c.flags)
		if err != nil || tier.Name != c.want || ok != (c.want != "") {
			t.Errorf("gateTier(%+v) = %q, %v, %v; want %q", c.flags, tier.Name, ok, err, c.want)
		}
	}
	if _, _, err := gateTier(gateFlags{event: "issue_comment"}); err == nil {
		t.Error("an event with no tier must be refused")
	}
	help := gateTierHelp()
	for _, tier := range cigate.Tiers() {
		if !strings.Contains(help, tier.Name+" ") || !strings.Contains(help, tier.About) {
			t.Errorf("--help misses tier %s:\n%s", tier.Name, help)
		}
	}
}

// A skipped check fails the gate, so `forge ci gate` is wired into `ci` as a
// verb that exists and rejects stray arguments rather than ignoring them.
func TestCIGateIsRegisteredAndStrictAboutArgs(t *testing.T) {
	root := newTestRootCmd(t)
	cmd, _, err := root.Find([]string{"ci", "gate"})
	if err != nil || cmd == nil || cmd.Name() != "gate" {
		t.Fatalf("forge ci gate is not registered: %v", err)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"ci", "gate", "checks", "extra"})
	if err := root.Execute(); err == nil {
		t.Error("a second positional argument should be rejected")
	}
	if (gateFailure{}).Error() == "" {
		t.Error("gateFailure must say something: main prints it")
	}
}
