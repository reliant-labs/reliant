// Copyright (c) 2025 Reliant Labs
package worktreereclaim

const maxStripWalk = 50_000

// shedsFor is retained for interpreting historical cleanup holds. Automatic
// cleanup no longer strips ignored subtrees: complete safe worktrees retire
// through retireWorkspace; held worktrees retain ALL their files.
func shedsFor(reason HeldReason) bool {
	switch reason {
	case ReasonDirty, ReasonUnpushed, ReasonUnreachable, ReasonData, ReasonOutsideFiles, ReasonNestedRepo:
		return true
	}
	return false
}
