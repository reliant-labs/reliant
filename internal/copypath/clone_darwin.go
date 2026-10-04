// Copyright (c) 2025 Reliant Labs

//go:build darwin

package copypath

import "golang.org/x/sys/unix"

// cloneTree clones src — a file, a symlink, or a whole directory tree — to
// dst in one clonefile(2) call. It reports false when the filesystem cannot
// clone (not APFS, or src and dst on different volumes), and the caller falls
// back to copying.
//
// CLONE_NOFOLLOW clones a symlink as a symlink rather than its target.
// clonefile never follows symlinks inside a directory tree.
func cloneTree(src, dst string) bool {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) == nil
}
