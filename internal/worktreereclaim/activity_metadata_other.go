//go:build !linux

package worktreereclaim

import "os"

func activityFileMetadata(os.FileInfo) string { return "unsupported" }
