//go:build !linux

package osutil

import (
	"errors"
	"os"
)

// Platforms without Linux pidfds retain identity-safe direct-child escalation.
func captureGroupKiller(proc *os.Process) func() error {
	return func() error {
		_ = proc.Kill()
		return errors.New("complete process-tree exit unconfirmed: unsupported platform")
	}
}
