package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goTestJSON fabricates a `go test -json` stream: one package, `pass` passing
// tests and `skip` skipped ones.
func goTestJSON(pkg string, pass, skip int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"Action":"start","Package":%q}`+"\n", pkg)
	for i := range pass {
		fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Test":"TestPass%02d"}`+"\n", pkg, i)
	}
	for i := range skip {
		fmt.Fprintf(&b, `{"Action":"skip","Package":%q,"Test":"TestSkip%02d"}`+"\n", pkg, i)
	}
	fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Elapsed":0.2}`+"\n", pkg)
	return b.String()
}

// runVerifyTestRun drives the command with an injected stream and returns its
// combined output plus the error.
func runVerifyTestRun(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newCIVerifyTestRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// writeProject drops a minimal forge.yaml (plus any extra YAML) and chdirs to it.
func writeProject(t *testing.T, extra string) string {
	t.Helper()
	root := t.TempDir()
	// The `ci` group is gated on the ci feature, which derives on for any
	// project that is not a library — so give the project a binary.
	if err := os.MkdirAll(filepath.Join(root, "cmd", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "app", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "name: app\nmodule_path: example.com/app\n" + extra
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root
}

// A package that skipped 90% of its tests fails the gate and is NAMED. This is
// the reliant internal/threads shape: `go test` exits 0 either way, so if forge
// does not say it, nothing does.
func TestCIVerifyTestRun_MassSkipFailsAndNamesThePackage(t *testing.T) {
	writeProject(t, "")

	out, err := runVerifyTestRun(t, goTestJSON("example.com/app/internal/threads", 9, 91))

	if err == nil {
		t.Fatalf("a 91%%-skipped package exited 0:\n%s", out)
	}
	if !strings.Contains(out, "internal/threads") || !strings.Contains(out, "91 of 100") {
		t.Errorf("report does not name the package and the loss:\n%s", out)
	}
}

// The healthy shape of the same package — 1 legitimate skip out of 173 —
// passes, and the pass STATES what it verified.
func TestCIVerifyTestRun_AFewLegitimateSkipsPass(t *testing.T) {
	writeProject(t, "")

	out, err := runVerifyTestRun(t, goTestJSON("example.com/app/internal/threads", 172, 1))

	if err != nil {
		t.Fatalf("1 skip out of 173 failed the gate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "173 test(s)") || !strings.Contains(out, "172 passed") {
		t.Errorf("the pass line must state what it verified:\n%s", out)
	}
}

// A declared exemption in forge.yaml silences a legitimate mass-skip, and the
// declared reason is echoed so the suppression is visible rather than silent.
func TestCIVerifyTestRun_DeclaredExemptionInForgeYAML(t *testing.T) {
	writeProject(t, `ci:
  test_skips:
    allow:
      - package: internal/dockerintegration
        reason: "every test here needs a live docker daemon"
`)

	out, err := runVerifyTestRun(t, goTestJSON("example.com/app/internal/dockerintegration", 0, 30))

	if err != nil {
		t.Fatalf("declared exemption did not apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "live docker daemon") {
		t.Errorf("a suppression must remain visible, with its reason:\n%s", out)
	}
}

// An exemption with no reason is a configuration ERROR, not a silently ignored
// line: a suppression that did not parse is indistinguishable from one that
// worked, right up until it is not.
func TestCIVerifyTestRun_ExemptionWithoutAReasonIsAConfigError(t *testing.T) {
	writeProject(t, `ci:
  test_skips:
    allow:
      - package: internal/whatever
`)

	out, err := runVerifyTestRun(t, goTestJSON("example.com/app/internal/whatever", 50, 1))

	if err == nil {
		t.Fatalf("an exemption with no reason was accepted:\n%s", out)
	}
	if !strings.Contains(out+err.Error(), "reason") {
		t.Errorf("the error must name the missing key:\n%s\n%v", out, err)
	}
}

// forge.yaml tunes the threshold, and an explicit flag beats forge.yaml — but
// only when the user actually typed it.
func TestCIVerifyTestRun_ThresholdLayering(t *testing.T) {
	writeProject(t, `ci:
  test_skips:
    max_skip_ratio: 0.9
`)
	stream := goTestJSON("example.com/app/internal/x", 30, 70) // 70%

	if _, err := runVerifyTestRun(t, stream); err != nil {
		t.Fatalf("forge.yaml's 0.9 ceiling was ignored (70%% should pass): %v", err)
	}
	if _, err := runVerifyTestRun(t, stream, "--max-skip-ratio", "0.5"); err == nil {
		t.Fatal("an explicit --max-skip-ratio did not override forge.yaml")
	}
}

// --warn-only is an adoption ramp for FINDINGS. It is not permission to report
// a pass over facts forge never obtained, so UNDETERMINED still fails.
func TestCIVerifyTestRun_WarnOnlyDowngradesFindingsButNotUndetermined(t *testing.T) {
	writeProject(t, "")

	out, err := runVerifyTestRun(t, goTestJSON("example.com/app/internal/threads", 9, 91), "--warn-only")
	if err != nil {
		t.Fatalf("--warn-only still failed on a finding: %v\n%s", err, out)
	}
	if !strings.Contains(out, "internal/threads") {
		t.Errorf("--warn-only must still REPORT:\n%s", out)
	}

	out, err = runVerifyTestRun(t, "ok  \texample.com/app/internal/threads\t0.4s\n", "--warn-only")
	if err == nil {
		t.Fatalf("--warn-only turned UNDETERMINED into a pass:\n%s", out)
	}
	if !strings.Contains(out, "UNDETERMINED") {
		t.Errorf("output must say UNDETERMINED:\n%s", out)
	}
}

// Plain `go test` output (no -json) is UNDETERMINED, never a pass — the exact
// mistake a copy-pasted pipeline makes.
func TestCIVerifyTestRun_NonJSONInputIsUndetermined(t *testing.T) {
	writeProject(t, "")

	out, err := runVerifyTestRun(t, "ok  \texample.com/app/internal/a\t0.4s\nok  \texample.com/app/internal/b\t0.1s\n")

	if err == nil {
		t.Fatalf("non-JSON input reported a pass:\n%s", out)
	}
	if !strings.Contains(err.Error(), "UNDETERMINED") {
		t.Errorf("the error must be an UNDETERMINED, not a finding: %v", err)
	}
	if strings.Contains(out, "✅") {
		t.Errorf("no pass mark may appear:\n%s", out)
	}
}

// An empty stream is the absence of facts, not a clean run.
func TestCIVerifyTestRun_EmptyInputIsNotAPass(t *testing.T) {
	writeProject(t, "")

	out, err := runVerifyTestRun(t, "")

	if err == nil {
		t.Fatalf("an empty stream reported a pass:\n%s", out)
	}
	if !strings.Contains(err.Error(), "UNDETERMINED") {
		t.Errorf("want an UNDETERMINED error, got: %v", err)
	}
}

// --from a path that does not exist names the path instead of hanging or
// passing.
func TestCIVerifyTestRun_MissingFromFile(t *testing.T) {
	writeProject(t, "")

	_, err := runVerifyTestRun(t, "", "--from", filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("a missing --from file reported a pass")
	}
	if !strings.Contains(err.Error(), "nope.json") {
		t.Errorf("the error must name the path: %v", err)
	}
}

// The command works outside a forge project — its input is a Go fact, not a
// forge fact — and says so, since declared exemptions live in forge.yaml and a
// user whose exemption did not apply must not have to guess why.
func TestCIVerifyTestRun_WorksWithoutAForgeProject(t *testing.T) {
	t.Chdir(t.TempDir())

	out, err := runVerifyTestRun(t, goTestJSON("example.com/app/internal/ok", 50, 1))
	if err != nil {
		t.Fatalf("no forge.yaml made the command unusable: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No forge.yaml") {
		t.Errorf("the fallback to defaults must be stated:\n%s", out)
	}
}

// A red suite piped in must fail the command. In a shell without
// `set -o pipefail`, `go test -json ./... | forge ci verify-test-run` reports
// only forge's status — a checker that ignored failures would launder it green.
func TestCIVerifyTestRun_FailuresInTheStreamFailTheCommand(t *testing.T) {
	writeProject(t, "")

	stream := `{"Action":"start","Package":"example.com/app/internal/x"}` + "\n" +
		`{"Action":"pass","Package":"example.com/app/internal/x","Test":"TestA"}` + "\n" +
		`{"Action":"fail","Package":"example.com/app/internal/x","Test":"TestB"}` + "\n" +
		`{"Action":"fail","Package":"example.com/app/internal/x","Elapsed":0.2}` + "\n"

	out, err := runVerifyTestRun(t, stream, "--warn-only")
	if err == nil {
		t.Fatalf("a failing suite exited 0 (pipefail laundering):\n%s", out)
	}
	if !strings.Contains(err.Error(), "pipefail") {
		t.Errorf("the fix line should mention the pipefail trap: %v", err)
	}
}

// `forge ci verify-test-run` must be reachable from the ci group, including
// under the spellings people will guess.
func TestCIVerifyTestRun_IsRegisteredUnderCI(t *testing.T) {
	for _, name := range []string{"verify-test-run", "verify-tests", "test-skips"} {
		found, _, err := newCICmd().Find([]string{name})
		if err != nil || found == nil || found.Name() != "verify-test-run" {
			t.Errorf("`forge ci %s` does not resolve to verify-test-run (got %v, err %v)", name, found, err)
		}
	}
}
