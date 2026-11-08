// Copyright (c) 2026 Reliant Labs
//go:build !linux && !darwin && !windows

package osutil

// isZombie cannot tell a zombie from a live process without a process table.
func isZombie(int) bool { return false }
