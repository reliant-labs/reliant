// Copyright (c) 2026 Reliant Labs
//go:build !linux

package osutil

// GroupMembers cannot list a group's processes without /proc: ok is false,
// and callers fall back to what they can prove without it.
func GroupMembers(int) ([]GroupMember, bool) { return nil, false }

// GroupOwned cannot prove anything without /proc.
func GroupOwned(int, []GroupMember) bool { return false }
