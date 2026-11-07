// Copyright (c) 2026 Reliant Labs
//go:build !linux && !darwin

package osutil

// GroupMembers cannot list a group's processes without a process-table API: ok is false,
// and callers fall back to what they can prove without it.
func GroupMembers(int) ([]GroupMember, bool) { return nil, false }

// GroupOwned cannot prove anything without a process-table API.
func GroupOwned(int, []GroupMember) bool { return false }
