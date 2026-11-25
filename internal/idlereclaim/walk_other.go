// Copyright (c) 2026 Reliant Labs

//go:build !unix

package idlereclaim

import "os"

// deviceOf cannot name a filesystem here, so the scan sees no mounts. Nothing
// is removed here anyway: process inspection is Linux-only, and a directory
// it cannot inspect is held (worktreereclaim.CheckProcessUse).
func deviceOf(os.FileInfo) (uint64, bool) { return 0, false }
