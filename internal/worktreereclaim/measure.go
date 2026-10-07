// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

const (
	// sizeBudget and sizeMaxEntries bound one directory measurement. A
	// worktree with a populated node_modules holds hundreds of thousands of
	// entries; the number shown is a lower bound once either limit trips.
	sizeBudget     = 2 * time.Second
	sizeMaxEntries = 150_000
)

// DirSize adds up regular-file sizes under root within a fixed time and entry
// budget. approx is true when the walk stopped early, so the result is a lower
// bound. Symlinks are not followed.
func DirSize(ctx context.Context, root string) (bytes int64, approx bool) {
	ctx, cancel := context.WithTimeout(ctx, sizeBudget)
	defer cancel()
	entries := 0
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil || entries >= sizeMaxEntries {
			approx = true
			return fs.SkipAll
		}
		entries++
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			bytes += info.Size()
		}
		return nil
	})
	return bytes, approx
}
