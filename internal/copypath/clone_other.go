// Copyright (c) 2025 Reliant Labs

//go:build !darwin

package copypath

// cloneTree has no single-call tree clone off macOS. Linux's FICLONE clones
// one file at a time and only on btrfs/XFS, which a daemon cannot assume, so
// the portable copy handles every entry there.
func cloneTree(src, dst string) bool { return false }
