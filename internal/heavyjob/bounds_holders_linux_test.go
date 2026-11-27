// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"path/filepath"
	"testing"
)

func TestLowerBounds_TwoHoldersRestoreOnlyAfterLastRelease(t *testing.T) {
	root := t.TempDir()
	dir := boundsCgroup(t, root, cmdCgroup, "100", "max")
	self := selfIn(t, cmdCgroup)
	first := lowerBoundsAt(self, root, bounds{weight: 25, memoryHigh: 12 << 30})
	t.Cleanup(first.release)
	second := lowerBoundsAt(self, root, bounds{weight: 25, memoryHigh: 12 << 30})
	t.Cleanup(second.release)
	first.release()
	for name, want := range map[string]string{"cpu.weight": "25", "memory.high": "12884901888"} {
		if got := readTrim(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s after first release = %s, want %s", name, got, want)
		}
	}
	second.release()
	for name, want := range map[string]string{"cpu.weight": "100", "memory.high": "max"} {
		if got := readTrim(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s after last release = %s, want %s", name, got, want)
		}
	}
}

func TestLowerBounds_PreservesExistingLowerMemoryLimitWhileActive(t *testing.T) {
	root := t.TempDir()
	dir := boundsCgroup(t, root, cmdCgroup, "100", "12884901888")
	c := lowerBoundsAt(selfIn(t, cmdCgroup), root, bounds{weight: 25, memoryHigh: 1 << 40})
	t.Cleanup(c.release)
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "12884901888" {
		t.Fatalf("memory.high = %s, want existing 12 GiB", got)
	}
	c.release()
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "12884901888" {
		t.Fatalf("memory.high after release = %s, want existing 12 GiB", got)
	}
}
