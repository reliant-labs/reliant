// Copyright (c) 2025 Reliant Labs
//go:build windows

package gitutil

import "os"

// fileIdentity has no meaningful Windows implementation here, and that costs
// nothing: the liveness probe (osutil.FileHolders) always answers Unknown on
// Windows, so clearStrandedIndexLock returns before ever reaching this.
//
// It reports modification time as a weak identity so the code still compiles
// and still behaves conservatively if the probe ever gains a Windows answer —
// a replaced lock will usually differ, and a false "same" only ever leads back
// to the other conditions, never around them.
func fileIdentity(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return uint64(info.ModTime().UnixNano()), true
}
