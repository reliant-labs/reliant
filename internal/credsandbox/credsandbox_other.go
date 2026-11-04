// Copyright (c) 2025 Reliant Labs
//go:build !linux

package credsandbox

import (
	"errors"
	"os"
	"os/exec"
)

// Available reports why this machine cannot run the sandbox.
func Available(string) error {
	return errors.New("the credentials sandbox is implemented for Linux only")
}

// Apply leaves cmd as it was: this platform has no unprivileged equivalent of a
// user and mount namespace, so a credentials-none command keeps only the
// environment scrub. The daemon's file tools still refuse the Reliant credential
// files.
func Apply(*exec.Cmd, string, Plan) string { return "" }

// Dispatch refuses the helper argument: the helper exists on Linux only.
func Dispatch() {
	dispatched.Store(true)
	if len(os.Args) >= 2 && os.Args[1] == HelperArg {
		os.Exit(RefusedExitCode)
	}
}
