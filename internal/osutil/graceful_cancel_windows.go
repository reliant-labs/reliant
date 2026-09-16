// Copyright (c) 2025 Reliant Labs
//go:build windows

package osutil

import (
	"os"
)

// terminateGracefully asks the child to stop.
//
// Windows has no SIGTERM. os.Process.Signal accepts only os.Kill on Windows
// (anything else returns "not supported"), and the console-control route
// (GenerateConsoleCtrlEvent) only reaches processes attached to a console —
// which the daemon's children, spawned without one, are not. A graceful stop
// would need each child to cooperate over a named event or job object, which
// no tool we spawn does.
//
// So on Windows this is a kill, and the grace period is moot: the cleanup
// that Unix children run from a SIGTERM handler does not happen. That is a
// real platform gap rather than an oversight, and it is the reason the
// stranded-lock RECOVERY path in internal/gitutil is not merely a backstop
// here — on Windows it is the only mechanism that clears a lock stranded by a
// cancelled git.
func terminateGracefully(proc *os.Process) error {
	return proc.Kill()
}
