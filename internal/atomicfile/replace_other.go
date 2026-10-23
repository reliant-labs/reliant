//go:build !windows

package atomicfile

import "os"

// replaceFile is rename(2), which replaces the target atomically however many
// readers have it open.
func replaceFile(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
