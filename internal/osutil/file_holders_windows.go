// Copyright (c) 2025 Reliant Labs
//go:build windows

package osutil

import "context"

// fileHolders has no Windows implementation, and returning Unknown is the
// deliberate answer rather than a stub to be filled in later.
//
// Enumerating the processes holding a given file on Windows means walking
// system handle tables (NtQuerySystemInformation with
// SystemExtendedHandleInformation, then resolving each handle to a path), which
// needs elevated privileges to be reliable and is exactly the kind of probe
// that returns a plausible wrong answer when it degrades.
//
// Unknown means callers never remove a lock on Windows. That is the safe
// direction: a stranded lock is an error message with a documented manual fix,
// while a deleted live lock is a corrupted concurrent write. Windows users get
// the actionable error text instead of automatic recovery.
func fileHolders(_ context.Context, _ string) FileHoldState {
	return FileHoldUnknown
}
