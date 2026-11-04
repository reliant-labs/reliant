// Copyright (c) 2026 Reliant Labs

//go:build !linux

package diskwatch

import (
	"errors"
	"time"
)

// scanProcesses has no /proc to read here, so nothing about a work directory
// can be proven (gowork.go falls back to its age rule).
func scanProcesses() (procSnapshot, error) {
	return procSnapshot{}, errors.New("no /proc on this platform")
}

// fileBirth is unused without a proof; now, which is never old enough.
func fileBirth(string) time.Time { return time.Now() }
