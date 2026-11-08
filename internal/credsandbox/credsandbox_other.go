// Copyright (c) 2025 Reliant Labs
//go:build !linux

package credsandbox

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Available reports why this machine cannot run the sandbox.
func Available(string) error {
	return errors.New("the credentials sandbox is implemented for Linux only")
}

var unsupportedOnce sync.Once

// Apply leaves cmd as it was: this platform has no unprivileged equivalent of a
// user and mount namespace, so a credentials-none command keeps only the
// environment scrub. The daemon's file tools still refuse the Reliant credential
// files. It says so once, in the daemon's log: credentialed commands get no
// SignalEnv here, so a step that needs one fails closed.
func Apply(*exec.Cmd, string, Plan) string {
	unsupportedOnce.Do(func() {
		logging.Warn("credsandbox: no filesystem sandbox on this platform: credentials-none commands keep only the environment scrub "+
			"(they can still read and write what the daemon's user can, outside the files the daemon's tools refuse), "+
			"and credentialed commands get no "+SignalEnv, "os", runtime.GOOS)
	})
	return ""
}

// ensureCredScratch is never reached here: Wrap prepares the credentialed
// scratch only where the sandbox is in force (Available), which it never is on
// this platform.
func ensureCredScratch() error {
	return errors.New("the credentials sandbox is implemented for Linux only")
}

// Dispatch refuses the helper argument: the helper exists on Linux only.
func Dispatch() {
	dispatched.Store(true)
	if len(os.Args) >= 2 && os.Args[1] == HelperArg {
		os.Exit(RefusedExitCode)
	}
}
