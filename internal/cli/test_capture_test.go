package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/forge/pkg/testreport"
)

func TestCaptureVerdictIsTheCommandsStatus(t *testing.T) {
	t.Parallel()
	if err := captureVerdict(testcapture.CaptureResult{ExitCode: 0}, "unit", []string{"go"}); err != nil {
		t.Fatalf("a passing command: %v, want nil", err)
	}
	err := captureVerdict(testcapture.CaptureResult{ExitCode: 3, LaneDir: "/r/unit"}, "unit", []string{"go"})
	if exitcode.Of(err) != 3 {
		t.Fatalf("exit %d, want the command's 3", exitcode.Of(err))
	}
	// The command already reported its failure: capture's line is a
	// verdict, not an "Error:".
	if line := exitcode.Line(err); strings.HasPrefix(line, "Error:") || !strings.Contains(line, "/r/unit") {
		t.Fatalf("verdict line %q", line)
	}
	start := captureVerdict(testcapture.CaptureResult{ExitCode: 127, StartErr: errors.New("not found")}, "unit", []string{"nope"})
	if exitcode.Of(start) != 127 {
		t.Fatalf("a command that never started: exit %d, want 127", exitcode.Of(start))
	}
}

func runTestCapture(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	cmd := newTestCaptureCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), err
}

func TestTestCaptureMissingCommandIs127(t *testing.T) {
	t.Setenv(testcapture.EnvTests, "off")
	_, err := runTestCapture(t, "--lane", "unit", "--", "forge-no-such-command-xyz")
	if exitcode.Of(err) != 127 {
		t.Fatalf("forge test capture of a missing command = %v (exit %d), want 127", err, exitcode.Of(err))
	}
}

// testCaptureHelperEnv makes this test binary a lane command that exits with
// the status it names.
const testCaptureHelperEnv = "FORGE_TEST_CAPTURE_CLI_HELPER"

func TestTestCaptureHelperProcess(t *testing.T) {
	code := os.Getenv(testCaptureHelperEnv)
	if code == "" {
		return
	}
	n, _ := strconv.Atoi(code)
	os.Stdout.WriteString(`{"Action":"output","Package":"p","Output":"FAIL\tp\t0.01s\n"}` + "\n")
	os.Exit(n)
}

func TestTestCaptureExitsWithTheCommandsStatus(t *testing.T) {
	testkit.Slow(t, "execs this test binary as the lane's command")
	t.Setenv(testcapture.EnvTests, "")
	t.Setenv(testcapture.EnvRun, "")
	t.Setenv(testCaptureHelperEnv, "3")
	run := t.TempDir()
	if err := os.WriteFile(filepath.Join(run, testreport.RunManifestFile), []byte(`{"schema":"forge.test.run/v1","lanes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTestCapture(t, "--run", run, "--lane", "unit", "--", os.Args[0], "-test.run=^TestTestCaptureHelperProcess$")
	if exitcode.Of(err) != 3 {
		t.Fatalf("forge test capture = %v (exit %d), want the command's 3", err, exitcode.Of(err))
	}
	if out != "FAIL\tp\t0.01s\n" {
		t.Fatalf("terminal = %q, want the stream rendered as go test text", out)
	}
	m, rerr := testreport.ReadLaneManifest(run, "unit")
	if rerr != nil || m.ExitCode != 3 || m.Results != testreport.LaneResultsParsed {
		t.Fatalf("capture.json = %+v, %v", m, rerr)
	}
}
