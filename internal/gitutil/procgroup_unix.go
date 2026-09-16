// Copyright (c) 2025 Reliant Labs
//go:build !windows

package gitutil

import (
	"os/exec"
	"syscall"
)

// setCommandProcessGroup puts git in its own process group so cancellation can
// signal the group rather than just the direct child.
//
// It matters for git specifically because git delegates real work to
// subprocesses: a clean filter, a pre-commit hook, `git gc --auto`. The
// process actually holding index.lock at the moment of cancellation may be a
// descendant, and a signal to the parent alone would never reach it.
func setCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
