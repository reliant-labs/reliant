package worktreereclaim

import (
	"context"
	"os"
)

type inspectionFilesKey struct{}

// withInspectionFiles exempts ONLY the exact live descriptors the retirement
// inspector itself holds. It is private trusted code, never payload/PID based.
// All other own-process fds, cwd, executable and mappings still veto cleanup.
func withInspectionFiles(ctx context.Context, files []*os.File) context.Context {
	return context.WithValue(ctx, inspectionFilesKey{}, files)
}
func inspectionFile(ctx context.Context, fd uintptr) bool {
	files, _ := ctx.Value(inspectionFilesKey{}).([]*os.File)
	for _, file := range files {
		if file != nil && file.Fd() == fd {
			if _, err := file.Stat(); err == nil {
				return true
			}
		}
	}
	return false
}
