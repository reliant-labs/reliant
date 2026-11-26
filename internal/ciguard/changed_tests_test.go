package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// scripts/ci-changed-tests.sh is a thin wrapper over `forge test flakes
// gate`, which finds the changed tests and runs them three times in one
// `go test` invocation (internal/testflakes' gate tests pin that). What the
// wrapper owns, and these tests pin, is its contract with ci.yml and
// scripts/ci-premerge.sh: its arguments become the gate's, the test command
// is scripts/ci-go-test.sh, and the gate's exit codes become the ones the
// script always had.
//
// The child reads only copies of scripts this process opened: the short
// tier's cache sees the production input, and no command touches the real
// git checkout or builds forge.
func changedTestsFixture(t *testing.T, mode string, env ...string) (out, calls string, err error) {
	t.Helper()
	root := t.TempDir()
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-changed-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed.sh"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "forge-log")
	args := []string{"changed.sh", "fixture-base"}
	if mode != "" {
		args = append(args, mode)
	}
	cmd := exec.Command("bash", args...)
	cmd.Dir = root
	cmd.Env = append(append(os.Environ(), installStubForge(t, root), "FORGE_LOG="+log), env...)
	b, err := cmd.CombinedOutput()
	c, _ := os.ReadFile(log)
	return string(b), string(c), err
}

func TestChangedTestsRunTheFlakeGate(t *testing.T) {
	testkit.Slow(t, "execs bash")
	t.Parallel()
	out, calls, err := changedTestsFixture(t, "short")
	if err != nil {
		t.Fatalf("changed tests: %v\n%s", err, out)
	}
	if strings.TrimSpace(calls) != "test flakes gate --base fixture-base -- scripts/ci-go-test.sh" {
		t.Errorf("want one gate run over the base, through scripts/ci-go-test.sh and without -race; got:\n%s", calls)
	}
}

func TestChangedTestsFullAddsRace(t *testing.T) {
	testkit.Slow(t, "execs bash")
	t.Parallel()
	out, calls, err := changedTestsFixture(t, "full")
	if err != nil {
		t.Fatalf("full changed tests: %v\n%s", err, out)
	}
	if strings.TrimSpace(calls) != "test flakes gate --base fixture-base --full -- scripts/ci-go-test.sh" {
		t.Errorf("full mode must pass --full (-race); got:\n%s", calls)
	}
}

func TestChangedTestsPropagateFailure(t *testing.T) {
	testkit.Slow(t, "execs bash")
	t.Parallel()
	out, _, err := changedTestsFixture(t, "short", "STUB_EXIT=1", "STUB_STDERR=flake gate: FAILED after 3s")
	if err == nil {
		t.Fatalf("a failed changed test must fail the gate:\n%s", out)
	}
	// The command could not start: a failure, as a missing command always was.
	out, _, err = changedTestsFixture(t, "short", "STUB_EXIT=2", "STUB_STDERR=flake gate: could not run scripts/ci-go-test.sh: not found")
	if err == nil {
		t.Fatalf("a test command that cannot start must fail the gate:\n%s", out)
	}
}

// The gate exits 2 when it identifies no changed test; the script always
// passed such a change, including one whose diff could not be read.
func TestChangedTestsPassWithNoChangedTests(t *testing.T) {
	testkit.Slow(t, "execs bash")
	t.Parallel()
	out, _, err := changedTestsFixture(t, "short", "STUB_EXIT=2", "STUB_STDERR=flake gate: no new or edited tests in fixture-base..HEAD")
	if err != nil {
		t.Fatalf("no changed tests must pass: %v\n%s", err, out)
	}
	out, _, err = changedTestsFixture(t, "short", "STUB_EXIT=2", "STUB_STDERR=flake gate: could not identify the changed tests in fixture-base..HEAD: bad revision")
	if err != nil {
		t.Fatalf("an unreadable diff passes, as it always did: %v\n%s", err, out)
	}
	if !strings.Contains(out, "::warning::flake gate: could not identify the changed tests") {
		t.Errorf("an unreadable diff must be a warning, not a silent pass:\n%s", out)
	}
}

func TestChangedTestsRejectInvalidMode(t *testing.T) {
	testkit.Slow(t, "execs bash")
	t.Parallel()
	out, calls, err := changedTestsFixture(t, "typo")
	if err == nil || calls != "" {
		t.Fatalf("unknown mode must not silently pick test flags: %v\n%s\n%s", err, out, calls)
	}
}
