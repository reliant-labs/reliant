// Copyright (c) 2025 Reliant Labs
//go:build !linux

package osutil

// LowerChildIOPriority is a no-op outside Linux: ioprio_set is a Linux
// syscall, and the other platforms have no per-process I/O priority we apply.
func LowerChildIOPriority(pid int) error {
	return nil
}
