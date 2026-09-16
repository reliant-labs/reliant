// Copyright (c) 2025 Reliant Labs
package gitutil

import (
	"context"
	"os/exec"

	"github.com/reliant-labs/reliant/internal/osutil"
)

// IndexCommand builds a git command that writes the index, with the
// cancellation policy already applied.
//
// Every index-writing call site goes through here rather than calling
// exec.CommandContext directly, so the policy lives in one place and cannot
// drift across the ~96 git call sites in this repository. What "the policy"
// means:
//
//   - Cancellation sends SIGTERM to the process group and only escalates to a
//     kill after a grace period, instead of os/exec's default immediate
//     SIGKILL. Git removes .git/index.lock from an atexit handler, and an
//     atexit handler does not run under SIGKILL — which is precisely how a
//     cancelled `git add` leaves behind a lock that breaks every subsequent
//     write in that repository.
//   - A stranded lock from an EARLIER death is cleared first when, and only
//     when, it can be shown to be stranded. Prevention cannot help a lock that
//     is already on disk, and SIGKILL still happens outside our control via
//     OOM kills, `kill -9`, and power loss.
//
// The caller runs the returned command and MUST call stop when it is done, so
// the escalation timer cannot outlive it; stop is safe to defer and safe to
// call twice.
//
// dir is the working tree the command runs in and must not be empty — git
// would otherwise inherit the daemon's own working directory and operate on
// whatever repository happens to be there.
func IndexCommand(ctx context.Context, dir string, args ...string) (cmd *exec.Cmd, stop func()) {
	// Clear a provably-stranded lock before spawning, so the command does not
	// fail on a lock whose owner died long ago.
	EnsureIndexWritable(ctx, dir)

	cmd = exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	setCommandProcessGroup(cmd)
	stop = osutil.ApplyGracefulCancel(cmd, osutil.DefaultGraceDelay)
	return cmd, stop
}

// RunIndexCommand runs an index-writing git command to completion and returns
// its combined output, with index-lock failures annotated with remediation the
// user can act on.
//
// This is the form nearly every call site wants; IndexCommand is there for the
// few that need to set something on the command first.
func RunIndexCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd, stop := IndexCommand(ctx, dir, args...)
	defer stop()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return []byte(AnnotateIndexLockError(string(output))), err
	}
	return output, nil
}
