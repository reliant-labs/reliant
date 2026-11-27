// Copyright (c) 2025 Reliant Labs

//go:build !linux

package gate

import (
	"errors"
	"testing"
)

// Off Linux a disk slot is refused, never silently unbounded (review B2 on
// #3793); the slot tests themselves skip here.
func TestDiskSlotsRefusedOffLinux(t *testing.T) {
	if _, err := lockSlot(t.TempDir()); !errors.Is(err, errSlotsNeedLinux) {
		t.Fatalf("lockSlot = %v, want errSlotsNeedLinux", err)
	}
	if _, err := slotSize(t.TempDir(), t.TempDir()); !errors.Is(err, errSlotsNeedLinux) {
		t.Fatalf("slotSize = %v, want errSlotsNeedLinux", err)
	}
}
