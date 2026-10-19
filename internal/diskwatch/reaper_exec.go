// Copyright (c) 2025 Reliant Labs

package diskwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
)

// errReaperUnderTest refuses to start the real reaper from a test binary:
// os.Executable is the test binary there, so `forge storage auto-gc` would
// re-run the whole suite. Tests inject their own runner.
var errReaperUnderTest = errors.New("refusing to run the storage reaper under `go test` (inject a reaperRunner)")

// reaperCommandArgs is the argument list after the binary: the reliant binary
// embeds forge's CLI, so this is `reliant forge storage auto-gc --policy <path>`.
func reaperCommandArgs(policyPath string) []string {
	return []string{"forge", "storage", "auto-gc", "--policy", policyPath}
}

// execReaper runs `<this binary> forge storage auto-gc --policy <path>`.
//
// It re-executes THIS binary rather than a `forge` found on PATH, as every
// other forge call the daemon makes does (daemonruntime.runForgeSelfExec — not
// importable from here, it imports this package): the forge that answers is
// exactly the one reliant was built against, and it carries the storage
// reaper.
func execReaper(ctx context.Context, policyPath string) (reaperRun, error) {
	if testing.Testing() {
		return reaperRun{}, errReaperUnderTest
	}
	if policyPath == "" {
		return reaperRun{}, errors.New("no storage policy location: the user config directory is unknown; set FORGE_STORAGE_POLICY")
	}
	self, err := os.Executable()
	if err != nil {
		return reaperRun{}, fmt.Errorf("resolve own executable: %w", err)
	}
	return runReaperProcess(ctx, self, reaperCommandArgs(policyPath))
}

// runReaperProcess runs bin with args and returns once it has exited. The child
// runs in the system temp directory so forge does not take a project from the
// daemon's working directory, at the lowest CPU and I/O priority, in its own
// process group so the context can stop the whole of it. A non-zero exit is a
// result; an error is a command that could not start or was stopped by ctx.
func runReaperProcess(ctx context.Context, bin string, args []string) (reaperRun, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = os.TempDir()
	stdout, stderr := &tailBuffer{max: reaperOutputTail}, &tailBuffer{max: reaperOutputTail}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = reaperKillGrace
	prepareReaperCmd(cmd)

	if err := cmd.Start(); err != nil {
		return reaperRun{}, fmt.Errorf("start %s: %w", filepath.Base(bin), err)
	}
	if err := osutil.LowerChildPriority(cmd.Process.Pid); err != nil {
		logging.Debug("[diskwatch] Failed to lower storage reaper priority", "pid", cmd.Process.Pid, "error", err)
	}
	if err := osutil.LowerChildIOPriority(cmd.Process.Pid); err != nil {
		logging.Debug("[diskwatch] Failed to lower storage reaper I/O priority", "pid", cmd.Process.Pid, "error", err)
	}

	runErr := cmd.Wait()
	run := reaperRun{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case ctx.Err() != nil:
		// Stopped by the budget or a cancel: no verdict. Checked before the
		// ExitError case because a killed child also reports an ExitError.
		return run, ctx.Err()
	case errors.As(runErr, &exitErr):
		run.ExitCode = exitErr.ExitCode()
	default:
		return run, runErr
	}
	return run, nil
}
