package testflakes

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The gate end to end with real git and go: a change adds a test that fails
// its second run, and the gate fails, names it a retry pass and records it.
func TestGateRealGoAndGit(t *testing.T) {
	testkit.Slow(t, "execs git and go test")
	root := newModule(t, map[string]string{"pkg/a/a.go": "package a\n"})
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitIn("init", "-q")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "base")
	write(t, filepath.Join(root, "pkg/a/a_test.go"), `package a

import "testing"

var runs int

func TestSecondRunFails(t *testing.T) {
	runs++
	if runs == 2 {
		t.Fatal("second run")
	}
}
`)
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "add a flaky test")

	// Separate buffers: os/exec copies the child's stderr into its writer
	// from a goroutine of its own.
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	res := Gate(context.Background(), GateOptions{
		Root: root, Base: "HEAD~1", Stdout: out, Stderr: errOut, Getenv: noEnv,
		Environ: goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")),
		Begin:   begin(root, "c1", "t1", time.Now()),
	})
	if res.ExitCode != GateFailed {
		t.Fatalf("exit %d: %s\n%s", res.ExitCode, res.Message, out)
	}
	if !strings.Contains(out.String(), "--- FAIL: TestSecondRunFails") {
		t.Errorf("the stream must render as go test's text:\n%s", out)
	}
	if len(res.RetryPassed) != 1 || res.RetryPassed[0].Name != "TestSecondRunFails" {
		t.Errorf("retry passed = %v\n%s", res.RetryPassed, out)
	}
	m, err := testreport.ReadLaneManifest(filepath.Dir(res.LaneDir), LaneFlakeGate)
	if err != nil || m.Results != testreport.LaneResultsParsed {
		t.Errorf("lane = %+v %v", m, err)
	}
}
