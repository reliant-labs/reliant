//go:build !linux

package osutil

import "os"

// Platforms without Linux pidfds retain identity-safe direct-child escalation.
func captureGroupKiller(proc *os.Process) func() { return func() { _ = proc.Kill() } }
