// File: internal/cli/lint/lint_golangci_lock_test.go
//
// `forge lint` must QUEUE for golangci-lint's machine-global lock, not fail
// on it.
//
// golangci-lint takes an exclusive lock on $TMPDIR/golangci-lint.lock — one
// path for every golangci-lint on the machine — and by default gives up after
// five seconds with exit 3, "parallel golangci-lint is running". On a shared
// box where several agents lint different projects, that failed `forge lint`
// whenever anyone else happened to be linting. The scaffolded .golangci.yml
// sets `allow-serial-runners: true`, but only projects scaffolded or upgraded
// after that line landed have it; control-plane did not, and its lint lane
// failed on contention (reported by the FREE-DAEMON run as F2).
//
// The stub below is golangci-lint with the lock HELD by someone else: it
// exits 3 exactly as the real binary does unless the invocation asked to wait
// for the lock. Every lane that shells golangci-lint is driven through it.

package lint

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/hostslot"
)

// contendedUnlessSerialStub is golangci-lint while another instance holds the
// machine-global lock. With --allow-serial-runners the real binary blocks
// until the lock frees and then runs; the stub models the run that follows.
const contendedUnlessSerialStub = `for a in "$@"; do
  if [ "$a" = "--allow-serial-runners" ]; then echo "0 issues."; exit 0; fi
done
echo "Error: parallel golangci-lint is running" >&2
echo "The command is terminated due to an error: parallel golangci-lint is running" >&2
exit 3`

func TestGolangciLanesQueueForTheMachineLock(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through shell subprocesses; runs in task test")
	}
	t.Run("gate with --fix", func(t *testing.T) {
		fakeGolangciLint(t, contendedUnlessSerialStub)
		rc := testRunCtx(t, false)
		rc.fix = true
		if fs, gated := collectGolangciLintJSON(rc); gated || len(fs) != 0 {
			t.Fatalf("golangci-lint --fix failed on a contended lock: gated=%v %+v", gated, fs)
		}
	})
	t.Run("gate", func(t *testing.T) {
		fakeGolangciLint(t, contendedUnlessSerialStub)
		fs, gated := collectGolangciLintJSON(testRunCtx(t, false))
		if gated || len(fs) != 0 {
			t.Fatalf("--json golangci-lint gated on a contended lock: gated=%v findings=%+v", gated, fs)
		}
	})
	t.Run("json advisory guardrail", func(t *testing.T) {
		fakeGolangciLint(t, contendedUnlessSerialStub)
		// Strict, so an unavailable lane would gate. What must be absent
		// is the could-not-run rule.
		fs, gated, err := collectTypedAccessGuardJSON(testRunCtx(t, true))
		if err != nil || gated {
			t.Fatalf("--json guardrail gated on a contended lock: err=%v gated=%v findings=%+v", err, gated, fs)
		}
		for _, f := range fs {
			if f.Rule == ruleTypedAccessGuardUnavailable {
				t.Fatalf("--json guardrail reported it could not run on a contended lock: %+v", f)
			}
		}
	})
}

// TestGolangciRunArgsKeepTheLockOutsideAnOwnSlot pins the half of the choice
// the stub cannot see. --allow-parallel-runners would ALSO get past a
// contended lock, by not taking it at all — and two full golangci-lint runs at
// once can exhaust a small machine's memory, which is why the lock exists.
// Only a heavy slot of this process's own bounds that, so only there may the
// lock go: inside one, keeping it serialized every lint on the machine while
// each waiter held a slot it could not use.
func TestGolangciRunArgsKeepTheLockOutsideAnOwnSlot(t *testing.T) {
	t.Setenv(hostslot.EnvDir, t.TempDir())
	t.Setenv(hostslot.EnvHeld, "")
	t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "2")
	argv := func() string {
		return strings.Join(golangciRunArgs([]string{"--fix"}, []string{"./internal/..."}), " ")
	}

	got := argv()
	if !strings.HasPrefix(got, "run ") {
		t.Errorf("argv must start with the run subcommand: %q", got)
	}
	if !strings.Contains(got, "--allow-serial-runners") || strings.Contains(got, "--allow-parallel-runners") {
		t.Errorf("outside a slot the argv must wait for golangci-lint's lock, not drop it: %q", got)
	}
	if !strings.HasSuffix(got, "--fix ./internal/...") {
		t.Errorf("extra flags and paths must be passed through in order: %q", got)
	}

	release, err := cmdutil.HoldHeavySlot(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	inSlot := argv()
	release()
	if !strings.Contains(inSlot, "--allow-parallel-runners") || strings.Contains(inSlot, "--allow-serial-runners") {
		t.Errorf("inside its own heavy slot the argv must drop golangci-lint's lock: %q", inSlot)
	}
	if after := argv(); !strings.Contains(after, "--allow-serial-runners") {
		t.Errorf("after the slot is released the argv must wait for the lock again: %q", after)
	}
}
