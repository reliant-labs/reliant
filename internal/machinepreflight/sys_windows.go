// Copyright (c) 2026 Reliant Labs
//go:build windows

package machinepreflight

import "io/fs"

// FileOwner is unknown on Windows: ownership is an ACL, not a uid.
func FileOwner(fs.FileInfo) (int, bool) { return 0, false }

// ProcessUID is unknown on Windows.
func ProcessUID() (int, bool) { return 0, false }

// hasTmpCheck reports whether the platform's /tmp is checked: Windows has no
// /tmp, and its temp directory has no sticky bit.
const hasTmpCheck = false
