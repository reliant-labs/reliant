// Copyright (c) 2025 Reliant Labs
//go:build windows

package worktreereclaim

// ownedByMe is false on Windows, where ownership is not a uid: the file
// readers in gitfiles.go then always ask git, as they did before.
func ownedByMe(string) bool { return false }
