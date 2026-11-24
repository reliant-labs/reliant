package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/testflakes"
	"github.com/reliant-labs/forge/pkg/testreport"
)

func TestTestFlakesAndQuarantineAreRegistered(t *testing.T) {
	root := newTestRootCmd(t)
	for _, path := range [][]string{
		{"test", "flakes"}, {"test", "flakes", "gate"}, {"test", "flakes", "confirm"},
		{"test", "quarantine", "add"}, {"test", "quarantine", "remove"},
		{"test", "quarantine", "list"}, {"test", "quarantine", "skip-regex"},
	} {
		found, _, err := root.Find(path)
		if err != nil || found.Name() != path[len(path)-1] {
			t.Errorf("forge %s is not registered: %v", strings.Join(path, " "), err)
		}
	}
}

func flakesModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func execTestCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newTestCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String() + errb.String(), err
}

func TestTestQuarantineCommands(t *testing.T) {
	root := flakesModule(t)
	t.Chdir(root)
	t.Setenv(testflakes.EnvQuarantineExtra, "")

	out, err := execTestCmd(t, "quarantine", "add", "./internal/billing:TestRefund/partial", "--issue", "https://github.com/acme/m/issues/3")
	if err != nil || !strings.Contains(out, "ci/quarantine/go/internal/billing/TestRefund.yaml") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, _ := execTestCmd(t, "quarantine", "add", "go:example.com/m/internal/billing:TestRefund"); !strings.Contains(out, "already quarantined") {
		t.Errorf("re-add: %s", out)
	}
	if out, err := execTestCmd(t, "quarantine", "skip-regex", "go"); err != nil || strings.TrimSpace(out) != "^(TestRefund)$" {
		t.Errorf("skip-regex: %q %v", out, err)
	}
	if _, err := execTestCmd(t, "quarantine", "skip-regex", "ts"); err == nil {
		t.Error("skip-regex ts must refuse")
	}
	out, err = execTestCmd(t, "quarantine", "list", "--json")
	var doc quarantineListDoc
	if err != nil || json.Unmarshal([]byte(out), &doc) != nil || len(doc.Entries) != 1 || doc.Entries[0].Issue != "https://github.com/acme/m/issues/3" || doc.Entries[0].Overdue {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, _ := execTestCmd(t, "quarantine", "list", "--overdue"); !strings.Contains(out, "No overdue") {
		t.Errorf("--overdue: %s", out)
	}
	if out, err := execTestCmd(t, "quarantine", "remove", "internal/billing:TestRefund"); err != nil || !strings.Contains(out, "removed") {
		t.Errorf("remove: %v %s", err, out)
	}
	_, err = execTestCmd(t, "quarantine", "remove", "internal/billing:TestRefund")
	var ec exitCodeError
	if !errors.As(err, &ec) || ec.code != exitWrong {
		t.Errorf("removing what is not quarantined: %v", err)
	}
}

func TestRunTestFlakesJSON(t *testing.T) {
	root := flakesModule(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	exp := now.Add(-time.Hour)
	if _, err := testflakes.QuarantineAdd(root, "example.com/m", testflakes.Entry{Lang: testreport.LangGo, Package: "example.com/m/x", Test: "TestOld", ExpiresAt: exp}, false); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runTestFlakes(t.Context(), &out, testFlakesArgs{root: root, json: true, now: now}); err != nil {
		t.Fatal(err)
	}
	var doc testreport.Flakes
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != testreport.SchemaFlakes || len(doc.Tests) != 1 || doc.Tests[0].State != testreport.StateQuarantined ||
		doc.Tests[0].Quarantine == nil || !doc.Tests[0].Quarantine.Overdue {
		t.Fatalf("doc = %s", out.String())
	}
	if len(doc.Suggested) != 1 || !strings.Contains(doc.Suggested[0], "forge test quarantine remove go:example.com/m/x:TestOld") {
		t.Errorf("suggested = %v", doc.Suggested)
	}

	out.Reset()
	if err := runTestFlakes(t.Context(), &out, testFlakesArgs{root: root, now: now}); err != nil || !strings.Contains(out.String(), "OVERDUE") {
		t.Errorf("text: %v\n%s", err, out.String())
	}
	if err := runTestFlakes(t.Context(), &out, testFlakesArgs{root: root, states: []string{"bogus"}}); err == nil {
		t.Error("an unknown --state must be refused")
	}
}

func TestFlakeGateVerdict(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	failed := testflakes.GateResult{ExitCode: testflakes.GateFailed, Message: "flake gate: FAILED"}
	none := testflakes.GateResult{ExitCode: testflakes.GateUndetermined, Message: "flake gate: no new or edited tests"}

	var ec exitCodeError
	if err := flakeGateVerdict(&out, failed, false, root); !errors.As(err, &ec) || ec.code != 1 {
		t.Errorf("ungraded failure: %v", err)
	}
	if err := flakeGateVerdict(&out, none, false, root); !errors.As(err, &ec) || ec.code != 2 {
		t.Errorf("ungraded no tests: %v", err)
	}
	// --graded with no forge.yaml: a warning, and no tests is a pass.
	if err := flakeGateVerdict(&out, failed, true, root); err != nil || !strings.Contains(out.String(), "flake-check: error") {
		t.Errorf("graded failure without the dial: %v\n%s", err, out.String())
	}
	if err := flakeGateVerdict(&out, none, true, root); err != nil {
		t.Errorf("graded no tests: %v", err)
	}
}

// A selected test command that could not start proved nothing: graded, it
// fails a project that sets flake-check: error and warns one that does not,
// as a failed test does; a change with no new or edited tests still passes.
func TestFlakeGateVerdictGradedFailsWhenTheCommandCannotStart(t *testing.T) {
	notRun := testflakes.GateResult{ExitCode: testflakes.GateUndetermined, CommandFailed: true, Message: "flake gate: could not run go: executable file not found in $PATH"}
	none := testflakes.GateResult{ExitCode: testflakes.GateUndetermined, Message: "flake gate: no new or edited tests in origin/main..HEAD"}

	gated := t.TempDir()
	if err := os.WriteFile(filepath.Join(gated, "forge.yaml"), []byte("name: m\nlint:\n  rules:\n    flake-check: error\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var ec exitCodeError
	if err := flakeGateVerdict(&out, notRun, true, gated); !errors.As(err, &ec) || ec.code != exitWrong {
		t.Errorf("graded, flake-check: error, command could not start: %v, want exit %d", err, exitWrong)
	}
	if err := flakeGateVerdict(&out, none, true, gated); err != nil {
		t.Errorf("graded, flake-check: error, no changed tests: %v, want a pass", err)
	}
	out.Reset()
	if err := flakeGateVerdict(&out, notRun, true, t.TempDir()); err != nil || !strings.Contains(out.String(), "warning: flake gate: could not run go") {
		t.Errorf("graded, advisory, command could not start: %v, want a warning\n%s", err, out.String())
	}
	// Ungraded, the exit code is the gate's own, unchanged.
	if err := flakeGateVerdict(&out, notRun, false, gated); !errors.As(err, &ec) || ec.code != testflakes.GateUndetermined {
		t.Errorf("ungraded: %v, want exit %d", err, testflakes.GateUndetermined)
	}
}
